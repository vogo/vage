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
	"reflect"
	"strings"
	"testing"

	"github.com/vogo/aimodel"
	"github.com/vogo/vage/schema"
)

// captureCompleter records the request the middleware finally sent
// downstream, so tests can compare it against the caller's request.
type captureCompleter struct {
	gotChat   *aimodel.ChatRequest
	gotStream *aimodel.ChatRequest
	chatResp  *aimodel.ChatResponse
}

func (c *captureCompleter) ChatCompletion(_ context.Context, req *aimodel.ChatRequest) (*aimodel.ChatResponse, error) {
	c.gotChat = req
	if c.chatResp == nil {
		return &aimodel.ChatResponse{ID: "ok"}, nil
	}
	return c.chatResp, nil
}

func (c *captureCompleter) ChatCompletionStream(_ context.Context, req *aimodel.ChatRequest) (*aimodel.Stream, error) {
	c.gotStream = req
	return nil, nil
}

// makeReq builds a ReAct-style request: user prompt → assistant tool_calls →
// n tool_result messages, all with synthetic content of size bytes each.
// Returns the request plus the original tool_call_ids in order.
func makeReq(n int, contentBytes int) (*aimodel.ChatRequest, []string) {
	msgs := []aimodel.Message{
		{Role: aimodel.RoleSystem, Content: aimodel.NewTextContent("sys")},
		{Role: aimodel.RoleUser, Content: aimodel.NewTextContent("hello")},
	}

	calls := make([]aimodel.ToolCall, 0, n)
	ids := make([]string, 0, n)
	body := strings.Repeat("x", contentBytes)

	for i := range n {
		id := "call-" + string(rune('a'+i))
		ids = append(ids, id)
		calls = append(calls, aimodel.ToolCall{ID: id, Function: aimodel.FunctionCall{Name: "fake"}})
	}

	msgs = append(msgs, aimodel.Message{Role: aimodel.RoleAssistant, ToolCalls: calls})

	for _, id := range ids {
		msgs = append(msgs, aimodel.Message{
			Role:       aimodel.RoleTool,
			ToolCallID: id,
			Content:    aimodel.NewTextContent(body),
		})
	}

	return &aimodel.ChatRequest{Model: "test", Messages: msgs}, ids
}

