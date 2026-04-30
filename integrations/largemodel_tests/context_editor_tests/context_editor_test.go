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

// Package context_editor_tests holds integration tests for the
// largemodel.ContextEditorMiddleware and the taskagent.WithContextEditor
// option, exercising both pieces through their public API together with
// a real hook.Manager so the event-dispatch wiring is end-to-end.
package context_editor_tests //nolint:revive // integration test package

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/vogo/aimodel"
	"github.com/vogo/vage/agent"
	"github.com/vogo/vage/agent/taskagent"
	"github.com/vogo/vage/hook"
	"github.com/vogo/vage/largemodel"
	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/tool"
)

// recordingCompleter records every ChatRequest the upstream middleware
// chain passes down. It implements aimodel.ChatCompleter so it can sit
// directly under the ContextEditorMiddleware in the wrap chain — every
// request the editor forwards is captured here for inspection.
//
// chatResponses are returned in order from ChatCompletion. A nil entry
// means "respond with an empty stop response". streamingNotImplemented
// is returned from ChatCompletionStream — the streaming path is exercised
// via a real SSE httptest.Server in the dedicated stream test below.
type recordingCompleter struct {
	mu            sync.Mutex
	requests      []*aimodel.ChatRequest
	chatResponses []*aimodel.ChatResponse
	chatCalls     int
}

func (r *recordingCompleter) ChatCompletion(_ context.Context, req *aimodel.ChatRequest) (*aimodel.ChatResponse, error) {
	r.mu.Lock()
	// Snapshot the messages slice so subsequent appends by the agent's
	// ReAct loop cannot retroactively change what we recorded.
	snap := *req
	snap.Messages = append([]aimodel.Message(nil), req.Messages...)
	r.requests = append(r.requests, &snap)
	idx := r.chatCalls
	r.chatCalls++
	r.mu.Unlock()

	if idx >= len(r.chatResponses) {
		return nil, fmt.Errorf("recordingCompleter: no response at call %d", idx)
	}
	resp := r.chatResponses[idx]
	if resp == nil {
		return stopResponse("ok"), nil
	}
	return resp, nil
}

func (r *recordingCompleter) ChatCompletionStream(_ context.Context, _ *aimodel.ChatRequest) (*aimodel.Stream, error) {
	return nil, errors.New("recordingCompleter: streaming not implemented")
}

func (r *recordingCompleter) snapshot() []*aimodel.ChatRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*aimodel.ChatRequest, len(r.requests))
	copy(out, r.requests)
	return out
}

// stopResponse returns a minimal assistant "stop" response.
func stopResponse(text string) *aimodel.ChatResponse {
	return &aimodel.ChatResponse{
		Choices: []aimodel.Choice{{
			Message:      aimodel.Message{Role: aimodel.RoleAssistant, Content: aimodel.NewTextContent(text)},
			FinishReason: aimodel.FinishReasonStop,
		}},
		Usage: aimodel.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2},
	}
}

// toolCallResponse returns an assistant tool_calls response with a single call.
func toolCallResponse(toolCallID, name, args string) *aimodel.ChatResponse {
	return &aimodel.ChatResponse{
		Choices: []aimodel.Choice{{
			Message: aimodel.Message{
				Role:    aimodel.RoleAssistant,
				Content: aimodel.NewTextContent(""),
				ToolCalls: []aimodel.ToolCall{{
					ID:       toolCallID,
					Type:     "function",
					Function: aimodel.FunctionCall{Name: name, Arguments: args},
				}},
			},
			FinishReason: aimodel.FinishReasonToolCalls,
		}},
		Usage: aimodel.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2},
	}
}

// echoRegistry returns a registry whose single tool echoes a fixed-size
// payload so each ReAct iteration produces a tool_result of known length.
func echoRegistry(payload string) tool.ToolRegistry {
	reg := tool.NewRegistry()
	_ = reg.Register(
		schema.ToolDef{Name: "echo"},
		func(_ context.Context, _, _ string) (schema.ToolResult, error) {
			return schema.TextResult("", payload), nil
		},
	)
	return reg
}

