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

package vctx

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/vogo/aimodel"
	"github.com/vogo/vage/memory"
	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/vector"
)

// vectorTruncSuffix is appended to text that has been character-truncated
// to fit a per-hit byte cap or the overall budget. A short marker keeps
// the LLM from treating partial text as authoritative.
const vectorTruncSuffix = " ... [truncated]"

// HitsRenderer formats a list of search hits into a single message body.
// The FetchInput is passed in so renderers can adapt by Intent or
// SessionID. Returning "" makes VectorRecallSource emit Status="skipped".
type HitsRenderer func(in FetchInput, hits []vector.SearchHit) string

// VectorRecallSource is an optional Source. It does NOT implement
// MustIncludeSource — recall is an enhancement, not a precondition.
//
// The source self-controls budget: when in.Budget > 0 it drops
// lowest-score hits, then character-truncates the last surviving hit so
// the emitted message is guaranteed to fit. This avoids a footgun where
// Builder's trim-by-token would otherwise drop the single aggregated
// message entirely.
//
// All failure modes are fail-open per Builder convention: nil Store /
// Embedder, empty query, embed/search errors, and zero-hit results all
// surface as Status="skipped" or Status="error" with the Builder
// continuing onto the next source.
type VectorRecallSource struct {
	// Store is the backend used for similarity search. nil -> skipped.
	Store vector.VectorStore
	// Embedder turns the query text into a vector. nil -> skipped.
	Embedder vector.Embedder
	// TopK caps the number of hits requested from the store. 0 -> the
	// store's default TopK applies.
	TopK int
	// MinScore filters out hits below this score before rendering.
	// 0 -> no threshold.
	MinScore float32
	// MetadataEquals is forwarded to the store as a declarative filter.
	// Real backends push it down; MapVectorStore applies it client-side.
	MetadataEquals map[string]any
	// Predicate is forwarded as a client-side post-filter. Use sparingly
	// on large stores — it cannot be pushed to a remote backend.
	Predicate func(vector.Document) bool
	// Render overrides the default hits-to-message renderer.
	Render HitsRenderer
	// QueryFn computes the query text from the FetchInput. nil falls
	// back to defaultQuery: prefer Intent, else last user message.
	QueryFn func(in FetchInput) string
	// MaxBytesPerHit clamps each hit's text to this many bytes before
	// rendering. 0 = unlimited. Catches a single oversized Document
	// before it hogs the whole budget.
	MaxBytesPerHit int
	// TokenEstimator overrides the default estimator used for self-trim.
	// nil -> memory.DefaultTokenEstimator.
	TokenEstimator memory.TokenEstimator
}

// Compile-time interface conformance.
var _ Source = (*VectorRecallSource)(nil)

// Name returns SourceNameVectorRecall.
func (s *VectorRecallSource) Name() string { return SourceNameVectorRecall }

