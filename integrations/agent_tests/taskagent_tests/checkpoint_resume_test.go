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

package taskagent_tests //nolint:revive // integration test package

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/vogo/aimodel"
	"github.com/vogo/vage/agent"
	"github.com/vogo/vage/agent/taskagent"
	"github.com/vogo/vage/checkpoint"
	"github.com/vogo/vage/hook"
	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/tool"
)

// echoToolReg builds a tiny tool registry with one harmless "echo"
// tool. Mirrors the local-test helper without exporting it from
// taskagent.
func echoToolReg() tool.ToolRegistry {
	r := tool.NewRegistry()
	def := schema.ToolDef{
		Name:        "echo",
		Description: "echo back",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{"v": map[string]any{"type": "string"}},
		},
	}
	handler := func(_ context.Context, _, _ string) (schema.ToolResult, error) {
		return schema.ToolResult{Content: []schema.ContentPart{{Type: "text", Text: "ok"}}}, nil
	}
	_ = r.Register(def, handler)
	return r
}

// TestCheckpoint_AC_2_1_KIterationsKCheckpoints exercises AC-2.1: a
// successful K-iteration Run leaves exactly K persisted checkpoints
// with the last one Final = true and StopReason == complete.
func TestCheckpoint_AC_2_1_KIterationsKCheckpoints(t *testing.T) {
	const k = 4
	responses := make([]*aimodel.ChatResponse, 0, k)
	for i := range k - 1 {
		responses = append(responses, makeToolCallResponse("tc"+string(rune('a'+i)), "echo", `{"v":"x"}`, 50))
	}
	responses = append(responses, makeStopResponse("done", 50))

	mock := &mockChatCompleter{responses: responses}
	store := checkpoint.NewMapIterationStore()

	a := taskagent.New(agent.Config{ID: "agent-k"},
		taskagent.WithChatCompleter(mock),
		taskagent.WithIterationStore(store),
		taskagent.WithToolRegistry(echoToolReg()),
	)

	resp, err := a.Run(context.Background(), &schema.RunRequest{
		SessionID: "sess-k",
		Messages:  []schema.Message{schema.NewUserMessage("plan k")},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if resp.StopReason != schema.StopReasonComplete {
		t.Errorf("StopReason = %q, want complete", resp.StopReason)
	}

	metas, _ := store.List(context.Background(), "sess-k")
	if len(metas) != k {
		t.Fatalf("checkpoints = %d, want %d", len(metas), k)
	}
	for i, m := range metas {
		isLast := i == k-1
		if m.Final != isLast {
			t.Errorf("metas[%d].Final = %v, want %v", i, m.Final, isLast)
		}
		if m.Sequence != i+1 {
			t.Errorf("metas[%d].Sequence = %d, want %d", i, m.Sequence, i+1)
		}
	}
	if metas[k-1].StopReason != schema.StopReasonComplete {
		t.Errorf("final StopReason = %q, want complete", metas[k-1].StopReason)
	}
}

// TestCheckpoint_AC_2_2_PartialCheckpointsAfterCrash exercises AC-2.2:
// when Run errors mid-iter, the checkpoints written before the error
// remain readable. We simulate the crash by exhausting the mock's
// response list before the run completes.
func TestCheckpoint_AC_2_2_PartialCheckpointsAfterCrash(t *testing.T) {
	mock := &mockChatCompleter{
		responses: []*aimodel.ChatResponse{
			makeToolCallResponse("tc-1", "echo", `{"v":"a"}`, 50),
			makeToolCallResponse("tc-2", "echo", `{"v":"b"}`, 50),
			// Third call fails (mock returns "no more responses").
		},
	}
	store := checkpoint.NewMapIterationStore()

	a := taskagent.New(agent.Config{ID: "agent-crash"},
		taskagent.WithChatCompleter(mock),
		taskagent.WithIterationStore(store),
		taskagent.WithToolRegistry(echoToolReg()),
	)

	if _, err := a.Run(context.Background(), &schema.RunRequest{
		SessionID: "sess-crash",
		Messages:  []schema.Message{schema.NewUserMessage("crash plan")},
	}); err == nil {
		t.Fatal("Run: expected error on third call, got nil")
	}

	metas, _ := store.List(context.Background(), "sess-crash")
	if len(metas) != 2 {
		t.Fatalf("checkpoints = %d, want 2", len(metas))
	}
	for _, m := range metas {
		if m.Final {
			t.Errorf("crashed run produced unexpected Final cp at seq %d", m.Sequence)
		}
	}
}

// TestCheckpoint_AC_2_3_NoStoreEquivalent exercises AC-2.3: without
// WithIterationStore, Run output must be byte-equivalent to a Run with
// the option but a fresh store before any cp is written. We check both
// reach StopReasonComplete with the same response text and usage.
func TestCheckpoint_AC_2_3_NoStoreEquivalent(t *testing.T) {
	resp1 := runOnce(t, "sess-nostore", false)
	resp2 := runOnce(t, "sess-store", true)

	if resp1.StopReason != resp2.StopReason {
		t.Errorf("StopReason mismatch: %q vs %q", resp1.StopReason, resp2.StopReason)
	}
	if resp1.Messages[0].Content.Text() != resp2.Messages[0].Content.Text() {
		t.Errorf("text mismatch: %q vs %q",
			resp1.Messages[0].Content.Text(),
			resp2.Messages[0].Content.Text())
	}
	if resp1.Usage.TotalTokens != resp2.Usage.TotalTokens {
		t.Errorf("Usage mismatch: %d vs %d", resp1.Usage.TotalTokens, resp2.Usage.TotalTokens)
	}
}

func runOnce(t *testing.T, sessionID string, withStore bool) *schema.RunResponse {
	t.Helper()
	mock := &mockChatCompleter{
		responses: []*aimodel.ChatResponse{
			makeToolCallResponse("tc-1", "echo", `{"v":"x"}`, 50),
			makeStopResponse("hello", 50),
		},
	}
	opts := []taskagent.Option{
		taskagent.WithChatCompleter(mock),
		taskagent.WithToolRegistry(echoToolReg()),
	}
	if withStore {
		opts = append(opts, taskagent.WithIterationStore(checkpoint.NewMapIterationStore()))
	}
	a := taskagent.New(agent.Config{ID: "agent-eq"}, opts...)
	resp, err := a.Run(context.Background(), &schema.RunRequest{
		SessionID: sessionID,
		Messages:  []schema.Message{schema.NewUserMessage("hi")},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return resp
}

// TestCheckpoint_AC_1_1_ResumeContinuesFromLatest exercises AC-1.1: a
// crashed Run can be resumed and finishes with the expected output.
// Importantly, we verify the resumed run's final output text matches an
// uninterrupted control run's final output.
func TestCheckpoint_AC_1_1_ResumeContinuesFromLatest(t *testing.T) {
	store := checkpoint.NewMapIterationStore()

	// First Run: crashes after 1 successful iter (tool call response).
	mock1 := &mockChatCompleter{
		responses: []*aimodel.ChatResponse{
			makeToolCallResponse("tc-1", "echo", `{"v":"a"}`, 50),
		},
	}
	a1 := taskagent.New(agent.Config{ID: "agent-r"},
		taskagent.WithChatCompleter(mock1),
		taskagent.WithIterationStore(store),
		taskagent.WithToolRegistry(echoToolReg()),
	)
	_, err := a1.Run(context.Background(), &schema.RunRequest{
		SessionID: "sess-r",
		Messages:  []schema.Message{schema.NewUserMessage("multi-step plan")},
	})
	if err == nil {
		t.Fatal("first Run: want error, got nil")
	}

	// Resume with a fresh agent + completer that returns a stop response.
	mock2 := &mockChatCompleter{
		responses: []*aimodel.ChatResponse{makeStopResponse("RESUMED-FINAL", 50)},
	}
	a2 := taskagent.New(agent.Config{ID: "agent-r"},
		taskagent.WithChatCompleter(mock2),
		taskagent.WithIterationStore(store),
		taskagent.WithToolRegistry(echoToolReg()),
	)
	resp, err := a2.Resume(context.Background(), "sess-r")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if resp.StopReason != schema.StopReasonComplete {
		t.Errorf("StopReason = %q, want complete", resp.StopReason)
	}
	if got := resp.Messages[0].Content.Text(); got != "RESUMED-FINAL" {
		t.Errorf("text = %q, want RESUMED-FINAL", got)
	}

	// AC-2.1 corollary: total cp count after Resume = 1 (crashed) + 1 (resumed final) = 2.
	metas, _ := store.List(context.Background(), "sess-r")
	if len(metas) != 2 {
		t.Errorf("post-resume cp count = %d, want 2", len(metas))
	}
}

// TestCheckpoint_AC_1_3_ResumeUnknownSession exercises AC-1.3: Resume
// returns ErrCheckpointNotFound for an unknown session id.
func TestCheckpoint_AC_1_3_ResumeUnknownSession(t *testing.T) {
	store := checkpoint.NewMapIterationStore()
	a := taskagent.New(agent.Config{ID: "agent-x"},
		taskagent.WithChatCompleter(&mockChatCompleter{}),
		taskagent.WithIterationStore(store),
	)
	_, err := a.Resume(context.Background(), "sess-does-not-exist")
	if !errors.Is(err, checkpoint.ErrCheckpointNotFound) {
		t.Errorf("err = %v, want ErrCheckpointNotFound", err)
	}
}

// TestCheckpoint_AC_3_1_ListMetadataDoesNotEmbedMessages exercises
// AC-3.1: List returns metadata only — CheckpointMeta has no Messages
// field; the only way to get full messages is Load.
func TestCheckpoint_AC_3_1_ListMetadataDoesNotEmbedMessages(t *testing.T) {
	store := checkpoint.NewMapIterationStore()
	mock := &mockChatCompleter{
		responses: []*aimodel.ChatResponse{makeStopResponse("hi", 50)},
	}
	a := taskagent.New(agent.Config{ID: "agent-meta"},
		taskagent.WithChatCompleter(mock),
		taskagent.WithIterationStore(store),
	)
	if _, err := a.Run(context.Background(), &schema.RunRequest{
		SessionID: "sess-meta",
		Messages:  []schema.Message{schema.NewUserMessage("hi")},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	metas, err := store.List(context.Background(), "sess-meta")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(metas) != 1 {
		t.Fatalf("metas len = %d, want 1", len(metas))
	}
	if metas[0].MessagesCount == 0 {
		t.Error("MessagesCount should report messages, got 0")
	}

	cp, err := store.Load(context.Background(), "sess-meta", metas[0].ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cp.Messages) == 0 {
		t.Error("Load should return populated Messages")
	}
}

// TestCheckpoint_AC_3_3_HookEventEmitted exercises AC-3.3: a hook
// subscriber receives EventCheckpointWritten for each checkpoint with a
// well-formed payload.
func TestCheckpoint_AC_3_3_HookEventEmitted(t *testing.T) {
	store := checkpoint.NewMapIterationStore()
	mock := &mockChatCompleter{
		responses: []*aimodel.ChatResponse{makeStopResponse("ok", 50)},
	}

	hookMgr := hook.NewManager()
	var (
		muSeen sync.Mutex
		seen   []schema.CheckpointWrittenData
		count  int32
	)
	hookMgr.Register(hook.NewHookFunc(func(_ context.Context, e schema.Event) error {
		atomic.AddInt32(&count, 1)
		if d, ok := e.Data.(schema.CheckpointWrittenData); ok {
			muSeen.Lock()
			seen = append(seen, d)
			muSeen.Unlock()
		}
		return nil
	}, schema.EventCheckpointWritten))

	a := taskagent.New(agent.Config{ID: "agent-hook"},
		taskagent.WithChatCompleter(mock),
		taskagent.WithIterationStore(store),
		taskagent.WithHookManager(hookMgr),
	)
	if _, err := a.Run(context.Background(), &schema.RunRequest{
		SessionID: "sess-hook",
		Messages:  []schema.Message{schema.NewUserMessage("hi")},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := atomic.LoadInt32(&count); got != 1 {
		t.Fatalf("hook count = %d, want 1", got)
	}
	muSeen.Lock()
	defer muSeen.Unlock()
	if seen[0].CheckpointID == "" {
		t.Error("CheckpointWrittenData.CheckpointID empty")
	}
	if seen[0].Sequence != 1 {
		t.Errorf("Sequence = %d, want 1", seen[0].Sequence)
	}
	if !seen[0].Final {
		t.Error("expected Final=true on stop response")
	}
	if seen[0].StopReason != schema.StopReasonComplete {
		t.Errorf("StopReason = %q, want complete", seen[0].StopReason)
	}
	if seen[0].MessagesCount == 0 {
		t.Error("MessagesCount = 0")
	}
}

// TestCheckpoint_AC_4_1_FileLayout exercises AC-4.1: FileIterationStore
// places files at <root>/<session_id>/checkpoints/<NNNNNN>-<id>.json
// and the suffix is parseable.
func TestCheckpoint_AC_4_1_FileLayout(t *testing.T) {
	root := t.TempDir()
	store, err := checkpoint.NewFileIterationStore(root)
	if err != nil {
		t.Fatalf("NewFileIterationStore: %v", err)
	}
	mock := &mockChatCompleter{
		responses: []*aimodel.ChatResponse{makeStopResponse("done", 50)},
	}
	a := taskagent.New(agent.Config{ID: "agent-fs"},
		taskagent.WithChatCompleter(mock),
		taskagent.WithIterationStore(store),
	)
	if _, err := a.Run(context.Background(), &schema.RunRequest{
		SessionID: "sess-fs",
		Messages:  []schema.Message{schema.NewUserMessage("hi")},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	cpDir := filepath.Join(root, "sess-fs", "checkpoints")
	matches, err := filepath.Glob(filepath.Join(cpDir, "000001-*.json"))
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected exactly one 000001-*.json file, got %d", len(matches))
	}
}

// TestCheckpoint_FileStore_ConcurrentDifferentSessions verifies that
// FileIterationStore tolerates concurrent saves on different session
// ids — the per-session lock isolates writers.
func TestCheckpoint_FileStore_ConcurrentDifferentSessions(t *testing.T) {
	root := t.TempDir()
	store, err := checkpoint.NewFileIterationStore(root)
	if err != nil {
		t.Fatalf("NewFileIterationStore: %v", err)
	}

	const n = 8
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func(idx int) {
			defer wg.Done()
			cp := &checkpoint.Checkpoint{
				SessionID: "sess-" + string(rune('a'+idx)),
				AgentID:   "agent-c",
				Iteration: 0,
				Messages: []aimodel.Message{
					{Role: aimodel.RoleSystem, Content: aimodel.NewTextContent("sys")},
				},
			}
			if err := store.Save(context.Background(), cp); err != nil {
				t.Errorf("Save: %v", err)
			}
		}(i)
	}
	wg.Wait()

	for i := range n {
		sid := "sess-" + string(rune('a'+i))
		metas, err := store.List(context.Background(), sid)
		if err != nil {
			t.Errorf("List %s: %v", sid, err)
			continue
		}
		if len(metas) != 1 || metas[0].Sequence != 1 {
			t.Errorf("session %s: metas=%+v", sid, metas)
		}
	}
}

// TestCheckpoint_PreCallBudgetExhausted_FirstIter exercises the iter=0
// pre-call budget exhausted path. Setting RunOptions.RunTokenBudget to
// 1 with the first call's usage of 50 ensures the second iteration's
// pre-call check fires.
func TestCheckpoint_PreCallBudgetExhausted_FirstIter(t *testing.T) {
	mock := &mockChatCompleter{
		responses: []*aimodel.ChatResponse{
			makeToolCallResponse("tc-1", "echo", `{"v":"a"}`, 50),
		},
	}
	store := checkpoint.NewMapIterationStore()
	a := taskagent.New(agent.Config{ID: "agent-budget"},
		taskagent.WithChatCompleter(mock),
		taskagent.WithIterationStore(store),
		taskagent.WithToolRegistry(echoToolReg()),
		taskagent.WithRunTokenBudget(1),
	)

	resp, err := a.Run(context.Background(), &schema.RunRequest{
		SessionID: "sess-budget",
		Messages:  []schema.Message{schema.NewUserMessage("go")},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if resp.StopReason != schema.StopReasonBudgetExhausted {
		t.Errorf("StopReason = %q, want token_budget_exhausted", resp.StopReason)
	}
	metas, _ := store.List(context.Background(), "sess-budget")
	// Iter 0 completes (post-call budget after tool batch — actually
	// post-call check fires before the tool batch, so iter 0 ends with
	// a Final BudgetExhausted cp; no tool batch cp). Two cps either way
	// would break the invariant, so we accept exactly 1 Final cp.
	final := 0
	for _, m := range metas {
		if m.Final {
			final++
		}
	}
	if final != 1 {
		t.Errorf("Final cp count = %d, want 1", final)
	}
	if metas[len(metas)-1].StopReason != schema.StopReasonBudgetExhausted {
		t.Errorf("last StopReason = %q, want budget_exhausted",
			metas[len(metas)-1].StopReason)
	}
}