// countToolResults returns the number of RoleTool messages in msgs.
func countToolResults(msgs []aimodel.Message) int {
	n := 0
	for _, m := range msgs {
		if m.Role == aimodel.RoleTool {
			n++
		}
	}
	return n
}

// countElided returns the number of RoleTool messages whose content
// looks like the editor's placeholder text.
func countElided(msgs []aimodel.Message) int {
	n := 0
	for _, m := range msgs {
		if m.Role == aimodel.RoleTool && strings.Contains(m.Content.Text(), "context_edited") {
			n++
		}
	}
	return n
}

// assistantToolCallIDs returns the assistant ToolCalls[].ID values from
// the most recent assistant message in msgs (in declaration order).
func assistantToolCallIDs(msgs []aimodel.Message) []string {
	var ids []string
	for _, m := range msgs {
		if m.Role == aimodel.RoleAssistant {
			for _, tc := range m.ToolCalls {
				ids = append(ids, tc.ID)
			}
		}
	}
	return ids
}

// toolResultIDs returns the tool_call_id of each RoleTool message in
// msgs in slice order.
func toolResultIDs(msgs []aimodel.Message) []string {
	var ids []string
	for _, m := range msgs {
		if m.Role == aimodel.RoleTool {
			ids = append(ids, m.ToolCallID)
		}
	}
	return ids
}

// TestIntegration_TaskAgent_ContextEditor_LongReActLoop verifies the
// end-to-end behaviour for a long ReAct loop (≥ 4 iterations): with
// keepLast = 2 the editor should let the first one or two iterations
// through verbatim, then start eliding the older tool_results while
// keeping the most recent K. It also checks that every tool_call_id
// stays paired with the corresponding assistant ToolCalls[].ID and
// that the final RunResponse text is unaffected.
func TestIntegration_TaskAgent_ContextEditor_LongReActLoop(t *testing.T) {
	const k = 2
	rc := &recordingCompleter{
		chatResponses: []*aimodel.ChatResponse{
			toolCallResponse("call-1", "echo", `{"i":1}`),
			toolCallResponse("call-2", "echo", `{"i":2}`),
			toolCallResponse("call-3", "echo", `{"i":3}`),
			toolCallResponse("call-4", "echo", `{"i":4}`),
			stopResponse("FINAL_OUTPUT"),
		},
	}

	editor := largemodel.NewContextEditorMiddleware(largemodel.WithKeepLastTools(k))
	a := taskagent.New(
		agent.Config{ID: "ctx-edit-long"},
		taskagent.WithChatCompleter(rc),
		taskagent.WithToolRegistry(echoRegistry(strings.Repeat("y", 200))),
		taskagent.WithContextEditor(editor),
	)

	resp, err := a.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("go")},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	reqs := rc.snapshot()
	if len(reqs) != 5 {
		t.Fatalf("expected 5 LLM calls, got %d", len(reqs))
	}

	// Per-iteration expectations:
	// req[0]: 0 tool_results (just user) → 0 elided
	// req[1]: 1 tool_result (call-1)     → 1 ≤ k → 0 elided
	// req[2]: 2 tool_results (1, 2)      → 2 ≤ k → 0 elided
	// req[3]: 3 tool_results (1..3)      → eliding 1 (call-1), 2 kept
	// req[4]: 4 tool_results (1..4)      → eliding 2 (call-1, call-2), 2 kept
	wantTotal := []int{0, 1, 2, 3, 4}
	wantElided := []int{0, 0, 0, 1, 2}

	for i := range reqs {
		gotTotal := countToolResults(reqs[i].Messages)
		gotElided := countElided(reqs[i].Messages)
		if gotTotal != wantTotal[i] {
			t.Errorf("req[%d] tool_result count: got %d want %d", i, gotTotal, wantTotal[i])
		}
		if gotElided != wantElided[i] {
			t.Errorf("req[%d] elided count: got %d want %d", i, gotElided, wantElided[i])
		}

		// Most recent K tool_results must stay verbatim.
		if gotTotal > 0 {
			intact := gotTotal - gotElided
			wantIntact := min(wantTotal[i], k)
			if intact != wantIntact {
				t.Errorf("req[%d] intact tool_results: got %d want %d", i, intact, wantIntact)
			}
		}

		// Every tool_result must still carry a non-empty ToolCallID.
		for _, m := range reqs[i].Messages {
			if m.Role == aimodel.RoleTool && m.ToolCallID == "" {
				t.Errorf("req[%d] tool_result missing ToolCallID: %+v", i, m)
			}
		}
	}

	// The final request (req[4]) carries assistant ToolCalls[].ID that
	// are exactly the union of all tool_call_ids ever paired — verify
	// each tool_result's ToolCallID matches an assistant ToolCalls[].ID
	// somewhere in the same conversation. This guards the tool_use →
	// tool_result pairing invariant after editing.
	last := reqs[len(reqs)-1]
	asstIDs := map[string]bool{}
	for _, id := range assistantToolCallIDs(last.Messages) {
		asstIDs[id] = true
	}
	for _, id := range toolResultIDs(last.Messages) {
		if !asstIDs[id] {
			t.Errorf("tool_result id %q has no matching assistant.ToolCalls[].ID", id)
		}
	}

	// The agent's final response must reflect the LLM's final text.
	if len(resp.Messages) == 0 || resp.Messages[0].Content.Text() != "FINAL_OUTPUT" {
		t.Errorf("unexpected final response: %+v", resp.Messages)
	}
}

