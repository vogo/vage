/*
 * Licensed to the Apache Software Foundation (ASF) under one or more
 * contributor license agreements.  See the NOTICE file distributed with
 * this work for additional information regarding copyright ownership.
 * The ASF licenses this file to You under the Apache License, Version 2.0
 * (the "License"); you may not use this file except in compliance with
 * the License.  You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package largemodel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"

	"github.com/vogo/aimodel"
	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/tool"
)

const (
	defaultKeepLastTools = 5

	contextEditStrategyKeepLastK     = "keep_last_k"
	contextEditStrategyStaleResource = "stale_resource"
	// ContextEditStrategyElideArtifact is the strategy reported when a
	// single oversized tool_result is externalised into the workspace
	// artifact store. The constant is exported so observability hooks
	// (e.g. session/metrics_hook.go) can match on it.
	ContextEditStrategyElideArtifact = "elide_to_artifact"
	// contextEditStrategyElideInline is the fallback strategy used when
	// MaxBytesPerMessage was exceeded but no ArtifactWriter was wired
	// (or the writer returned an error) — the body is replaced with a
	// short inline notice rather than externalised. Kept private
	// because it is a degraded-mode label, not a feature toggle.
	contextEditStrategyElideInline = "elide_inline"
)

// PlaceholderV2Func renders a placeholder that includes the *reason* a
// tool_result was elided (keep_last_k, stale_resource, ...) plus an
// optional human-readable detail (e.g. "file /a/b modified by call_3").
// Reason and detail are produced by the editor; implementations should
// treat empty detail as "no extra context".
type PlaceholderV2Func func(toolCallID string, originalBytes int, reason, detail string) string

// ResourceLookupFunc resolves a tool name to its ResourceTracker. The
// editor consults the lookup once per ToolCall it inspects; returning
// nil means the tool does not advertise resource semantics and the
// stale_resource pass should skip it. The lookup itself must be cheap
// — it is on the per-request hot path.
type ResourceLookupFunc func(toolName string) tool.ResourceTracker

// ArtifactWriter externalises an oversized tool_result body to a
// persistent store keyed by (sessionID, name). The editor consults the
// writer when a single tool_result exceeds MaxBytesPerMessage; on
// success the prompt carries a short reference, on failure the editor
// falls back to an inline notice. Implementations should be safe for
// concurrent calls across distinct sessions.
type ArtifactWriter interface {
	Write(ctx context.Context, sessionID, name string, content []byte) (path string, err error)
}

// SessionIDFunc extracts the session ID associated with an outgoing
// ChatRequest. The middleware needs the ID only to namespace artifact
// names; callers that operate without sessions can leave the option
// unset, in which case the elision pass falls back to the inline form.
type SessionIDFunc func(req *aimodel.ChatRequest) string

// DefaultContextEditPlaceholderV2 is the V2 default. It surfaces the
// editor's reason inline so a human reading the prompt can immediately
// see whether a fold was driven by recency (keep_last_k) or by a later
// write invalidating an earlier read (stale_resource).
func DefaultContextEditPlaceholderV2(toolCallID string, originalBytes int, reason, detail string) string {
	if reason == "" {
		reason = contextEditStrategyKeepLastK
	}
	if detail != "" {
		return fmt.Sprintf("[context_edited: tool_result %s elided (%s — %s), %d bytes]",
			toolCallID, reason, detail, originalBytes)
	}
	return fmt.Sprintf("[context_edited: tool_result %s elided (%s), %d bytes]",
		toolCallID, reason, originalBytes)
}

// ContextEditorMiddleware folds older tool_result messages into short
// placeholders before the request reaches the underlying ChatCompleter,
// so multi-iteration ReAct loops do not pay for the full tool_result
// payload on every turn.
//
// Editing is applied to a SHALLOW COPY of *aimodel.ChatRequest. The
// caller's request and its Messages slice are never mutated; modified
// messages are constructed as new aimodel.Message values placed in a
// fresh slice.
//
// The middleware is stateless: each Chat / Stream call is judged
// independently from req.Messages alone.
type ContextEditorMiddleware struct {
	keepLast           int
	minElidedBytes     int
	maxBytesPerMessage int
	dispatch           DispatchFunc
	placeholderFn      PlaceholderFunc
	placeholderV2      PlaceholderV2Func
	resourceLookup     ResourceLookupFunc
	artifactWriter     ArtifactWriter
	sessionIDFn        SessionIDFunc
}

// ContextEditorOption configures ContextEditorMiddleware.
type ContextEditorOption func(*ContextEditorMiddleware)

// WithKeepLastTools sets how many of the most recent tool_result
// messages to keep verbatim. Older tool_result messages have their
// content replaced with a placeholder. n == 0 means "keep none, elide
// every tool_result"; n < 0 falls back to default (5).
func WithKeepLastTools(n int) ContextEditorOption {
	return func(m *ContextEditorMiddleware) {
		if n < 0 {
			n = defaultKeepLastTools
		}
		m.keepLast = n
	}
}

// WithMinElidedBytes sets the minimum freed-byte budget for a single
// editing pass. If freeing all eligible older tool_results would save
// fewer than n bytes, no editing happens (and no event fires). n <= 0
// disables the threshold (always edit). Default: 0.
func WithMinElidedBytes(n int) ContextEditorOption {
	return func(m *ContextEditorMiddleware) {
		if n < 0 {
			n = 0
		}
		m.minElidedBytes = n
	}
}

// WithContextEditDispatch wires an event sink. When at least one
// tool_result is elided in a request, the middleware dispatches a
// schema.EventContextEdited event. nil dispatch ⇒ silent (no panic).
func WithContextEditDispatch(d DispatchFunc) ContextEditorOption {
	return func(m *ContextEditorMiddleware) { m.dispatch = d }
}

// WithPlaceholderV2 sets a placeholder template that receives the fold
// reason and an optional detail string. When configured, V2 takes
// precedence over any WithPlaceholder template; pass nil to clear.
func WithPlaceholderV2(fn PlaceholderV2Func) ContextEditorOption {
	return func(m *ContextEditorMiddleware) { m.placeholderV2 = fn }
}

// WithStaleResourceTracker enables the stale_resource pass. The lookup
// resolves a tool name to its ResourceTracker; tools that return nil
// are skipped. Without this option the editor only does keep_last_k
// (the historical default).
func WithStaleResourceTracker(fn ResourceLookupFunc) ContextEditorOption {
	return func(m *ContextEditorMiddleware) { m.resourceLookup = fn }
}

// WithMaxBytesPerMessage enables the single-message elision pass. Any
// tool_result whose Content text exceeds n bytes is replaced with a
// short reference (when an ArtifactWriter is wired and a session id is
// resolvable) or an inline notice (otherwise). n <= 0 disables the
// pass; default: disabled.
func WithMaxBytesPerMessage(n int) ContextEditorOption {
	return func(m *ContextEditorMiddleware) {
		if n < 0 {
			n = 0
		}
		m.maxBytesPerMessage = n
	}
}

// WithArtifactWriter wires the externalisation backend used by the
// single-message elision pass. Without a writer the pass falls back
// to the inline notice form, so the option is independent of
// WithMaxBytesPerMessage and may be supplied either way.
func WithArtifactWriter(w ArtifactWriter) ContextEditorOption {
	return func(m *ContextEditorMiddleware) { m.artifactWriter = w }
}

// WithSessionIDFunc tells the editor how to derive the session ID
// from an outgoing ChatRequest. Required for artifact externalisation
// (without a session id the editor cannot namespace the artifact);
// safely no-op for callers who never enable single-message elision.
func WithSessionIDFunc(fn SessionIDFunc) ContextEditorOption {
	return func(m *ContextEditorMiddleware) { m.sessionIDFn = fn }
}

// NewContextEditorMiddleware constructs a middleware. Editing is
// enabled by default (keep last 5 tool_results); pass options to
// customise.
func NewContextEditorMiddleware(opts ...ContextEditorOption) *ContextEditorMiddleware {
	m := &ContextEditorMiddleware{
		keepLast:      defaultKeepLastTools,
		placeholderFn: DefaultContextEditPlaceholder,
	}
	for _, o := range opts {
		o(m)
	}

	return m
}

// Wrap implements Middleware.
func (m *ContextEditorMiddleware) Wrap(next aimodel.ChatCompleter) aimodel.ChatCompleter {
	return &completerFunc{
		chat: func(ctx context.Context, req *aimodel.ChatRequest) (*aimodel.ChatResponse, error) {
			edReq := m.edit(ctx, req)
			return next.ChatCompletion(ctx, edReq)
		},
		stream: func(ctx context.Context, req *aimodel.ChatRequest) (*aimodel.Stream, error) {
			edReq := m.edit(ctx, req)
			return next.ChatCompletionStream(ctx, edReq)
		},
	}
}

// edit returns either the original req (no editing needed) or a
// shallow copy whose Messages slice has tool_result entries replaced
// with placeholder copies. Three passes contribute candidates:
// elide_to_artifact (opt-in via WithMaxBytesPerMessage), stale_resource
// (opt-in via WithStaleResourceTracker), and keep_last_k (always on,
// controlled by keepLast). Side-effect: emits an event when any
// elision happened and a dispatch is configured.
func (m *ContextEditorMiddleware) edit(ctx context.Context, req *aimodel.ChatRequest) *aimodel.ChatRequest {
	if req == nil || len(req.Messages) == 0 {
		return req
	}

	elideByIdx := m.scanByElide(ctx, req)
	keepLastIdx, totalTools := m.scanByKeepLastK(req.Messages)
	staleByIdx := m.scanByStale(req.Messages)

	indexSet := make(map[int]struct{}, len(keepLastIdx)+len(staleByIdx)+len(elideByIdx))
	for _, idx := range keepLastIdx {
		indexSet[idx] = struct{}{}
	}
	for idx := range staleByIdx {
		indexSet[idx] = struct{}{}
	}
	for idx := range elideByIdx {
		indexSet[idx] = struct{}{}
	}

	if len(indexSet) == 0 {
		return req
	}

	allIdx := make([]int, 0, len(indexSet))
	for idx := range indexSet {
		allIdx = append(allIdx, idx)
	}
	sort.Ints(allIdx)

	totalElidedBytes := 0
	for _, idx := range allIdx {
		totalElidedBytes += len(req.Messages[idx].Content.Text())
	}

	if m.minElidedBytes > 0 && totalElidedBytes < m.minElidedBytes {
		return req
	}

	resolver := strategyResolver{staleByIdx: staleByIdx, elideByIdx: elideByIdx}

	edited, placeholderBytes := m.applyElision(req.Messages, allIdx, resolver)

	edReq := *req
	edReq.Messages = edited

	if m.dispatch != nil {
		m.dispatch(ctx, schema.NewEvent(schema.EventContextEdited, "", "", schema.ContextEditedData{
			Edited:        len(allIdx),
			Kept:          totalTools - len(allIdx),
			Total:         len(req.Messages),
			OriginalBytes: totalElidedBytes,
			Placeholder:   placeholderBytes,
			Strategy:      resolver.dominantStrategy(allIdx),
		}))
	}

	return &edReq
}

// scanByKeepLastK returns the absolute indices of tool_result messages
// that the recency budget wants to elide (every tool_result before the
// last keepLast) plus the total count of tool_result messages. The
// indices are ascending so callers can union with other strategies via
// mergeElideIndices.
func (m *ContextEditorMiddleware) scanByKeepLastK(msgs []aimodel.Message) ([]int, int) {
	var toolIdx []int
	for i := range msgs {
		if msgs[i].Role == aimodel.RoleTool {
			toolIdx = append(toolIdx, i)
		}
	}

	if len(toolIdx) <= m.keepLast {
		return nil, len(toolIdx)
	}

	cut := len(toolIdx) - m.keepLast
	older := make([]int, cut)
	copy(older, toolIdx[:cut])
	return older, len(toolIdx)
}

// scanByStale walks msgs and returns the indices of tool_result
// messages whose underlying read has been invalidated by a *later*
// write to the same resource ID. The map value is a human-readable
// detail (e.g. "file /a/b modified by call_3") suitable for inclusion
// in the placeholder. Returns nil when stale detection is disabled,
// when no writes are observed, or when no read is shadowed.
func (m *ContextEditorMiddleware) scanByStale(msgs []aimodel.Message) map[int]string {
	if m.resourceLookup == nil {
		return nil
	}

	type callDesc struct {
		toolName     string
		args         map[string]any
		assistantIdx int
	}
	type writeEntry struct {
		toolCallID   string
		assistantIdx int
	}

	callInfo := make(map[string]callDesc)
	latestWrite := make(map[string]writeEntry)

	// Pass 1: index every assistant tool_call and record the latest
	// write per resource ID. Args parsing is tolerant — a malformed
	// arguments string demotes the call to "no resource" rather than
	// failing the whole request.
	for i := range msgs {
		msg := &msgs[i]
		if msg.Role != aimodel.RoleAssistant || len(msg.ToolCalls) == 0 {
			continue
		}
		for _, tc := range msg.ToolCalls {
			args := parseToolArgs(tc.Function.Arguments)
			callInfo[tc.ID] = callDesc{
				toolName:     tc.Function.Name,
				args:         args,
				assistantIdx: i,
			}

			tracker := m.resourceLookup(tc.Function.Name)
			if tracker == nil || args == nil {
				continue
			}
			for _, ref := range tracker.ResourceIDs(args) {
				if ref.Mode != tool.ResourceWrite || ref.ID == "" {
					continue
				}
				if existing, ok := latestWrite[ref.ID]; !ok || existing.assistantIdx < i {
					latestWrite[ref.ID] = writeEntry{toolCallID: tc.ID, assistantIdx: i}
				}
			}
		}
	}

	if len(latestWrite) == 0 {
		return nil
	}

	// Pass 2: visit every tool_result in order and decide if any of
	// the read refs it embodies has been invalidated by a strictly
	// later write. The first matching ref wins for the placeholder
	// detail to keep the prompt short.
	staleByIdx := make(map[int]string)
	for i := range msgs {
		msg := &msgs[i]
		if msg.Role != aimodel.RoleTool {
			continue
		}
		info, ok := callInfo[msg.ToolCallID]
		if !ok || info.args == nil {
			continue
		}
		tracker := m.resourceLookup(info.toolName)
		if tracker == nil {
			continue
		}
		for _, ref := range tracker.ResourceIDs(info.args) {
			if ref.Mode != tool.ResourceRead || ref.ID == "" {
				continue
			}
			w, ok := latestWrite[ref.ID]
			if !ok || w.assistantIdx <= info.assistantIdx {
				continue
			}
			staleByIdx[i] = "file " + ref.ID + " modified by " + w.toolCallID
			break
		}
	}

	if len(staleByIdx) == 0 {
		return nil
	}
	return staleByIdx
}

// elideOutcome is what scanByElide records for each index it decides
// to externalise. reason picks between elide_to_artifact (success) and
// elide_inline (degraded fallback). detail carries the human-friendly
// hint embedded in the placeholder — typically "see artifacts/..." or
// "no artifact store".
type elideOutcome struct {
	reason string
	detail string
}

// strategyResolver is the single point that decides which elision
// strategy applies to a message index. Both applyElision (per-index
// placeholder reason/detail) and the EventContextEdited dominant
// strategy consult it, so the "artifact elision > stale_resource >
// keep_last_k" precedence lives in exactly one place — adding a new
// strategy means extending this resolver, not editing the scan, apply
// and event paths independently.
type strategyResolver struct {
	staleByIdx map[int]string
	elideByIdx map[int]elideOutcome
}

// strategyForIndex returns the placeholder reason and detail for a
// single edited index. Precedence: an elide outcome (artifact or the
// degraded inline form) wins, then stale_resource, then keep_last_k.
func (r strategyResolver) strategyForIndex(idx int) (reason, detail string) {
	if e, ok := r.elideByIdx[idx]; ok {
		return e.reason, e.detail
	}
	if d, ok := r.staleByIdx[idx]; ok {
		return contextEditStrategyStaleResource, d
	}
	return contextEditStrategyKeepLastK, ""
}

// dominantStrategy folds the actually-edited index set into the single
// strategy reported on EventContextEdited. It ranks the same way as
// strategyForIndex — artifact elision above stale above keep_last_k —
// but reports in the event vocabulary: a degraded inline elision is not
// itself elevated to artifact, so such an index contributes its
// underlying stale_resource or keep_last_k cause instead.
func (r strategyResolver) dominantStrategy(edited []int) string {
	best := contextEditStrategyKeepLastK
	for _, idx := range edited {
		s := contextEditStrategyKeepLastK
		if e, ok := r.elideByIdx[idx]; ok && e.reason == ContextEditStrategyElideArtifact {
			s = ContextEditStrategyElideArtifact
		} else if _, ok := r.staleByIdx[idx]; ok {
			s = contextEditStrategyStaleResource
		}
		if dominantStrategyRank(s) > dominantStrategyRank(best) {
			best = s
		}
	}
	return best
}

// dominantStrategyRank ranks the event-level strategy vocabulary so
// dominantStrategy can pick the most informative one. Higher wins.
func dominantStrategyRank(strategy string) int {
	switch strategy {
	case ContextEditStrategyElideArtifact:
		return 3
	case contextEditStrategyStaleResource:
		return 2
	default:
		return 1
	}
}

// applyElision builds a new []aimodel.Message of the same length as
// msgs. Indices in elideIdx (ascending) are replaced with placeholder
// messages; all others are copied through verbatim. The per-index
// reason/detail come from the shared strategyResolver.
func (m *ContextEditorMiddleware) applyElision(
	msgs []aimodel.Message,
	elideIdx []int,
	resolver strategyResolver,
) ([]aimodel.Message, int) {
	out := make([]aimodel.Message, len(msgs))
	placeholderBytes := 0
	cursor := 0

	for i := range msgs {
		if cursor >= len(elideIdx) || elideIdx[cursor] != i {
			out[i] = msgs[i]
			continue
		}
		cursor++

		original := msgs[i]
		originalBytes := len(original.Content.Text())

		reason, detail := resolver.strategyForIndex(i)

		placeholder := m.renderPlaceholder(original.ToolCallID, originalBytes, reason, detail)
		placeholderBytes += len(placeholder)

		out[i] = aimodel.Message{
			Role:            aimodel.RoleTool,
			Content:         aimodel.NewTextContent(placeholder),
			ToolCallID:      original.ToolCallID,
			CacheBreakpoint: original.CacheBreakpoint,
		}
	}

	return out, placeholderBytes
}

// scanByElide visits every tool_result whose Content text exceeds
// maxBytesPerMessage and decides how to externalise it. Returns the
// outcome map keyed by message index, or nil when the pass is disabled
// or no message tripped the threshold. The pass writes to the artifact
// store synchronously — write failures degrade to elide_inline rather
// than aborting the request.
func (m *ContextEditorMiddleware) scanByElide(ctx context.Context, req *aimodel.ChatRequest) map[int]elideOutcome {
	if m.maxBytesPerMessage <= 0 {
		return nil
	}

	out := make(map[int]elideOutcome)
	sid := ""
	if m.sessionIDFn != nil {
		sid = m.sessionIDFn(req)
	}

	for i := range req.Messages {
		msg := &req.Messages[i]
		if msg.Role != aimodel.RoleTool {
			continue
		}
		body := msg.Content.Text()
		if len(body) <= m.maxBytesPerMessage {
			continue
		}
		out[i] = m.elideOne(ctx, sid, body)
	}

	if len(out) == 0 {
		return nil
	}
	return out
}

// elideOne externalises one body. It first attempts to write the body
// via the configured ArtifactWriter; on success the placeholder carries
// a "see artifacts/<name>" reference. When the writer is missing, the
// session id is empty, or the write itself fails, the placeholder
// degrades to an inline notice that still reports the original byte
// count so the LLM knows roughly what got dropped.
func (m *ContextEditorMiddleware) elideOne(ctx context.Context, sid, body string) elideOutcome {
	if m.artifactWriter == nil || sid == "" {
		return elideOutcome{
			reason: contextEditStrategyElideInline,
			detail: fmt.Sprintf("no artifact store, %s dropped", humanBytes(len(body))),
		}
	}

	hash := sha256.Sum256([]byte(body))
	name := "elided-" + hex.EncodeToString(hash[:8]) + ".txt"

	if _, err := m.artifactWriter.Write(ctx, sid, name, []byte(body)); err != nil {
		slog.Warn("largemodel.ContextEditor: artifact write failed; degrading to inline",
			"session_id", sid, "name", name, "err", err)
		return elideOutcome{
			reason: contextEditStrategyElideInline,
			detail: fmt.Sprintf("artifact write failed, %s dropped", humanBytes(len(body))),
		}
	}

	return elideOutcome{
		reason: ContextEditStrategyElideArtifact,
		detail: fmt.Sprintf("see artifacts/%s, %s", name, humanBytes(len(body))),
	}
}

// humanBytes formats n as e.g. "12.3 KiB" / "4.5 MiB" using binary
// prefixes. Bytes < 1 KiB render as the plain number with the "B"
// suffix.
func humanBytes(n int) string {
	const (
		k = 1024
		m = k * 1024
	)
	switch {
	case n >= m:
		return fmt.Sprintf("%.1f MiB", float64(n)/float64(m))
	case n >= k:
		return fmt.Sprintf("%.1f KiB", float64(n)/float64(k))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// parseToolArgs decodes the JSON arguments string the LLM emitted. It
// returns nil for malformed or empty payloads — callers must treat nil
// as "no resource info available", never as "empty object".
func parseToolArgs(raw string) map[string]any {
	if raw == "" {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}