// Fetch performs the recall pipeline: query selection -> embed -> search
// -> render -> self-trim. Errors short-circuit to fail-open and never
// bubble up.
func (s *VectorRecallSource) Fetch(ctx context.Context, in FetchInput) (FetchResult, error) {
	rep := schema.ContextSourceReport{Source: SourceNameVectorRecall}

	if s.Store == nil || s.Embedder == nil {
		rep.Status = StatusSkipped
		rep.Note = "no store / no embedder"
		return FetchResult{Report: rep}, nil
	}

	query := s.computeQuery(in)
	if query == "" {
		rep.Status = StatusSkipped
		rep.Note = "empty query"
		return FetchResult{Report: rep}, nil
	}

	vec, err := s.Embedder.Embed(ctx, query)
	if err != nil {
		slog.Warn("vctx: vector embed", "error", err)
		rep.Status = StatusError
		rep.Error = err.Error()
		return FetchResult{Report: rep}, nil
	}

	hits, err := s.Store.Search(ctx, vec, vector.SearchOptions{
		TopK:           s.TopK,
		MinScore:       s.MinScore,
		MetadataEquals: s.MetadataEquals,
		Predicate:      s.Predicate,
	})
	if err != nil {
		slog.Warn("vctx: vector search", "error", err)
		rep.Status = StatusError
		rep.Error = err.Error()
		return FetchResult{Report: rep}, nil
	}

	rep.InputN = len(hits)
	if len(hits) == 0 {
		rep.Status = StatusSkipped
		rep.Note = "no hits"
		return FetchResult{Report: rep}, nil
	}

	hits = s.applyMaxBytesPerHit(hits)

	render := s.Render
	if render == nil {
		render = defaultHitsRender
	}
	// Wrap a user-supplied renderer so a panic does not violate the
	// fail-open contract. defaultHitsRender is internal and safe; wrapping
	// uniformly keeps the call site simple.
	render = recoveringRenderer(render)

	text, hits, truncatedToFit := s.fitToBudget(in.Budget, hits, render, in)
	if text == "" {
		rep.Status = StatusSkipped
		rep.Note = "empty render"
		rep.DroppedN = rep.InputN
		return FetchResult{Report: rep}, nil
	}

	rep.OutputN = 1
	rep.DroppedN = rep.InputN - len(hits)
	rep.Tokens = s.estimateTokens(text)
	if truncatedToFit {
		rep.Status = StatusTruncated
		rep.Note = noteWithRange("truncated to fit budget", hits)
	} else {
		rep.Status = StatusOK
		rep.Note = noteWithRange("", hits)
	}

	msg := aimodel.Message{
		Role:    aimodel.RoleSystem,
		Content: aimodel.NewTextContent(text),
	}
	return FetchResult{Messages: []aimodel.Message{msg}, Report: rep}, nil
}

// computeQuery selects the query text for this Fetch. QueryFn wins when
// provided; otherwise we use the documented defaultQuery rule.
func (s *VectorRecallSource) computeQuery(in FetchInput) string {
	if s.QueryFn != nil {
		return s.QueryFn(in)
	}
	return defaultQuery(in)
}

// applyMaxBytesPerHit truncates each hit's text in place when the option
// is set. The trailing marker tells the LLM the value was clipped.
func (s *VectorRecallSource) applyMaxBytesPerHit(hits []vector.SearchHit) []vector.SearchHit {
	if s.MaxBytesPerHit <= 0 {
		return hits
	}
	for i := range hits {
		hits[i].Document.Text = clampText(hits[i].Document.Text, s.MaxBytesPerHit)
	}
	return hits
}

// fitToBudget renders the hits into a single body and self-trims so the
// estimated token count is ≤ budget. It first drops the lowest-score
// hits one by one; if even a single hit overflows, it byte-truncates
// that hit's text. budget == 0 means unlimited and skips trimming.
func (s *VectorRecallSource) fitToBudget(budget int, hits []vector.SearchHit, render HitsRenderer, in FetchInput) (string, []vector.SearchHit, bool) {
	text := render(in, hits)
	if budget <= 0 {
		return text, hits, false
	}

	tokens := s.estimateTokens(text)
	if tokens <= budget {
		return text, hits, false
	}

	// Drop lowest-score hits (last in the slice — Search returns sorted
	// descending) until we fit or only one remains.
	for len(hits) > 1 && tokens > budget {
		hits = hits[:len(hits)-1]
		text = render(in, hits)
		tokens = s.estimateTokens(text)
	}

	if tokens <= budget {
		return text, hits, true
	}

	// Single remaining hit still overflows. Character-truncate its text
	// and re-render. We approximate "bytes per token" via the default
	// heuristic of 4 bytes/token; refine in one shrinking loop to
	// guarantee correctness even with a custom estimator.
	if len(hits) == 0 {
		return "", hits, true
	}
	original := hits[0].Document.Text
	maxBytes := budget * 4
	for maxBytes > 0 {
		hits[0].Document.Text = clampText(original, maxBytes)
		text = render(in, hits)
		if s.estimateTokens(text) <= budget {
			break
		}
		maxBytes /= 2
	}
	if maxBytes <= 0 {
		// Pathological case: even one byte exceeds budget. Yield empty.
		return "", hits, true
	}
	return text, hits, true
}