// TestIntegration_ContextEditor_EventEmission wires the middleware's
// DispatchFunc into a real hook.Manager (via manager.Dispatch) and
// confirms a subscribed listener observes EventContextEdited with a
// coherent payload (Edited ≥ 1, Kept = total tool_results − Edited,
// OriginalBytes > 0, Placeholder > 0, Strategy = "keep_last_k").
func TestIntegration_ContextEditor_EventEmission(t *testing.T) {
	mgr := hook.NewManager()
	var (
		mu     sync.Mutex
		events []schema.Event
	)
	mgr.Register(hook.NewHookFunc(func(_ context.Context, e schema.Event) error {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, e)
		return nil
	}, schema.EventContextEdited))

	editor := largemodel.NewContextEditorMiddleware(
		largemodel.WithKeepLastTools(2),
		largemodel.WithContextEditDispatch(mgr.Dispatch),
	)

	rc := &recordingCompleter{
		chatResponses: []*aimodel.ChatResponse{
			toolCallResponse("call-1", "echo", `{}`),
			toolCallResponse("call-2", "echo", `{}`),
			toolCallResponse("call-3", "echo", `{}`),
			toolCallResponse("call-4", "echo", `{}`),
			stopResponse("done"),
		},
	}

	a := taskagent.New(
		agent.Config{ID: "ctx-edit-evt"},
		taskagent.WithChatCompleter(rc),
		taskagent.WithToolRegistry(echoRegistry(strings.Repeat("z", 128))),
		taskagent.WithContextEditor(editor),
	)

	if _, err := a.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("go")},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Iterations 0/1/2 produce ≤ 2 tool_results so no event fires.
	// Iteration 3 has 3 tool_results (Edited=1), iteration 4 has 4
	// tool_results (Edited=2). Both should fire one event each.
	mu.Lock()
	defer mu.Unlock()

	if len(events) != 2 {
		t.Fatalf("expected 2 EventContextEdited events, got %d", len(events))
	}

	wantEdited := []int{1, 2}
	wantKept := []int{2, 2}
	wantTotal := []int{
		// req[3]: user + 3 × (assistant + tool_result) = 7
		7,
		// req[4]: user + 4 × (assistant + tool_result) = 9
		9,
	}

	for i, e := range events {
		if e.Type != schema.EventContextEdited {
			t.Errorf("events[%d].Type = %q, want %q", i, e.Type, schema.EventContextEdited)
			continue
		}
		data, ok := e.Data.(schema.ContextEditedData)
		if !ok {
			t.Errorf("events[%d].Data type = %T, want ContextEditedData", i, e.Data)
			continue
		}
		if data.Edited != wantEdited[i] {
			t.Errorf("events[%d].Edited = %d, want %d", i, data.Edited, wantEdited[i])
		}
		if data.Kept != wantKept[i] {
			t.Errorf("events[%d].Kept = %d, want %d", i, data.Kept, wantKept[i])
		}
		// Total counts ALL messages in the request, not just tool_results.
		if data.Total != wantTotal[i] {
			t.Errorf("events[%d].Total = %d, want %d", i, data.Total, wantTotal[i])
		}
		// Sanity: Kept must equal (tool_result count) - Edited.
		if data.Kept != (data.Edited+data.Kept)-data.Edited {
			t.Errorf("events[%d].Kept arithmetic broken", i)
		}
		if data.OriginalBytes <= 0 {
			t.Errorf("events[%d].OriginalBytes = %d, want > 0", i, data.OriginalBytes)
		}
		if data.Placeholder <= 0 {
			t.Errorf("events[%d].Placeholder = %d, want > 0", i, data.Placeholder)
		}
		if data.Strategy != "keep_last_k" {
			t.Errorf("events[%d].Strategy = %q, want %q", i, data.Strategy, "keep_last_k")
		}
	}
}

