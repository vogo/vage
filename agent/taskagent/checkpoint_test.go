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

package taskagent

import (
	"context"
	"errors"
	"testing"

	"github.com/vogo/aimodel"
	"github.com/vogo/vage/agent"
	"github.com/vogo/vage/checkpoint"
	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/tool"
)

// TestRun_NoCheckpointStore_NoOp asserts that without WithIterationStore
// the agent behaves exactly as before — Run completes normally and there
// are no calls into the (absent) store.
func TestRun_NoCheckpointStore_NoOp(t *testing.T) {
	mock := &mockChatCompleter{
		responses: []*aimodel.ChatResponse{stopResponse("done")},
	}
	a := New(agent.Config{ID: "a1"}, WithChatCompleter(mock))

	resp, err := a.Run(context.Background(), &schema.RunRequest{
		SessionID: "sess-noop",
		Messages:  []schema.Message{schema.NewUserMessage("hi")},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if resp.StopReason != schema.StopReasonComplete {
		t.Errorf("StopReason = %q, want complete", resp.StopReason)
	}
}

// TestRun_WritesCheckpointPerIteration asserts that a 3-iteration Run
// (tool, tool, stop) leaves exactly 3 checkpoints in the store, with
// only the last one Final.
func TestRun_WritesCheckpointPerIteration(t *testing.T) {
	mock := &mockChatCompleter{
		responses: []*aimodel.ChatResponse{
			toolCallResponse("tc-1", "echo", `{"v":"a"}`),
			toolCallResponse("tc-2", "echo", `{"v":"b"}`),
			stopResponse("final"),
		},
	}
	store := checkpoint.NewMapIterationStore()
	registry := newEchoRegistry()

	a := New(agent.Config{ID: "a1"},
		WithChatCompleter(mock),
		WithIterationStore(store),
		WithToolRegistry(registry),
	)

	if _, err := a.Run(context.Background(), &schema.RunRequest{
		SessionID: "sess-3iter",
		Messages:  []schema.Message{schema.NewUserMessage("go")},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	metas, err := store.List(context.Background(), "sess-3iter")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(metas) != 3 {
		t.Fatalf("checkpoints = %d, want 3", len(metas))
	}
	for i, m := range metas {
		wantFinal := i == 2
		if m.Final != wantFinal {
			t.Errorf("metas[%d].Final = %v, want %v", i, m.Final, wantFinal)
		}
		if m.Sequence != i+1 {
			t.Errorf("metas[%d].Sequence = %d, want %d", i, m.Sequence, i+1)
		}
	}
	if metas[2].StopReason != schema.StopReasonComplete {
		t.Errorf("final StopReason = %q, want complete", metas[2].StopReason)
	}
}

// TestResume_MissingStore_ReturnsInvalidArgument verifies the precondition.
func TestResume_MissingStore_ReturnsInvalidArgument(t *testing.T) {
	mock := &mockChatCompleter{}
	a := New(agent.Config{ID: "a1"}, WithChatCompleter(mock))
	_, err := a.Resume(context.Background(), "sess-x")
	if !errors.Is(err, checkpoint.ErrInvalidArgument) {
		t.Errorf("err = %v, want ErrInvalidArgument", err)
	}
}

// TestResume_NoCheckpoint_ReturnsNotFound verifies the empty-session path.
func TestResume_NoCheckpoint_ReturnsNotFound(t *testing.T) {
	store := checkpoint.NewMapIterationStore()
	a := New(agent.Config{ID: "a1"},
		WithChatCompleter(&mockChatCompleter{}),
		WithIterationStore(store),
	)
	_, err := a.Resume(context.Background(), "sess-empty")
	if !errors.Is(err, checkpoint.ErrCheckpointNotFound) {
		t.Errorf("err = %v, want ErrCheckpointNotFound", err)
	}
}

// TestResume_AlreadyFinal_ReturnsErrAlreadyFinal verifies the Final
// short-circuit in Resume.
func TestResume_AlreadyFinal_ReturnsErrAlreadyFinal(t *testing.T) {
	mock := &mockChatCompleter{
		responses: []*aimodel.ChatResponse{stopResponse("done")},
	}
	store := checkpoint.NewMapIterationStore()
	a := New(agent.Config{ID: "a1"},
		WithChatCompleter(mock),
		WithIterationStore(store),
	)
	if _, err := a.Run(context.Background(), &schema.RunRequest{
		SessionID: "sess-final",
		Messages:  []schema.Message{schema.NewUserMessage("hi")},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	_, err := a.Resume(context.Background(), "sess-final")
	if !errors.Is(err, checkpoint.ErrAlreadyFinal) {
		t.Errorf("err = %v, want ErrAlreadyFinal", err)
	}
}

// TestResume_AfterFailedRun_ContinuesFromCheckpoint verifies the core
// resume scenario: a 3-iter plan crashes during the second LLM call;
// after a fresh agent + Resume, the run completes successfully and the
// final stop reason is Complete.
func TestResume_AfterFailedRun_ContinuesFromCheckpoint(t *testing.T) {
	store := checkpoint.NewMapIterationStore()
	registry := newEchoRegistry()

	// First Run: returns one tool call, then errors out on second call.
	mock1 := &mockChatCompleter{
		responses: []*aimodel.ChatResponse{
			toolCallResponse("tc-1", "echo", `{"v":"a"}`),
		},
		// After consuming the single response, the second ChatCompletion
		// returns "no more responses" — simulating a crash.
	}
	a1 := New(agent.Config{ID: "agent-resume"},
		WithChatCompleter(mock1),
		WithIterationStore(store),
		WithToolRegistry(registry),
	)
	_, err := a1.Run(context.Background(), &schema.RunRequest{
		SessionID: "sess-resume",
		Messages:  []schema.Message{schema.NewUserMessage("plan")},
	})
	if err == nil {
		t.Fatal("first Run: want error from mock running out, got nil")
	}

	// Verify exactly one (Final=false) checkpoint was persisted.
	metas, listErr := store.List(context.Background(), "sess-resume")
	if listErr != nil {
		t.Fatalf("List: %v", listErr)
	}
	if len(metas) != 1 {
		t.Fatalf("checkpoints after crash = %d, want 1", len(metas))
	}
	if metas[0].Final {
		t.Error("crashed run should not have Final=true checkpoint")
	}

	// Second agent (fresh instance, same store): Resume should pick up
	// the partial run and finish it via a stop response.
	mock2 := &mockChatCompleter{
		responses: []*aimodel.ChatResponse{stopResponse("resumed-done")},
	}
	a2 := New(agent.Config{ID: "agent-resume"},
		WithChatCompleter(mock2),
		WithIterationStore(store),
		WithToolRegistry(registry),
	)
	resp, err := a2.Resume(context.Background(), "sess-resume")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if resp.StopReason != schema.StopReasonComplete {
		t.Errorf("StopReason = %q, want complete", resp.StopReason)
	}
	if got := resp.Messages[0].Content.Text(); got != "resumed-done" {
		t.Errorf("response text = %q, want resumed-done", got)
	}

	// After Resume the store should now hold 2 checkpoints (1 from the
	// crashed Run + 1 Final from Resume). The latest must be Final.
	metas, _ = store.List(context.Background(), "sess-resume")
	if len(metas) != 2 {
		t.Errorf("checkpoints after resume = %d, want 2", len(metas))
	}
	if !metas[len(metas)-1].Final {
		t.Error("latest checkpoint should be Final after Resume completes")
	}
}

// TestResume_CrossAgent_Rejected verifies the safety net that prevents
// loading a session checkpointed by agent X into agent Y.
func TestResume_CrossAgent_Rejected(t *testing.T) {
	store := checkpoint.NewMapIterationStore()
	mock := &mockChatCompleter{
		responses: []*aimodel.ChatResponse{stopResponse("hi")},
	}
	original := New(agent.Config{ID: "agent-X"},
		WithChatCompleter(mock),
		WithIterationStore(store),
	)
	if _, err := original.Run(context.Background(), &schema.RunRequest{
		SessionID: "sess-cross",
		Messages:  []schema.Message{schema.NewUserMessage("hi")},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// A different agent ID tries to resume.
	other := New(agent.Config{ID: "agent-Y"},
		WithChatCompleter(&mockChatCompleter{}),
		WithIterationStore(store),
	)
	_, err := other.Resume(context.Background(), "sess-cross")
	if !errors.Is(err, checkpoint.ErrInvalidArgument) {
		t.Errorf("err = %v, want ErrInvalidArgument (or wrap)", err)
	}
}

func newEchoRegistry() tool.ToolRegistry {
	r := tool.NewRegistry()
	def := schema.ToolDef{
		Name:        "echo",
		Description: "echo",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{"v": map[string]any{"type": "string"}},
		},
	}
	handler := func(_ context.Context, _ string, _ string) (schema.ToolResult, error) {
		return schema.ToolResult{Content: []schema.ContentPart{{Type: "text", Text: "ok"}}}, nil
	}
	_ = r.Register(def, handler)
	return r
}
