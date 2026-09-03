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
	"slices"
	"strings"

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
// Local safety caps are TopK and MaxBytesPerHit. The model window is not
// an exclusive quota for this source; Builder applies the unified history
// remainder after all sources emit.
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
	// TokenEstimator overrides the default estimator used for report
	// token accounting. nil -> memory.DefaultTokenEstimator.
	TokenEstimator memory.TokenEstimator
}

// Compile-time interface conformance.
var _ Source = (*VectorRecallSource)(nil)

// Name returns SourceNameVectorRecall.
func (s *VectorRecallSource) Name() string { return SourceNameVectorRecall }

// Fetch performs the recall pipeline: query selection -> embed -> search
// -> render. Errors short-circuit to fail-open and never bubble up.
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
	render = recoveringRenderer(render)

	text := render(in, hits)
	if text == "" {
		rep.Status = StatusSkipped
		rep.Note = "empty render"
		rep.DroppedN = rep.InputN
		return FetchResult{Report: rep}, nil
	}

	rep.OutputN = 1
	rep.Tokens = s.estimateTokens(in.Protocol, text)
	rep.Status = StatusOK
	rep.Note = noteWithRange("", hits)

	msg := schema.NewSystemMessage(in.Protocol, text)
	return FetchResult{Messages: []schema.Message{msg}, Report: rep}, nil
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

// estimateTokens routes through the configured estimator, defaulting to
// memory.DefaultTokenEstimator. The estimator works on schema.Message,
// so the text is wrapped in a system message — safe approximation
// because all VectorRecallSource output is system-role.
func (s *VectorRecallSource) estimateTokens(proto schema.Protocol, text string) int {
	est := s.TokenEstimator
	if est == nil {
		est = memory.DefaultTokenEstimator
	}
	return est(schema.NewSystemMessage(proto, text))
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
	for _, m := range slices.Backward(in.Request.Messages) {

		if m.Role() != schema.RoleUser {
			continue
		}
		if t := strings.TrimSpace(m.Text()); t != "" {
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