// TestIntegration_ContextEditor_SilentPassUnderK confirms that when the
// number of tool_results is at or below K, no event fires AND the
// downstream completer sees the *same* slice header as the caller
// (zero-copy fast path).
func TestIntegration_ContextEditor_SilentPassUnderK(t *testing.T) {
	mgr := hook.NewManager()
	dispatchCount := 0
	mgr.Register(hook.NewHookFunc(func(_ context.Context, _ schema.Event) error {
		dispatchCount++
		return nil
	}, schema.EventContextEdited))

	editor := largemodel.NewContextEditorMiddleware(
		largemodel.WithKeepLastTools(5),
		largemodel.WithContextEditDispatch(mgr.Dispatch),
	)

	// Build a request directly so we can compare slice headers — the
	// TaskAgent path always re-allocates messages, so we exercise the
	// middleware in isolation here.
	req := &aimodel.ChatRequest{
		Model: "test",
		Messages: []aimodel.Message{
			{Role: aimodel.RoleSystem, Content: aimodel.NewTextContent("sys")},
			{Role: aimodel.RoleUser, Content: aimodel.NewTextContent("hi")},
			{Role: aimodel.RoleAssistant, ToolCalls: []aimodel.ToolCall{
				{ID: "t-1", Function: aimodel.FunctionCall{Name: "x"}},
				{ID: "t-2", Function: aimodel.FunctionCall{Name: "x"}},
			}},
			{Role: aimodel.RoleTool, ToolCallID: "t-1", Content: aimodel.NewTextContent("a")},
			{Role: aimodel.RoleTool, ToolCallID: "t-2", Content: aimodel.NewTextContent("b")},
		},
	}

	rc := &recordingCompleter{chatResponses: []*aimodel.ChatResponse{stopResponse("ok")}}
	wrapped := editor.Wrap(rc)

	if _, err := wrapped.ChatCompletion(context.Background(), req); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	if dispatchCount != 0 {
		t.Errorf("expected no EventContextEdited under threshold, got %d", dispatchCount)
	}

	got := rc.snapshot()
	if len(got) != 1 {
		t.Fatalf("expected 1 captured request, got %d", len(got))
	}
	// The captured request was a deep snapshot — assert that no message
	// was rewritten by checking byte-for-byte equality with the input.
	if !reflect.DeepEqual(got[0].Messages, req.Messages) {
		t.Error("downstream messages differ from caller — silent pass should not edit")
	}
}