// estimateTokens routes through the configured estimator, defaulting to
// memory.DefaultTokenEstimator. The estimator works on schema.Message,
// so the text is wrapped in a system message — safe approximation
// because all VectorRecallSource output is system-role.
func (s *VectorRecallSource) estimateTokens(text string) int {
	est := s.TokenEstimator
	if est == nil {
		est = memory.DefaultTokenEstimator
	}
	return est(schema.FromAIModelMessage(aimodel.Message{
		Role:    aimodel.RoleSystem,
		Content: aimodel.NewTextContent(text),
	}))
}

// defaultQuery is the fallback when VectorRecallSource.QueryFn is nil.
// It prefers a non-empty Intent, then walks the request messages
// backwards looking for the most recent user message that contains
// extractable text. Tool-result-only or empty messages are skipped so
// the recall has a meaningful query.
func defaultQuery(in FetchInput) string {
	if in.Intent != "" {
		return in.Intent
	}
	if in.Request == nil || len(in.Request.Messages) == 0 {
		return ""
	}
	for i := len(in.Request.Messages) - 1; i >= 0; i-- {
		m := in.Request.Messages[i]
		if m.Role != aimodel.RoleUser {
			continue
		}
		if t := strings.TrimSpace(m.Content.Text()); t != "" {
			return t
		}
	}
	return ""
}

// recoveringRenderer wraps a HitsRenderer with a deferred recover so a
// panicking user-supplied renderer does not bring down the Builder. A
// recovered panic is logged and treated as an empty render, which makes
// the Source emit Status="skipped" — consistent with the fail-open
// contract.
func recoveringRenderer(r HitsRenderer) HitsRenderer {
	return func(in FetchInput, hits []vector.SearchHit) (out string) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Warn("vctx: vector renderer panicked", "panic", rec)
				out = ""
			}
		}()
		return r(in, hits)
	}
}

// defaultHitsRender writes a numbered list of hits with score and text.
// Layout deliberately mimics how a human would summarize: stable,
// grep-friendly, low-overhead for the LLM.
func defaultHitsRender(_ FetchInput, hits []vector.SearchHit) string {
	if len(hits) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Relevant Memories\n")
	b.WriteString("(Recalled via vector similarity. Use as background context, not as authoritative quotes.)\n\n")
	for i, h := range hits {
		fmt.Fprintf(&b, "%d. (score=%.2f) %s\n", i+1, h.Score, strings.TrimSpace(h.Document.Text))
	}
	return b.String()
}

// clampText returns s truncated to maxBytes including the trunc suffix.
// When s already fits, it is returned unchanged. When maxBytes is too
// small to even hold the suffix, the suffix is dropped so the byte-trim
// loop can keep making progress (otherwise it would floor at suffix
// length and never converge).
func clampText(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	suffix := vectorTruncSuffix
	if maxBytes <= len(suffix) {
		return s[:maxBytes]
	}
	return s[:maxBytes-len(suffix)] + suffix
}

// noteWithRange formats a "hits=N score=[min..max]" suffix and prepends
// any preamble. Empty preamble produces just the metric.
func noteWithRange(preamble string, hits []vector.SearchHit) string {
	if len(hits) == 0 {
		return preamble
	}
	minS, maxS := hits[0].Score, hits[0].Score
	for _, h := range hits[1:] {
		if h.Score < minS {
			minS = h.Score
		}
		if h.Score > maxS {
			maxS = h.Score
		}
	}
	metric := fmt.Sprintf("hits=%d score=[%.2f..%.2f]", len(hits), minS, maxS)
	if preamble == "" {
		return metric
	}
	return preamble + "; " + metric
}
