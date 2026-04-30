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
	"fmt"

	"github.com/vogo/aimodel"
	"github.com/vogo/vage/schema"
)

const (
	defaultKeepLastTools = 5

	contextEditStrategyKeepLastK = "keep_last_k"
)

// PlaceholderFunc renders the placeholder text that replaces an elided
// tool_result Content. It receives the original tool_call_id and the
// byte length of the elided text.
type PlaceholderFunc func(toolCallID string, originalBytes int) string

// DefaultContextEditPlaceholder is the built-in placeholder template.
// It mirrors Anthropic's "context editing" wording so a human reading
// the prompt can immediately see what was folded.
func DefaultContextEditPlaceholder(toolCallID string, originalBytes int) string {
	return fmt.Sprintf("[context_edited: tool_result %s elided, %d bytes]", toolCallID, originalBytes)
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
	keepLast       int
	minElidedBytes int
	dispatch       DispatchFunc
	placeholderFn  PlaceholderFunc
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

// WithPlaceholder customises the placeholder text. The function
// receives the original tool_call_id and the byte length of the
// elided text content.
func WithPlaceholder(fn PlaceholderFunc) ContextEditorOption {
	return func(m *ContextEditorMiddleware) {
		if fn != nil {
			m.placeholderFn = fn
		}
	}
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
// shallow copy whose Messages slice has older tool_result entries
// replaced with placeholder copies. Side-effect: emits an event when
// any elision happened and a dispatch is configured.
func (m *ContextEditorMiddleware) edit(ctx context.Context, req *aimodel.ChatRequest) *aimodel.ChatRequest {
	if req == nil || len(req.Messages) == 0 {
		return req
	}

	older, kept, totalElidedBytes := m.scanElidable(req.Messages)
	if len(older) == 0 {
		return req
	}

	if m.minElidedBytes > 0 && totalElidedBytes < m.minElidedBytes {
		return req
	}

	edited, placeholderBytes := m.applyElision(req.Messages, older)

	edReq := *req
	edReq.Messages = edited

	if m.dispatch != nil {
		m.dispatch(ctx, schema.NewEvent(schema.EventContextEdited, "", "", schema.ContextEditedData{
			Edited:        len(older),
			Kept:          kept,
			Total:         len(req.Messages),
			OriginalBytes: totalElidedBytes,
			Placeholder:   placeholderBytes,
			Strategy:      contextEditStrategyKeepLastK,
		}))
	}

	return &edReq
}

// scanElidable walks msgs once, returning the absolute indices of
// tool_result messages that should be elided (every tool_result before
// the last keepLast), the count of tool_result messages kept verbatim,
// and the sum of the elided messages' original Content.Text() byte
// lengths. older is returned in ascending index order so applyElision
// can walk it in lockstep without an auxiliary set.
func (m *ContextEditorMiddleware) scanElidable(msgs []aimodel.Message) ([]int, int, int) {
	var toolIdx []int
	for i := range msgs {
		if msgs[i].Role == aimodel.RoleTool {
			toolIdx = append(toolIdx, i)
		}
	}

	if len(toolIdx) <= m.keepLast {
		return nil, len(toolIdx), 0
	}

	cut := len(toolIdx) - m.keepLast
	older := toolIdx[:cut]
	kept := m.keepLast

	totalBytes := 0
	for _, idx := range older {
		totalBytes += len(msgs[idx].Content.Text())
	}

	return older, kept, totalBytes
}

// applyElision builds a new []aimodel.Message of the same length as
// msgs. Indices in older (ascending order, as produced by scanElidable)
// are replaced with placeholder messages; all others are copied through
// verbatim. Returns the edited slice and the total bytes occupied by
// placeholder strings.
func (m *ContextEditorMiddleware) applyElision(msgs []aimodel.Message, older []int) ([]aimodel.Message, int) {
	out := make([]aimodel.Message, len(msgs))
	placeholderBytes := 0
	cursor := 0 // index into older[]

	for i := range msgs {
		if cursor >= len(older) || older[cursor] != i {
			out[i] = msgs[i]
			continue
		}
		cursor++

		original := msgs[i]
		originalBytes := len(original.Content.Text())
		placeholder := m.placeholderFn(original.ToolCallID, originalBytes)
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