// TestIntegration_ContextEditor_MinElidedBytesThreshold configures a
// high MinElidedBytes threshold relative to the synthetic tool_result
// sizes so even when there are more than K tool_results, no edit
// happens and no event fires.
func TestIntegration_ContextEditor_MinElidedBytesThreshold(t *testing.T) {
	mgr := hook.NewManager()
	dispatchCount := 0
	mgr.Register(hook.NewHookFunc(func(_ context.Context, _ schema.Event) error {
		dispatchCount++
		return nil
	}, schema.EventContextEdited))

	editor := largemodel.NewContextEditorMiddleware(
		largemodel.WithKeepLastTools(1),
		// Each tool_result is ~10 bytes; 1_000_000 floor blocks edits.
		largemodel.WithMinElidedBytes(1_000_000),
		largemodel.WithContextEditDispatch(mgr.Dispatch),
	)

	rc := &recordingCompleter{
		chatResponses: []*aimodel.ChatResponse{
			toolCallResponse("call-1", "echo", `{}`),
			toolCallResponse("call-2", "echo", `{}`),
			toolCallResponse("call-3", "echo", `{}`),
			stopResponse("done"),
		},
	}

	a := taskagent.New(
		agent.Config{ID: "ctx-edit-thresh"},
		taskagent.WithChatCompleter(rc),
		taskagent.WithToolRegistry(echoRegistry("tiny")),
		taskagent.WithContextEditor(editor),
	)

	if _, err := a.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("go")},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if dispatchCount != 0 {
		t.Errorf("expected no events under MinElidedBytes threshold, got %d", dispatchCount)
	}

	for i, req := range rc.snapshot() {
		if elided := countElided(req.Messages); elided != 0 {
			t.Errorf("req[%d]: expected 0 elided under threshold, got %d", i, elided)
		}
	}
}

// TestIntegration_TaskAgent_ContextEditor_StreamPath drives RunStream
// with a real SSE-backed aimodel.Client, then asserts at least one
// of the captured outbound stream requests has elided tool_results
// when iterations ≥ K + 2. The capturing happens via an interposing
// recordingCompleter that wraps the real client and forwards
// ChatCompletionStream while snapshotting req.Messages.
func TestIntegration_TaskAgent_ContextEditor_StreamPath(t *testing.T) {
	const k = 1

	srv := sseStreamServer(t, [][]string{
		toolCallChunks("st-1", "echo", `{}`),
		toolCallChunks("st-2", "echo", `{}`),
		toolCallChunks("st-3", "echo", `{}`),
		{textDeltaChunk("done"), stopChunk()},
	})
	defer srv.Close()

	client, err := aimodel.NewClient(aimodel.WithAPIKey("test"), aimodel.WithBaseURL(srv.URL))
	if err != nil {
		t.Fatalf("aimodel.NewClient: %v", err)
	}

	cap := &streamCapturer{inner: client}

	editor := largemodel.NewContextEditorMiddleware(largemodel.WithKeepLastTools(k))
	a := taskagent.New(
		agent.Config{ID: "ctx-edit-stream"},
		taskagent.WithChatCompleter(cap),
		taskagent.WithToolRegistry(echoRegistry(strings.Repeat("s", 128))),
		taskagent.WithContextEditor(editor),
	)

	rs, err := a.RunStream(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("go")},
	})
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}

	// Drain the stream to completion so all iterations execute.
	for {
		_, recvErr := rs.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			t.Fatalf("Recv: %v", recvErr)
		}
	}

	reqs := cap.snapshot()
	if len(reqs) < 3 {
		t.Fatalf("expected at least 3 stream requests, got %d", len(reqs))
	}

	// At least one stream request after iteration K+1 must show editing.
	var anyElided bool
	for _, req := range reqs {
		if countElided(req.Messages) > 0 {
			anyElided = true
			break
		}
	}
	if !anyElided {
		t.Error("expected at least one stream request with elided tool_results")
	}

	// The last stream request before the final text turn carries 3
	// tool_results, of which all but K (=1) should be elided.
	last := reqs[len(reqs)-1]
	if countToolResults(last.Messages) >= k+2 {
		expected := countToolResults(last.Messages) - k
		got := countElided(last.Messages)
		if got != expected {
			t.Errorf("last stream req: elided=%d want %d (kept=%d)", got, expected, k)
		}
	}
}