// TC-1: the K most recent tool_result messages stay verbatim, every
// older tool_result has its Content replaced with the placeholder, and
// every ToolCallID is preserved.
func TestContextEditor_FoldsOlderToolResults(t *testing.T) {
	cap := &captureCompleter{}
	mw := NewContextEditorMiddleware(WithKeepLastTools(3))
	wrapped := mw.Wrap(cap)

	req, ids := makeReq(7, 100)
	if _, err := wrapped.ChatCompletion(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cap.gotChat == nil {
		t.Fatal("downstream not called")
	}

	if len(cap.gotChat.Messages) != len(req.Messages) {
		t.Fatalf("message count changed: got %d want %d", len(cap.gotChat.Messages), len(req.Messages))
	}

	// Locate the indices of tool messages in the downstream request.
	var toolIdx []int
	for i, m := range cap.gotChat.Messages {
		if m.Role == aimodel.RoleTool {
			toolIdx = append(toolIdx, i)
		}
	}
	if len(toolIdx) != 7 {
		t.Fatalf("expected 7 tool messages, got %d", len(toolIdx))
	}

	// First 4 should be elided; last 3 should be intact.
	for i, idx := range toolIdx[:4] {
		text := cap.gotChat.Messages[idx].Content.Text()
		if !strings.Contains(text, "context_edited") {
			t.Errorf("expected tool[%d] elided, got %q", i, text)
		}
		if cap.gotChat.Messages[idx].ToolCallID != ids[i] {
			t.Errorf("tool[%d] ToolCallID changed: got %q want %q",
				i, cap.gotChat.Messages[idx].ToolCallID, ids[i])
		}
	}
	for i, idx := range toolIdx[4:] {
		text := cap.gotChat.Messages[idx].Content.Text()
		if strings.Contains(text, "context_edited") {
			t.Errorf("expected tool[%d] kept, got placeholder", 4+i)
		}
		if cap.gotChat.Messages[idx].ToolCallID != ids[4+i] {
			t.Errorf("tool[%d] ToolCallID changed", 4+i)
		}
	}
}

// TC-2: the caller's request and its Messages slice are not mutated;
// the middleware copies before editing.
func TestContextEditor_DoesNotMutateCaller(t *testing.T) {
	cap := &captureCompleter{}
	mw := NewContextEditorMiddleware(WithKeepLastTools(2))
	wrapped := mw.Wrap(cap)

	req, _ := makeReq(5, 50)
	original := make([]aimodel.Message, len(req.Messages))
	copy(original, req.Messages)

	if _, err := wrapped.ChatCompletion(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !reflect.DeepEqual(req.Messages, original) {
		t.Fatal("caller Messages slice was mutated by the middleware")
	}

	// Downstream must have seen a different slice header (we built a
	// fresh copy).
	if &req.Messages[0] == &cap.gotChat.Messages[0] {
		t.Fatal("downstream got the caller's slice without copying")
	}
}

// TC-3: when the number of tool_results does not exceed K, no editing
// happens and the request is forwarded verbatim.
func TestContextEditor_BelowThresholdNoOp(t *testing.T) {
	cap := &captureCompleter{}
	dispatched := 0
	mw := NewContextEditorMiddleware(
		WithKeepLastTools(5),
		WithContextEditDispatch(func(_ context.Context, _ schema.Event) { dispatched++ }),
	)

	wrapped := mw.Wrap(cap)
	req, _ := makeReq(3, 100)
	if _, err := wrapped.ChatCompletion(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if dispatched != 0 {
		t.Fatalf("expected no event dispatch, got %d", dispatched)
	}
	// Pass-through: downstream sees the same slice header.
	if &req.Messages[0] != &cap.gotChat.Messages[0] {
		t.Fatal("expected request to pass through unmodified")
	}
}

// TC-4: minElidedBytes blocks edits when the total potentially-freed
// bytes are below the threshold.
func TestContextEditor_MinElidedBytesBlocks(t *testing.T) {
	cap := &captureCompleter{}
	dispatched := 0
	mw := NewContextEditorMiddleware(
		WithKeepLastTools(2),
		WithMinElidedBytes(10_000),
		WithContextEditDispatch(func(_ context.Context, _ schema.Event) { dispatched++ }),
	)
	wrapped := mw.Wrap(cap)

	// 5 tool messages, 100 bytes each → 3 eligible × 100 = 300 freed,
	// well below 10_000. Should not edit.
	req, _ := makeReq(5, 100)
	if _, err := wrapped.ChatCompletion(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if dispatched != 0 {
		t.Fatalf("expected no event dispatch, got %d", dispatched)
	}
	if &req.Messages[0] != &cap.gotChat.Messages[0] {
		t.Fatal("expected pass-through under threshold")
	}
}

// TC-5: the dispatched event payload reflects the actual edit.
func TestContextEditor_EventPayload(t *testing.T) {
	cap := &captureCompleter{}
	var got schema.Event
	mw := NewContextEditorMiddleware(
		WithKeepLastTools(2),
		WithContextEditDispatch(func(_ context.Context, e schema.Event) { got = e }),
	)
	wrapped := mw.Wrap(cap)

	// 5 tool messages, 100 bytes each → 3 elided, 2 kept, 300 bytes freed.
	req, _ := makeReq(5, 100)
	if _, err := wrapped.ChatCompletion(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got.Type != schema.EventContextEdited {
		t.Fatalf("event type: got %q want %q", got.Type, schema.EventContextEdited)
	}

	data, ok := got.Data.(schema.ContextEditedData)
	if !ok {
		t.Fatalf("payload type: got %T", got.Data)
	}

	if data.Edited != 3 {
		t.Errorf("Edited: got %d want 3", data.Edited)
	}
	if data.Kept != 2 {
		t.Errorf("Kept: got %d want 2", data.Kept)
	}
	if data.Total != len(req.Messages) {
		t.Errorf("Total: got %d want %d", data.Total, len(req.Messages))
	}
	if data.OriginalBytes != 300 {
		t.Errorf("OriginalBytes: got %d want 300", data.OriginalBytes)
	}
	if data.Strategy != "keep_last_k" {
		t.Errorf("Strategy: got %q want %q", data.Strategy, "keep_last_k")
	}
	if data.Placeholder == 0 {
		t.Error("Placeholder bytes should be > 0 when edits happened")
	}
}

// TC-6: nil dispatch must not panic when an edit happens.
func TestContextEditor_NilDispatchSafe(t *testing.T) {
	cap := &captureCompleter{}
	mw := NewContextEditorMiddleware(WithKeepLastTools(1))
	wrapped := mw.Wrap(cap)

	req, _ := makeReq(3, 50)
	if _, err := wrapped.ChatCompletion(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cap.gotChat == nil {
		t.Fatal("downstream not called")
	}
}

// TC-7: the streaming path applies the same editing.
func TestContextEditor_StreamPath(t *testing.T) {
	cap := &captureCompleter{}
	mw := NewContextEditorMiddleware(WithKeepLastTools(2))
	wrapped := mw.Wrap(cap)

	req, _ := makeReq(5, 50)
	if _, err := wrapped.ChatCompletionStream(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cap.gotStream == nil {
		t.Fatal("downstream stream not called")
	}

	var elided int
	for _, m := range cap.gotStream.Messages {
		if m.Role == aimodel.RoleTool && strings.Contains(m.Content.Text(), "context_edited") {
			elided++
		}
	}
	if elided != 3 {
		t.Errorf("expected 3 tool messages elided in stream req, got %d", elided)
	}
}

// TC-8 & TC-9: system / user / assistant.ToolCalls are preserved verbatim.
func TestContextEditor_PreservesNonToolMessages(t *testing.T) {
	cap := &captureCompleter{}
	mw := NewContextEditorMiddleware(WithKeepLastTools(0))
	wrapped := mw.Wrap(cap)

	req, ids := makeReq(3, 30)
	if _, err := wrapped.ChatCompletion(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := cap.gotChat
	if got.Messages[0].Role != aimodel.RoleSystem || got.Messages[0].Content.Text() != "sys" {
		t.Errorf("system message changed: %+v", got.Messages[0])
	}
	if got.Messages[1].Role != aimodel.RoleUser || got.Messages[1].Content.Text() != "hello" {
		t.Errorf("user message changed: %+v", got.Messages[1])
	}

	asst := got.Messages[2]
	if asst.Role != aimodel.RoleAssistant {
		t.Fatalf("expected assistant, got %s", asst.Role)
	}
	if len(asst.ToolCalls) != len(ids) {
		t.Fatalf("ToolCalls count changed: got %d want %d", len(asst.ToolCalls), len(ids))
	}
	for i, tc := range asst.ToolCalls {
		if tc.ID != ids[i] {
			t.Errorf("ToolCalls[%d].ID changed: got %q want %q", i, tc.ID, ids[i])
		}
	}
}

// TC-10: WithPlaceholder customises the rendered text.
func TestContextEditor_CustomPlaceholder(t *testing.T) {
	cap := &captureCompleter{}
	mw := NewContextEditorMiddleware(
		WithKeepLastTools(0),
		WithPlaceholder(func(id string, n int) string { return "<<elided " + id + ">>" }),
	)
	wrapped := mw.Wrap(cap)

	req, _ := makeReq(2, 20)
	if _, err := wrapped.ChatCompletion(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, m := range cap.gotChat.Messages {
		if m.Role != aimodel.RoleTool {
			continue
		}
		text := m.Content.Text()
		if !strings.HasPrefix(text, "<<elided ") {
			t.Errorf("custom placeholder not applied: %q", text)
		}
	}
}

// TC-11: CacheBreakpoint flag is carried through to the placeholder
// copy so prompt-cache boundaries do not shift.
func TestContextEditor_PreservesCacheBreakpoint(t *testing.T) {
	cap := &captureCompleter{}
	mw := NewContextEditorMiddleware(WithKeepLastTools(0))
	wrapped := mw.Wrap(cap)

	req, _ := makeReq(2, 10)
	// Mark the first tool_result with a cache breakpoint.
	for i := range req.Messages {
		if req.Messages[i].Role == aimodel.RoleTool {
			req.Messages[i].CacheBreakpoint = true
			break
		}
	}

	if _, err := wrapped.ChatCompletion(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var first aimodel.Message
	for _, m := range cap.gotChat.Messages {
		if m.Role == aimodel.RoleTool {
			first = m
			break
		}
	}
	if !first.CacheBreakpoint {
		t.Fatal("CacheBreakpoint not propagated to placeholder")
	}
}

// TC-12: an empty / nil request is forwarded as-is.
func TestContextEditor_NilOrEmptyRequest(t *testing.T) {
	cap := &captureCompleter{}
	mw := NewContextEditorMiddleware()
	wrapped := mw.Wrap(cap)

	if _, err := wrapped.ChatCompletion(context.Background(), &aimodel.ChatRequest{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cap.gotChat == nil {
		t.Fatal("downstream not called for empty request")
	}
}

// TC-13: WithKeepLastTools(<=0) falls back to default 5.
func TestContextEditor_KeepLastDefaultFallback(t *testing.T) {
	cap := &captureCompleter{}
	mw := NewContextEditorMiddleware(WithKeepLastTools(-1))
	wrapped := mw.Wrap(cap)

	// 5 tools, default keepLast=5 → no edit.
	req, _ := makeReq(5, 10)
	if _, err := wrapped.ChatCompletion(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if &req.Messages[0] != &cap.gotChat.Messages[0] {
		t.Fatal("expected pass-through with default keepLast")
	}
}