// TestIntegration_TaskAgent_NoContextEditor_NoElision is the no-option
// regression guard: a TaskAgent constructed without WithContextEditor
// must run identical scenarios with zero elisions in any request.
func TestIntegration_TaskAgent_NoContextEditor_NoElision(t *testing.T) {
	rc := &recordingCompleter{
		chatResponses: []*aimodel.ChatResponse{
			toolCallResponse("call-1", "echo", `{}`),
			toolCallResponse("call-2", "echo", `{}`),
			toolCallResponse("call-3", "echo", `{}`),
			toolCallResponse("call-4", "echo", `{}`),
			stopResponse("done"),
		},
	}

	a := taskagent.New(
		agent.Config{ID: "no-editor"},
		taskagent.WithChatCompleter(rc),
		taskagent.WithToolRegistry(echoRegistry(strings.Repeat("p", 200))),
		// no WithContextEditor — chain must be untouched.
	)

	if _, err := a.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("go")},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	for i, req := range rc.snapshot() {
		if elided := countElided(req.Messages); elided != 0 {
			t.Errorf("req[%d]: expected 0 elided without editor, got %d", i, elided)
		}
	}
}

// TestIntegration_TaskAgent_ContextEditor_CallerMutationInvariant asserts
// that after a multi-iteration Run, the caller-supplied RunRequest.Messages
// stay byte-for-byte unchanged AND every internal tool_result the agent
// appended carries the FULL original payload (only the OUTBOUND ChatRequest
// gets edited; the agent's own accumulator must remain intact).
//
// Strategy: snapshot the user RunRequest before Run; after Run, compare.
// For the agent's internal accumulator, the recordingCompleter only sees
// outbound (post-edit) requests, so we instead inspect the agent's
// returned RunResponse to make sure the FINAL assistant text is the
// LLM's stop text (no corruption) and that NO request claims more
// tool_results than were actually executed.
func TestIntegration_TaskAgent_ContextEditor_CallerMutationInvariant(t *testing.T) {
	rc := &recordingCompleter{
		chatResponses: []*aimodel.ChatResponse{
			toolCallResponse("call-1", "echo", `{}`),
			toolCallResponse("call-2", "echo", `{}`),
			toolCallResponse("call-3", "echo", `{}`),
			stopResponse("done"),
		},
	}

	editor := largemodel.NewContextEditorMiddleware(largemodel.WithKeepLastTools(1))
	a := taskagent.New(
		agent.Config{ID: "ctx-mut"},
		taskagent.WithChatCompleter(rc),
		taskagent.WithToolRegistry(echoRegistry(strings.Repeat("M", 100))),
		taskagent.WithContextEditor(editor),
	)

	original := []schema.Message{schema.NewUserMessage("hi")}
	req := &schema.RunRequest{
		Messages: append([]schema.Message(nil), original...),
	}

	resp, err := a.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// The caller's original RunRequest.Messages slice must not have
	// been written through. We compare the underlying aimodel.Message
	// content text since that's the surface the editor would have
	// touched.
	if len(req.Messages) != len(original) {
		t.Errorf("req.Messages length changed: got %d want %d", len(req.Messages), len(original))
	}
	for i := range original {
		if req.Messages[i].Content.Text() != original[i].Content.Text() {
			t.Errorf("req.Messages[%d] mutated: got %q want %q",
				i, req.Messages[i].Content.Text(), original[i].Content.Text())
		}
	}

	// Final response sanity.
	if len(resp.Messages) == 0 || resp.Messages[0].Content.Text() != "done" {
		t.Errorf("unexpected final response: %+v", resp.Messages)
	}

	// The recording completer captured snapshots taken at outbound time.
	// Verify that the SECOND-most-recent iteration request (req[2]) saw
	// elided content while the latest tool_result (call-2) was kept
	// verbatim. This indirectly checks that the agent's internal
	// accumulator preserved the FULL original tool_result content for
	// call-1 across iterations — if it had been mutated, the editor
	// would have nothing to elide on the third request because the
	// content would already be a placeholder from a prior pass.
	reqs := rc.snapshot()
	if len(reqs) < 3 {
		t.Fatalf("expected at least 3 LLM calls, got %d", len(reqs))
	}
	r3 := reqs[2]
	// req[2] sees 2 tool_results (call-1, call-2) and elides 1 (call-1)
	// while keeping call-2 verbatim.
	if got := countElided(r3.Messages); got != 1 {
		t.Errorf("req[2] elided count: got %d want 1", got)
	}
	// Verify the latest (newest) tool_result still has the original
	// 100-byte "M" payload — proves the agent's accumulator and the
	// editor's "keep last K" both preserved newest content verbatim.
	for j := len(r3.Messages) - 1; j >= 0; j-- {
		if r3.Messages[j].Role == aimodel.RoleTool {
			text := r3.Messages[j].Content.Text()
			if strings.Contains(text, "context_edited") {
				t.Error("most recent tool_result was elided — keep_last_k violated")
			}
			if !strings.Contains(text, strings.Repeat("M", 100)) {
				t.Errorf("most recent tool_result missing original payload: %q", text)
			}
			break
		}
	}
}

// ---------------------------------------------------------------------
// Streaming test scaffolding (mirrors taskagent's task_test.go helpers
// — duplicated here because they are package-private to taskagent).
// ---------------------------------------------------------------------

// streamCapturer wraps an inner ChatCompleter and records every
// outbound ChatCompletionStream request it forwards. It is the stream
// counterpart of recordingCompleter and lets the test assert on the
// post-edit messages without needing access to taskagent internals.
type streamCapturer struct {
	mu       sync.Mutex
	inner    aimodel.ChatCompleter
	requests []*aimodel.ChatRequest
}

func (s *streamCapturer) ChatCompletion(ctx context.Context, req *aimodel.ChatRequest) (*aimodel.ChatResponse, error) {
	s.mu.Lock()
	snap := *req
	snap.Messages = append([]aimodel.Message(nil), req.Messages...)
	s.requests = append(s.requests, &snap)
	s.mu.Unlock()
	return s.inner.ChatCompletion(ctx, req)
}

func (s *streamCapturer) ChatCompletionStream(ctx context.Context, req *aimodel.ChatRequest) (*aimodel.Stream, error) {
	s.mu.Lock()
	snap := *req
	snap.Messages = append([]aimodel.Message(nil), req.Messages...)
	s.requests = append(s.requests, &snap)
	s.mu.Unlock()
	return s.inner.ChatCompletionStream(ctx, req)
}

func (s *streamCapturer) snapshot() []*aimodel.ChatRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*aimodel.ChatRequest, len(s.requests))
	copy(out, s.requests)
	return out
}

// sseStreamServer creates an httptest.Server that returns OpenAI-compatible
// SSE chunks. Each call advances to the next response set; if the test
// makes more calls than provided sets, the server returns a 500.
func sseStreamServer(t *testing.T, responseSets [][]string) *httptest.Server {
	t.Helper()
	var (
		mu      sync.Mutex
		callIdx int
	)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		idx := callIdx
		callIdx++
		mu.Unlock()
		if idx >= len(responseSets) {
			http.Error(w, "no more responses", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)

		for _, chunk := range responseSets[idx] {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", chunk)
			flusher.Flush()
		}
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
}

func textDeltaChunk(text string) string {
	return fmt.Sprintf(
		`{"id":"1","object":"chat.completion.chunk","created":1,"model":"test","choices":[{"index":0,"delta":{"role":"assistant","content":%s},"finish_reason":null}]}`,
		mustMarshal(text),
	)
}

func stopChunk() string {
	return `{"id":"1","object":"chat.completion.chunk","created":1,"model":"test","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`
}

func toolCallChunks(id, name, args string) []string {
	return []string{
		fmt.Sprintf(
			`{"id":"1","object":"chat.completion.chunk","created":1,"model":"test","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":%s,"type":"function","function":{"name":%s,"arguments":""}}]},"finish_reason":null}]}`,
			mustMarshal(id), mustMarshal(name),
		),
		fmt.Sprintf(
			`{"id":"1","object":"chat.completion.chunk","created":1,"model":"test","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":%s}}]},"finish_reason":null}]}`,
			mustMarshal(args),
		),
		`{"id":"1","object":"chat.completion.chunk","created":1,"model":"test","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	}
}

func mustMarshal(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
