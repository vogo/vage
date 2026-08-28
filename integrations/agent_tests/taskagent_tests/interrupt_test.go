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
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/vogo/vage/agent"
	"github.com/vogo/vage/agent/taskagent"
	"github.com/vogo/vage/hook"
	"github.com/vogo/vage/interrupt"
	"github.com/vogo/vage/largemodel"
	"github.com/vogo/vage/memory"
	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/tool"
)

// makeMultiToolCallResponse builds a scripted response requesting several
// tool calls in one assistant turn.
func makeMultiToolCallResponse(totalTokens int, calls ...schema.ToolCall) *largemodel.Response {
	return &largemodel.Response{
		Message:      schema.NewAssistantTurn(testProtocol, "", "", calls),
		FinishReason: largemodel.FinishReasonToolCalls,
		Usage: schema.Usage{
			PromptTokens:     totalTokens / 2,
			CompletionTokens: totalTokens - totalTokens/2,
			TotalTokens:      totalTokens,
		},
	}
}

// askUserReg builds a registry with one "ask_user"-shaped tool whose handler
// increments counter every time it actually runs. An InterruptPolicy that
// flags "ask_user" should keep counter at 0 forever — the whole point of the
// interrupt choke point is that this handler never executes.
func askUserReg(counter *atomic.Int32) tool.ToolRegistry {
	r := tool.NewRegistry()
	_ = r.Register(
		schema.ToolDef{Name: "ask_user", Description: "ask a human"},
		func(_ context.Context, _, _ string) (schema.ToolResult, error) {
			counter.Add(1)
			return schema.TextResult("", "SHOULD NEVER RUN"), nil
		},
	)
	return r
}

// eventCollector wires a hook.Manager that records every event's type in
// arrival order, for asserting ordering invariants.
func eventCollector() (*hook.Manager, func() []string) {
	var (
		mu    sync.Mutex
		types []string
	)
	mgr := hook.NewManager()
	mgr.Register(hook.NewHookFunc(func(_ context.Context, e schema.Event) error {
		mu.Lock()
		defer mu.Unlock()
		types = append(types, e.Type)
		return nil
	}))
	return mgr, func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := make([]string, len(types))
		copy(out, types)
		return out
	}
}

func sessionEntryCount(t *testing.T, session memory.Memory) int {
	t.Helper()
	entries, err := session.List(context.Background(), "msg:")
	if err != nil {
		t.Fatalf("session.List: %v", err)
	}
	return len(entries)
}

// TestInterrupt_Suspend_NoHandlerRuns_PersistsAndReportsPending exercises
// the suspend half of the acceptance path: a policy-flagged tool call stops
// the batch before any handler runs, persists a resumable record, and
// reports StopReasonInterrupted with the pending call — without replaying
// the model turn that produced it (a single LLM call).
func TestInterrupt_Suspend_NoHandlerRuns_PersistsAndReportsPending(t *testing.T) {
	var handlerRuns atomic.Int32
	mock := newMock(makeToolCallResponse("tc-1", "ask_user", `{"question":"proceed?"}`, 30))
	store := interrupt.NewMapStore()

	session := memory.NewSessionMemory("agent-i", "sess-i")
	memMgr := memory.NewManager(memory.WithSession(session))
	hookMgr, events := eventCollector()

	a := taskagent.New(
		agent.Config{ID: "agent-i"},
		taskagent.WithCaller(mock),
		taskagent.WithToolRegistry(askUserReg(&handlerRuns)),
		taskagent.WithInterruptStore(store),
		taskagent.WithInterruptToolNames("ask_user"),
		taskagent.WithMemory(memMgr),
		taskagent.WithHookManager(hookMgr),
	)

	resp, err := a.Run(context.Background(), &schema.RunRequest{
		SessionID: "sess-i",
		Messages:  []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "please ask")},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if resp.StopReason != schema.StopReasonInterrupted {
		t.Fatalf("StopReason = %q, want interrupted", resp.StopReason)
	}
	if resp.Interrupt == nil || resp.Interrupt.InterruptID == "" {
		t.Fatalf("Interrupt descriptor missing: %+v", resp.Interrupt)
	}
	if len(resp.Interrupt.Pending) != 1 || resp.Interrupt.Pending[0].ID != "tc-1" {
		t.Fatalf("Pending = %+v, want [tc-1]", resp.Interrupt.Pending)
	}
	if handlerRuns.Load() != 0 {
		t.Errorf("ask_user handler ran %d times, want 0", handlerRuns.Load())
	}
	if mock.Calls() != 1 {
		t.Errorf("LLM calls = %d, want 1 (no replay of the suspending turn)", mock.Calls())
	}

	for _, forbidden := range []string{schema.EventToolCallStart, schema.EventToolCallEnd} {
		for _, e := range events() {
			if e == forbidden {
				t.Errorf("saw forbidden event %q around a suspended tool call", forbidden)
			}
		}
	}
	seen := events()
	if !containsStr(seen, schema.EventInterruptCreated) {
		t.Errorf("events = %v, want interrupt_created", seen)
	}
	if !containsStr(seen, schema.EventAgentEnd) {
		t.Errorf("events = %v, want agent_end (interrupted is a call end, not a Run failure)", seen)
	}

	// The request message is promoted normally; the still-open
	// assistant/tool-call pair is withheld until Resume closes it.
	if got := sessionEntryCount(t, session); got != 1 {
		t.Errorf("session entries after suspend = %d, want 1 (request only)", got)
	}

	rec, err := store.Get(context.Background(), resp.Interrupt.InterruptID)
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if rec.Status != interrupt.StatusPending {
		t.Errorf("record status = %q, want pending", rec.Status)
	}
}

// TestInterrupt_Resume_ClosesLoopAndWritesSessionOnce exercises the resume
// half: submitting the decision injects it as the tool result immediately
// following the original assistant tool-call message, usage/iteration stay
// continuous (no re-run of the suspended turn), and session memory ends up
// with exactly one write of the complete conversation — never twice, never
// with a dangling tool call.
func TestInterrupt_Resume_ClosesLoopAndWritesSessionOnce(t *testing.T) {
	var handlerRuns atomic.Int32
	mock := newMock(
		makeToolCallResponse("tc-1", "ask_user", `{"question":"proceed?"}`, 30),
		makeStopResponse("done", 20),
	)
	store := interrupt.NewMapStore()
	session := memory.NewSessionMemory("agent-i", "sess-i2")
	memMgr := memory.NewManager(memory.WithSession(session))
	hookMgr, events := eventCollector()

	a := taskagent.New(
		agent.Config{ID: "agent-i"},
		taskagent.WithCaller(mock),
		taskagent.WithToolRegistry(askUserReg(&handlerRuns)),
		taskagent.WithInterruptStore(store),
		taskagent.WithInterruptToolNames("ask_user"),
		taskagent.WithMemory(memMgr),
		taskagent.WithHookManager(hookMgr),
	)

	first, err := a.Run(context.Background(), &schema.RunRequest{
		SessionID: "sess-i2",
		Messages:  []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "please ask")},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	resp, err := a.ResumeInterrupt(context.Background(), schema.ResumeInterruptRequest{
		InterruptID: first.Interrupt.InterruptID,
		Decisions:   []schema.InterruptDecision{{ToolCallID: "tc-1", Content: "approved"}},
	})
	if err != nil {
		t.Fatalf("ResumeInterrupt: %v", err)
	}

	if resp.StopReason != schema.StopReasonComplete {
		t.Fatalf("StopReason = %q, want complete", resp.StopReason)
	}
	if resp.Messages[0].Text() != "done" {
		t.Errorf("final text = %q, want done", resp.Messages[0].Text())
	}
	if handlerRuns.Load() != 0 {
		t.Errorf("ask_user handler ran %d times, want 0 (decision replaces execution)", handlerRuns.Load())
	}
	if mock.Calls() != 2 {
		t.Fatalf("LLM calls = %d, want 2", mock.Calls())
	}
	if resp.Usage.TotalTokens != 50 {
		t.Errorf("TotalTokens = %d, want 50 (30 + 20, continuous across resume)", resp.Usage.TotalTokens)
	}

	secondReq := mock.Requests()[1]
	n := len(secondReq.Messages)
	if n < 2 {
		t.Fatalf("resumed request has %d messages, want >= 2", n)
	}
	toolMsg := secondReq.Messages[n-1]
	assistantMsg := secondReq.Messages[n-2]
	if assistantMsg.Role() != schema.RoleAssistant || len(assistantMsg.ToolCalls()) != 1 {
		t.Fatalf("message before tool result = %+v, want the original assistant tool-call turn", assistantMsg)
	}
	if toolMsg.Role() != schema.RoleTool || toolMsg.ToolCallID() != "tc-1" {
		t.Fatalf("last message = role %q id %q, want tool/tc-1", toolMsg.Role(), toolMsg.ToolCallID())
	}
	if toolMsg.Text() != "approved" {
		t.Errorf("tool result text = %q, want approved", toolMsg.Text())
	}

	if got := sessionEntryCount(t, session); got != 2 {
		t.Errorf("session entries after resume = %d, want 2 (request + final answer, written exactly once)", got)
	}

	seen := events()
	idxCreated := indexOf(seen, schema.EventInterruptCreated)
	idxDecision := indexOf(seen, schema.EventInterruptDecisionStored)
	idxResumed := indexOf(seen, schema.EventInterruptResumed)
	idxLastEnd := lastIndexOf(seen, schema.EventAgentEnd)
	if idxCreated < 0 || idxDecision < 0 || idxResumed < 0 || idxLastEnd < 0 {
		t.Fatalf("missing expected event(s) in %v", seen)
	}
	inOrder := idxCreated < idxDecision && idxDecision < idxResumed && idxResumed < idxLastEnd
	if !inOrder {
		t.Errorf("event order = %v, want created < decision_stored < resumed < agent_end", seen)
	}
}

// TestInterrupt_MultiToolBatch_SiblingWaitsThenRunsOnce exercises a batch
// with both a pending call and an ordinary sibling: the sibling must not
// execute until every pending call has a decision, then executes exactly
// once, with all results reassembled in the model's original order.
func TestInterrupt_MultiToolBatch_SiblingWaitsThenRunsOnce(t *testing.T) {
	var (
		askRuns  atomic.Int32
		echoRuns atomic.Int32
	)
	mock := newMock(
		makeMultiToolCallResponse(
			30,
			schema.ToolCall{ID: "tc-1", Name: "ask_user", Arguments: `{"question":"proceed?"}`},
			schema.ToolCall{ID: "tc-2", Name: "echo", Arguments: `{"v":"x"}`},
		),
		makeStopResponse("done", 20),
	)

	reg := tool.NewRegistry()
	_ = reg.Register(schema.ToolDef{Name: "ask_user"}, func(_ context.Context, _, _ string) (schema.ToolResult, error) {
		askRuns.Add(1)
		return schema.TextResult("", "never"), nil
	})
	_ = reg.Register(schema.ToolDef{Name: "echo"}, func(_ context.Context, _, _ string) (schema.ToolResult, error) {
		echoRuns.Add(1)
		return schema.TextResult("", "echoed"), nil
	})

	store := interrupt.NewMapStore()
	a := taskagent.New(
		agent.Config{ID: "agent-multi"},
		taskagent.WithCaller(mock),
		taskagent.WithToolRegistry(reg),
		taskagent.WithInterruptStore(store),
		taskagent.WithInterruptToolNames("ask_user"),
	)

	first, err := a.Run(context.Background(), &schema.RunRequest{
		SessionID: "sess-multi",
		Messages:  []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "go")},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(first.Interrupt.Pending) != 1 || first.Interrupt.Pending[0].ID != "tc-1" {
		t.Fatalf("Pending = %+v, want only tc-1", first.Interrupt.Pending)
	}
	if echoRuns.Load() != 0 {
		t.Fatalf("sibling ran before decisions were complete: echoRuns=%d", echoRuns.Load())
	}

	resp, err := a.ResumeInterrupt(context.Background(), schema.ResumeInterruptRequest{
		InterruptID: first.Interrupt.InterruptID,
		Decisions:   []schema.InterruptDecision{{ToolCallID: "tc-1", Content: "approved"}},
	})
	if err != nil {
		t.Fatalf("ResumeInterrupt: %v", err)
	}
	if resp.StopReason != schema.StopReasonComplete {
		t.Fatalf("StopReason = %q, want complete", resp.StopReason)
	}
	if askRuns.Load() != 0 {
		t.Errorf("ask_user handler ran %d times, want 0", askRuns.Load())
	}
	if echoRuns.Load() != 1 {
		t.Errorf("echo handler ran %d times, want exactly 1", echoRuns.Load())
	}

	secondReq := mock.Requests()[1]
	n := len(secondReq.Messages)
	if n < 3 {
		t.Fatalf("resumed request has %d messages, want >= 3", n)
	}
	// Order must follow the model's original ToolCalls order: tc-1's
	// decision result, then tc-2's freshly-executed sibling result.
	tcMsg, echoMsg := secondReq.Messages[n-2], secondReq.Messages[n-1]
	if tcMsg.ToolCallID() != "tc-1" || tcMsg.Text() != "approved" {
		t.Errorf("messages[n-2] = id %q text %q, want tc-1/approved", tcMsg.ToolCallID(), tcMsg.Text())
	}
	if echoMsg.ToolCallID() != "tc-2" || echoMsg.Text() != "echoed" {
		t.Errorf("messages[n-1] = id %q text %q, want tc-2/echoed", echoMsg.ToolCallID(), echoMsg.Text())
	}
}

// TestInterrupt_StillPending_ReturnsStatusWithoutStartingAnything exercises
// a two-pending-call batch where only one decision is submitted: the batch
// must not start any tool or model call, and must report the same
// interrupt_id with the remaining pending set.
func TestInterrupt_StillPending_ReturnsStatusWithoutStartingAnything(t *testing.T) {
	mock := newMock(
		makeMultiToolCallResponse(
			30,
			schema.ToolCall{ID: "tc-1", Name: "ask_user", Arguments: `{"question":"a?"}`},
			schema.ToolCall{ID: "tc-2", Name: "ask_user", Arguments: `{"question":"b?"}`},
		),
		makeStopResponse("done", 20),
	)
	store := interrupt.NewMapStore()
	var handlerRuns atomic.Int32
	a := taskagent.New(
		agent.Config{ID: "agent-partial"},
		taskagent.WithCaller(mock),
		taskagent.WithToolRegistry(askUserReg(&handlerRuns)),
		taskagent.WithInterruptStore(store),
		taskagent.WithInterruptToolNames("ask_user"),
	)

	first, err := a.Run(context.Background(), &schema.RunRequest{
		SessionID: "sess-partial",
		Messages:  []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "go")},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	resp, err := a.ResumeInterrupt(context.Background(), schema.ResumeInterruptRequest{
		InterruptID: first.Interrupt.InterruptID,
		Decisions:   []schema.InterruptDecision{{ToolCallID: "tc-1", Content: "a-answer"}},
	})
	if err != nil {
		t.Fatalf("ResumeInterrupt (partial): %v", err)
	}
	if resp.StopReason != schema.StopReasonInterrupted {
		t.Fatalf("StopReason = %q, want interrupted (still pending)", resp.StopReason)
	}
	if resp.Interrupt.InterruptID != first.Interrupt.InterruptID {
		t.Errorf("interrupt_id changed: %q -> %q", first.Interrupt.InterruptID, resp.Interrupt.InterruptID)
	}
	if len(resp.Interrupt.Pending) != 1 || resp.Interrupt.Pending[0].ID != "tc-2" {
		t.Errorf("Pending = %+v, want only tc-2", resp.Interrupt.Pending)
	}
	if mock.Calls() != 1 {
		t.Errorf("LLM calls = %d, want 1 (no model call while still pending)", mock.Calls())
	}
	if handlerRuns.Load() != 0 {
		t.Errorf("handler ran %d times, want 0", handlerRuns.Load())
	}
}

func TestInterrupt_ZeroDecisions_ProbesPendingAndRetriesReady(t *testing.T) {
	var handlerRuns atomic.Int32
	mock := newMock(
		makeToolCallResponse("tc-1", "ask_user", `{}`, 30),
		makeStopResponse("done", 20),
	)
	store := interrupt.NewMapStore()
	a := taskagent.New(
		agent.Config{ID: "agent-zero-decisions"},
		taskagent.WithCaller(mock),
		taskagent.WithToolRegistry(askUserReg(&handlerRuns)),
		taskagent.WithInterruptStore(store),
		taskagent.WithInterruptToolNames("ask_user"),
	)

	first, err := a.Run(context.Background(), &schema.RunRequest{
		SessionID: "sess-zero-decisions",
		Messages:  []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "go")},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	probe, err := a.ResumeInterrupt(context.Background(), schema.ResumeInterruptRequest{
		InterruptID: first.Interrupt.InterruptID,
	})
	if err != nil {
		t.Fatalf("pending probe: %v", err)
	}
	if probe.StopReason != schema.StopReasonInterrupted || mock.Calls() != 1 || handlerRuns.Load() != 0 {
		t.Fatalf("pending probe started work: response=%+v calls=%d handlers=%d", probe, mock.Calls(), handlerRuns.Load())
	}

	if _, _, err := store.SubmitDecisions(context.Background(), first.Interrupt.InterruptID, []interrupt.Decision{{
		ToolCallID: "tc-1",
		Content:    "approved",
	}}); err != nil {
		t.Fatalf("prepare Ready record: %v", err)
	}

	resumed, err := a.ResumeInterrupt(context.Background(), schema.ResumeInterruptRequest{
		InterruptID: first.Interrupt.InterruptID,
	})
	if err != nil {
		t.Fatalf("ready retry: %v", err)
	}
	if resumed.StopReason != schema.StopReasonComplete || resumed.Messages[0].Text() != "done" {
		t.Fatalf("ready retry response = %+v, want completed done", resumed)
	}
	if mock.Calls() != 2 || handlerRuns.Load() != 0 {
		t.Fatalf("ready retry calls=%d handlers=%d, want 2/0", mock.Calls(), handlerRuns.Load())
	}
}

func TestInterrupt_ResumePreservesRunTokenBudget(t *testing.T) {
	var (
		askRuns     atomic.Int32
		expenseRuns atomic.Int32
	)
	mock := newMock(
		makeToolCallResponse("tc-ask", "ask_user", `{}`, 60),
		makeToolCallResponse("tc-expense", "expensive", `{}`, 40),
		makeStopResponse("should not run", 10),
	)
	reg := tool.NewRegistry()
	_ = reg.Register(schema.ToolDef{Name: "ask_user"}, func(_ context.Context, _, _ string) (schema.ToolResult, error) {
		askRuns.Add(1)
		return schema.TextResult("", "never"), nil
	})
	_ = reg.Register(schema.ToolDef{Name: "expensive"}, func(_ context.Context, _, _ string) (schema.ToolResult, error) {
		expenseRuns.Add(1)
		return schema.TextResult("", "spent"), nil
	})
	store := interrupt.NewMapStore()
	a := taskagent.New(
		agent.Config{ID: "agent-resume-budget"},
		taskagent.WithCaller(mock),
		taskagent.WithToolRegistry(reg),
		taskagent.WithRunTokenBudget(100),
		taskagent.WithMaxIterations(5),
		taskagent.WithInterruptStore(store),
		taskagent.WithInterruptToolNames("ask_user"),
	)

	first, err := a.Run(context.Background(), &schema.RunRequest{
		SessionID: "sess-resume-budget",
		Messages:  []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "go")},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	rec, err := store.Get(context.Background(), first.Interrupt.InterruptID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if rec.TokensConsumed != 60 {
		t.Fatalf("TokensConsumed = %d, want 60", rec.TokensConsumed)
	}

	resumed, err := a.ResumeInterrupt(context.Background(), schema.ResumeInterruptRequest{
		InterruptID: first.Interrupt.InterruptID,
		Decisions:   []schema.InterruptDecision{{ToolCallID: "tc-ask", Content: "approved"}},
	})
	if err != nil {
		t.Fatalf("ResumeInterrupt: %v", err)
	}
	if resumed.StopReason != schema.StopReasonBudgetExhausted {
		t.Fatalf("StopReason = %q, want %q", resumed.StopReason, schema.StopReasonBudgetExhausted)
	}
	if resumed.Usage.TotalTokens != 100 || mock.Calls() != 2 {
		t.Fatalf("usage/calls = %d/%d, want 100/2", resumed.Usage.TotalTokens, mock.Calls())
	}
	if askRuns.Load() != 0 || expenseRuns.Load() != 0 {
		t.Fatalf("handlers ran ask=%d expensive=%d, want 0/0", askRuns.Load(), expenseRuns.Load())
	}
}

func TestInterrupt_ResumeWithoutCallerHasNoSideEffects(t *testing.T) {
	var siblingRuns atomic.Int32
	store := interrupt.NewMapStore()
	seed := taskagent.New(
		agent.Config{ID: "agent-no-caller"},
		taskagent.WithCaller(newMock(makeMultiToolCallResponse(
			30,
			schema.ToolCall{ID: "tc-ask", Name: "ask_user", Arguments: `{}`},
			schema.ToolCall{ID: "tc-sibling", Name: "sibling", Arguments: `{}`},
		))),
		taskagent.WithToolRegistry(askUserReg(new(atomic.Int32))),
		taskagent.WithInterruptStore(store),
		taskagent.WithInterruptToolNames("ask_user"),
	)
	first, err := seed.Run(context.Background(), &schema.RunRequest{
		SessionID: "sess-no-caller",
		Messages:  []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "go")},
	})
	if err != nil {
		t.Fatalf("seed Run: %v", err)
	}

	reg := tool.NewRegistry()
	_ = reg.Register(schema.ToolDef{Name: "ask_user"}, func(_ context.Context, _, _ string) (schema.ToolResult, error) {
		return schema.TextResult("", "never"), nil
	})
	_ = reg.Register(schema.ToolDef{Name: "sibling"}, func(_ context.Context, _, _ string) (schema.ToolResult, error) {
		siblingRuns.Add(1)
		return schema.TextResult("", "side effect"), nil
	})
	broken := taskagent.New(
		agent.Config{ID: "agent-no-caller"},
		taskagent.WithToolRegistry(reg),
		taskagent.WithInterruptStore(store),
		taskagent.WithInterruptToolNames("ask_user"),
	)

	_, err = broken.ResumeInterrupt(context.Background(), schema.ResumeInterruptRequest{
		InterruptID: first.Interrupt.InterruptID,
		Decisions:   []schema.InterruptDecision{{ToolCallID: "tc-ask", Content: "approved"}},
	})
	if err == nil || !strings.Contains(err.Error(), "model caller is required") {
		t.Fatalf("ResumeInterrupt error = %v, want missing caller", err)
	}
	if siblingRuns.Load() != 0 {
		t.Fatalf("sibling ran %d times before caller preflight", siblingRuns.Load())
	}
	rec, err := store.Get(context.Background(), first.Interrupt.InterruptID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if rec.Status != interrupt.StatusPending || len(rec.Decisions) != 0 || rec.Revision != 1 {
		t.Fatalf("record mutated before caller preflight: %+v", rec)
	}
}

// TestInterrupt_ResumeInterrupt_FailureModes exercises the reject-before-
// consuming-anything error paths.
func TestInterrupt_ResumeInterrupt_FailureModes(t *testing.T) {
	store := interrupt.NewMapStore()
	var handlerRuns atomic.Int32

	newAgent := func(id string, reg tool.ToolRegistry) *taskagent.Agent {
		return taskagent.New(
			agent.Config{ID: id},
			taskagent.WithCaller(newMock(makeStopResponse("done", 20))),
			taskagent.WithToolRegistry(reg),
			taskagent.WithInterruptStore(store),
			taskagent.WithInterruptToolNames("ask_user"),
		)
	}

	seed := func(t *testing.T, sessionID string, calls ...schema.ToolCall) *schema.RunResponse {
		t.Helper()
		mock := newMock(makeMultiToolCallResponse(30, calls...))
		a := taskagent.New(
			agent.Config{ID: "agent-fail"},
			taskagent.WithCaller(mock),
			taskagent.WithToolRegistry(askUserReg(&handlerRuns)),
			taskagent.WithInterruptStore(store),
			taskagent.WithInterruptToolNames("ask_user"),
		)
		resp, err := a.Run(context.Background(), &schema.RunRequest{
			SessionID: sessionID,
			Messages:  []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "go")},
		})
		if err != nil {
			t.Fatalf("seed Run: %v", err)
		}
		return resp
	}

	t.Run("unknown_id", func(t *testing.T) {
		a := newAgent("agent-fail", askUserReg(&handlerRuns))
		_, err := a.ResumeInterrupt(context.Background(), schema.ResumeInterruptRequest{InterruptID: "does-not-exist"})
		if !errors.Is(err, interrupt.ErrNotFound) {
			t.Errorf("err = %v, want ErrNotFound", err)
		}
	})

	t.Run("unknown_tool_call_id", func(t *testing.T) {
		first := seed(t, "sess-fail-1", schema.ToolCall{ID: "tc-1", Name: "ask_user", Arguments: `{}`})
		a := newAgent("agent-fail", askUserReg(&handlerRuns))
		_, err := a.ResumeInterrupt(context.Background(), schema.ResumeInterruptRequest{
			InterruptID: first.Interrupt.InterruptID,
			Decisions:   []schema.InterruptDecision{{ToolCallID: "not-a-call", Content: "x"}},
		})
		if !errors.Is(err, interrupt.ErrUnknownToolCall) {
			t.Errorf("err = %v, want ErrUnknownToolCall", err)
		}
	})

	t.Run("decision_conflict", func(t *testing.T) {
		// Two pending calls: deciding only tc-1 keeps the record Pending
		// (tc-2 still undecided), so ResumeInterrupt returns the status
		// response instead of racing ahead to a real resume — letting this
		// test isolate SubmitDecisions' conflict rejection.
		first := seed(
			t, "sess-fail-2",
			schema.ToolCall{ID: "tc-1", Name: "ask_user", Arguments: `{}`},
			schema.ToolCall{ID: "tc-2", Name: "ask_user", Arguments: `{}`},
		)
		a := newAgent("agent-fail", askUserReg(&handlerRuns))
		if _, err := a.ResumeInterrupt(context.Background(), schema.ResumeInterruptRequest{
			InterruptID: first.Interrupt.InterruptID,
			Decisions:   []schema.InterruptDecision{{ToolCallID: "tc-1", Content: "first"}},
		}); err != nil {
			t.Fatalf("first decision: %v", err)
		}
		_, err := a.ResumeInterrupt(context.Background(), schema.ResumeInterruptRequest{
			InterruptID: first.Interrupt.InterruptID,
			Decisions:   []schema.InterruptDecision{{ToolCallID: "tc-1", Content: "second"}},
		})
		if !errors.Is(err, interrupt.ErrDecisionConflict) {
			t.Errorf("err = %v, want ErrDecisionConflict", err)
		}
	})

	t.Run("agent_mismatch", func(t *testing.T) {
		first := seed(t, "sess-fail-3", schema.ToolCall{ID: "tc-1", Name: "ask_user", Arguments: `{}`})
		other := newAgent("some-other-agent", askUserReg(&handlerRuns))
		_, err := other.ResumeInterrupt(context.Background(), schema.ResumeInterruptRequest{
			InterruptID: first.Interrupt.InterruptID,
			Decisions:   []schema.InterruptDecision{{ToolCallID: "tc-1", Content: "x"}},
		})
		if !errors.Is(err, taskagent.ErrInterruptAgentMismatch) {
			t.Errorf("err = %v, want ErrInterruptAgentMismatch", err)
		}
	})

	t.Run("incompatible_sibling_tool_releases_lease", func(t *testing.T) {
		first := seed(
			t, "sess-fail-4",
			schema.ToolCall{ID: "tc-1", Name: "ask_user", Arguments: `{}`},
			schema.ToolCall{ID: "tc-2", Name: "no_longer_registered", Arguments: `{}`},
		)
		// This agent shares the store and ID, but its registry lacks the
		// sibling tool the batch needs.
		a := newAgent("agent-fail", askUserReg(&handlerRuns))
		_, err := a.ResumeInterrupt(context.Background(), schema.ResumeInterruptRequest{
			InterruptID: first.Interrupt.InterruptID,
			Decisions:   []schema.InterruptDecision{{ToolCallID: "tc-1", Content: "x"}},
		})
		if !errors.Is(err, taskagent.ErrIncompatibleInterruptTools) {
			t.Fatalf("err = %v, want ErrIncompatibleInterruptTools", err)
		}

		// The lease must have been released so a compatible retry can
		// still pick the record up without re-asking the human.
		rec, gerr := store.Get(context.Background(), first.Interrupt.InterruptID)
		if gerr != nil {
			t.Fatalf("store.Get: %v", gerr)
		}
		if rec.Status != interrupt.StatusReady {
			t.Errorf("record status after failed resume = %q, want ready (lease released)", rec.Status)
		}
	})
}

// TestInterrupt_RunValuesNotInheritedAcrossResume exercises the boundary
// documented for Run values: ResumeInterrupt starts a brand-new, empty
// store, even for a sibling tool call executed in the very same batch that
// suspended.
func TestInterrupt_RunValuesNotInheritedAcrossResume(t *testing.T) {
	mock := newMock(
		makeMultiToolCallResponse(
			30,
			schema.ToolCall{ID: "tc-1", Name: "ask_user", Arguments: `{}`},
			schema.ToolCall{ID: "tc-2", Name: "check_rv", Arguments: `{}`},
		),
		makeStopResponse("done", 20),
	)

	reg := tool.NewRegistry()
	var handlerRuns atomic.Int32
	_ = reg.Register(schema.ToolDef{Name: "ask_user"}, func(_ context.Context, _, _ string) (schema.ToolResult, error) {
		handlerRuns.Add(1)
		return schema.TextResult("", "never"), nil
	})
	_ = reg.Register(schema.ToolDef{Name: "check_rv"}, func(ctx context.Context, _, _ string) (schema.ToolResult, error) {
		if _, ok := schema.GetRunValue(ctx, "flag"); ok {
			return schema.TextResult("", "found"), nil
		}
		return schema.TextResult("", "missing"), nil
	})

	store := interrupt.NewMapStore()
	mw := agent.MiddlewareFunc(func(next agent.RunFunc) agent.RunFunc {
		return func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
			schema.SetRunValue(ctx, "flag", "set-before-suspend")
			return next(ctx, req)
		}
	})

	a := taskagent.New(
		agent.Config{ID: "agent-rv"},
		taskagent.WithCaller(mock),
		taskagent.WithToolRegistry(reg),
		taskagent.WithInterruptStore(store),
		taskagent.WithInterruptToolNames("ask_user"),
		taskagent.WithMiddleware(mw),
	)

	first, err := a.Run(context.Background(), &schema.RunRequest{
		SessionID: "sess-rv",
		Messages:  []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "go")},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	resp, err := a.ResumeInterrupt(context.Background(), schema.ResumeInterruptRequest{
		InterruptID: first.Interrupt.InterruptID,
		Decisions:   []schema.InterruptDecision{{ToolCallID: "tc-1", Content: "x"}},
	})
	if err != nil {
		t.Fatalf("ResumeInterrupt: %v", err)
	}
	if resp.StopReason != schema.StopReasonComplete {
		t.Fatalf("StopReason = %q, want complete", resp.StopReason)
	}

	secondReq := mock.Requests()[1]
	n := len(secondReq.Messages)
	rvMsg := secondReq.Messages[n-1]
	if rvMsg.ToolCallID() != "tc-2" {
		t.Fatalf("messages[n-1] id = %q, want tc-2", rvMsg.ToolCallID())
	}
	if rvMsg.Text() != "missing" {
		t.Errorf("check_rv result = %q, want missing (Run values must not survive Resume)", rvMsg.Text())
	}
}

// TestInterrupt_CrossProcess_FileStore is the headline acceptance scenario:
// process A suspends via a file-backed store and exits (its handle is simply
// dropped — FileStore has no Close, every mutation is already durable);
// process B opens a brand-new FileStore over the same directory, submits the
// human decision by tool_call_id, and resumes to completion.
func TestInterrupt_CrossProcess_FileStore(t *testing.T) {
	root := t.TempDir()
	var handlerRuns atomic.Int32

	// --- Process A: suspend and "exit". ---
	storeA, err := interrupt.NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore A: %v", err)
	}

	mockA := newMock(makeToolCallResponse("tc-1", "ask_user", `{"question":"proceed?"}`, 30))
	agentA := taskagent.New(
		agent.Config{ID: "agent-xproc"},
		taskagent.WithCaller(mockA),
		taskagent.WithToolRegistry(askUserReg(&handlerRuns)),
		taskagent.WithInterruptStore(storeA),
		taskagent.WithInterruptToolNames("ask_user"),
	)

	first, err := agentA.Run(context.Background(), &schema.RunRequest{
		SessionID: "sess-xproc",
		Messages:  []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "please ask")},
	})
	if err != nil {
		t.Fatalf("process A Run: %v", err)
	}
	if first.StopReason != schema.StopReasonInterrupted {
		t.Fatalf("process A StopReason = %q, want interrupted", first.StopReason)
	}
	interruptID := first.Interrupt.InterruptID
	// storeA / agentA go out of use here — nothing more is called on
	// them until the final Completed assertion, standing in for "process
	// A exited".

	// --- Process B: fresh store handle, fresh Agent, resumes. ---
	storeB, err := interrupt.NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore B: %v", err)
	}

	mockB := newMock(makeStopResponse("done", 20))
	agentB := taskagent.New(
		agent.Config{ID: "agent-xproc"}, // same identity as process A's agent
		taskagent.WithCaller(mockB),
		taskagent.WithToolRegistry(askUserReg(&handlerRuns)),
		taskagent.WithInterruptStore(storeB),
		taskagent.WithInterruptToolNames("ask_user"),
	)

	resp, err := agentB.ResumeInterrupt(context.Background(), schema.ResumeInterruptRequest{
		InterruptID: interruptID,
		Decisions:   []schema.InterruptDecision{{ToolCallID: "tc-1", Content: "approved by human"}},
	})
	if err != nil {
		t.Fatalf("process B ResumeInterrupt: %v", err)
	}
	if resp.StopReason != schema.StopReasonComplete {
		t.Fatalf("process B StopReason = %q, want complete", resp.StopReason)
	}
	if resp.Messages[0].Text() != "done" {
		t.Errorf("process B final text = %q, want done", resp.Messages[0].Text())
	}
	if handlerRuns.Load() != 0 {
		t.Errorf("ask_user handler ran %d times across both processes, want 0", handlerRuns.Load())
	}

	secondReq := mockB.Requests()[0]
	n := len(secondReq.Messages)
	toolMsg := secondReq.Messages[n-1]
	if toolMsg.ToolCallID() != "tc-1" || toolMsg.Text() != "approved by human" {
		t.Errorf("resumed tool result = id %q text %q, want tc-1/'approved by human'", toolMsg.ToolCallID(), toolMsg.Text())
	}

	// Independently re-open the directory a third time to confirm the
	// record was durably marked Completed, not left dangling.
	storeC, err := interrupt.NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore C: %v", err)
	}
	rec, err := storeC.Get(context.Background(), interruptID)
	if err != nil {
		t.Fatalf("storeC.Get: %v", err)
	}
	if rec.Status != interrupt.StatusCompleted {
		t.Errorf("final record status = %q, want completed", rec.Status)
	}
}

// TestInterrupt_DuplicatePendingFromPolicy_RejectedBeforePersist covers a
// buggy or hostile InterruptPolicy that returns the same call ID twice.
// Create must never persist that record, and resume must never panic on a
// negative sibling-slice capacity.
func TestInterrupt_DuplicatePendingFromPolicy_RejectedBeforePersist(t *testing.T) {
	var handlerRuns atomic.Int32
	mock := newMock(makeToolCallResponse("tc-1", "ask_user", `{"question":"proceed?"}`, 30))
	store := interrupt.NewMapStore()

	a := taskagent.New(
		agent.Config{ID: "agent-dup"},
		taskagent.WithCaller(mock),
		taskagent.WithToolRegistry(askUserReg(&handlerRuns)),
		taskagent.WithInterruptStore(store),
		taskagent.WithInterruptPolicy(taskagent.InterruptPolicyFunc(
			func(_ context.Context, _ string, _ []schema.ToolCall) []string {
				return []string{"tc-1", "tc-1"}
			},
		)),
	)

	_, err := a.Run(context.Background(), &schema.RunRequest{
		SessionID: "sess-dup",
		Messages:  []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "please ask")},
	})
	if !errors.Is(err, interrupt.ErrInvalidArgument) {
		t.Fatalf("Run err = %v, want ErrInvalidArgument", err)
	}
	if handlerRuns.Load() != 0 {
		t.Errorf("ask_user handler ran %d times, want 0", handlerRuns.Load())
	}

	listed, lerr := store.List(context.Background(), "sess-dup")
	if lerr != nil {
		t.Fatalf("List: %v", lerr)
	}
	if len(listed) != 0 {
		t.Fatalf("persisted %d records after rejected policy, want 0", len(listed))
	}
}

// TestInterrupt_PrefixSubmit_EmitsEventsForCommittedDecisions keeps the
// observability contract: each durably committed decision emits
// interrupt_decision_stored, even when a later item in the same
// ResumeInterrupt call is rejected.
func TestInterrupt_PrefixSubmit_EmitsEventsForCommittedDecisions(t *testing.T) {
	var handlerRuns atomic.Int32
	mock := newMock(
		makeMultiToolCallResponse(
			30,
			schema.ToolCall{ID: "tc-1", Name: "ask_user", Arguments: `{"question":"a?"}`},
			schema.ToolCall{ID: "tc-2", Name: "ask_user", Arguments: `{"question":"b?"}`},
		),
	)
	store := interrupt.NewMapStore()
	hookMgr, events := eventCollector()

	a := taskagent.New(
		agent.Config{ID: "agent-prefix-evt"},
		taskagent.WithCaller(mock),
		taskagent.WithToolRegistry(askUserReg(&handlerRuns)),
		taskagent.WithInterruptStore(store),
		taskagent.WithInterruptToolNames("ask_user"),
		taskagent.WithHookManager(hookMgr),
	)

	first, err := a.Run(context.Background(), &schema.RunRequest{
		SessionID: "sess-prefix-evt",
		Messages:  []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "go")},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	_, err = a.ResumeInterrupt(context.Background(), schema.ResumeInterruptRequest{
		InterruptID: first.Interrupt.InterruptID,
		Decisions: []schema.InterruptDecision{
			{ToolCallID: "tc-1", Content: "yes"},
			{ToolCallID: "not-a-call", Content: "x"},
		},
	})
	if !errors.Is(err, interrupt.ErrUnknownToolCall) {
		t.Fatalf("ResumeInterrupt err = %v, want ErrUnknownToolCall", err)
	}

	var stored int
	for _, e := range events() {
		if e == schema.EventInterruptDecisionStored {
			stored++
		}
	}
	if stored != 1 {
		t.Errorf("interrupt_decision_stored count = %d, want 1 (committed prefix only)", stored)
	}

	rec, gerr := store.Get(context.Background(), first.Interrupt.InterruptID)
	if gerr != nil {
		t.Fatalf("store.Get: %v", gerr)
	}
	if rec.Decisions["tc-1"].Content != "yes" {
		t.Errorf("prefix decision missing: %+v", rec.Decisions)
	}
	if _, ok := rec.Decisions["not-a-call"]; ok {
		t.Error("rejected decision was persisted")
	}
	if rec.Status != interrupt.StatusPending {
		t.Errorf("status = %q, want pending", rec.Status)
	}
}

// barrierStore delays every SubmitDecisions until `n` of them have arrived,
// pinning the interleaving the race needs: all resumers finish their
// pre-submit Get before any decision is written, so each of them would see
// "absent before, present after" if the event were attributed by diffing
// those two reads.
type barrierStore struct {
	interrupt.Store
	arrive sync.WaitGroup
}

func newBarrierStore(inner interrupt.Store, n int) *barrierStore {
	s := &barrierStore{Store: inner}
	s.arrive.Add(n)
	return s
}

func (s *barrierStore) SubmitDecisions(ctx context.Context, id string, decisions []interrupt.Decision) (*interrupt.Record, []string, error) {
	s.arrive.Done()
	s.arrive.Wait()
	return s.Store.SubmitDecisions(ctx, id, decisions)
}

// TestInterrupt_ConcurrentIdenticalSubmit_EmitsOneEvent covers the racing
// version of the idempotency contract: resumers submitting the same
// decision at the same time must still produce exactly one
// interrupt_decision_stored, because only one of them wrote it. They all
// see the same post-submit record, so the event can only be attributed by
// what the store reports each call committed.
//
// Only one of the two pending calls is decided, so the record stays Pending
// and no resumer takes a lease or reaches the model.
func TestInterrupt_ConcurrentIdenticalSubmit_EmitsOneEvent(t *testing.T) {
	var handlerRuns atomic.Int32
	mock := newMock(
		makeMultiToolCallResponse(
			30,
			schema.ToolCall{ID: "tc-1", Name: "ask_user", Arguments: `{"question":"a?"}`},
			schema.ToolCall{ID: "tc-2", Name: "ask_user", Arguments: `{"question":"b?"}`},
		),
	)
	const resumers = 4
	store := newBarrierStore(interrupt.NewMapStore(), resumers)
	hookMgr, events := eventCollector()

	a := taskagent.New(
		agent.Config{ID: "agent-race-evt"},
		taskagent.WithCaller(mock),
		taskagent.WithToolRegistry(askUserReg(&handlerRuns)),
		taskagent.WithInterruptStore(store),
		taskagent.WithInterruptToolNames("ask_user"),
		taskagent.WithHookManager(hookMgr),
	)

	first, err := a.Run(context.Background(), &schema.RunRequest{
		SessionID: "sess-race-evt",
		Messages:  []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "go")},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)
	start := make(chan struct{})
	wg.Add(resumers)
	for range resumers {
		go func() {
			defer wg.Done()
			<-start
			_, resumeErr := a.ResumeInterrupt(context.Background(), schema.ResumeInterruptRequest{
				InterruptID: first.Interrupt.InterruptID,
				Decisions:   []schema.InterruptDecision{{ToolCallID: "tc-1", Content: "yes"}},
			})
			if resumeErr != nil {
				mu.Lock()
				errs = append(errs, resumeErr)
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()

	if len(errs) > 0 {
		t.Fatalf("concurrent ResumeInterrupt errors: %v", errs)
	}

	stored := 0
	for _, e := range events() {
		if e == schema.EventInterruptDecisionStored {
			stored++
		}
	}
	if stored != 1 {
		t.Errorf("interrupt_decision_stored count = %d, want 1 across %d identical concurrent submits", stored, resumers)
	}
	if handlerRuns.Load() != 0 {
		t.Errorf("ask_user handler ran %d times, want 0", handlerRuns.Load())
	}
}

// TestInterrupt_IdempotentResubmit_EmitsNoDuplicateEvents pins the other
// half of the event contract: interrupt_decision_stored counts durable
// writes, not submitted items. Resubmitting an identical decision is a
// store no-op, so it must emit nothing — and a batch mixing an already
// stored decision with a new one emits exactly one event, for the new one.
func TestInterrupt_IdempotentResubmit_EmitsNoDuplicateEvents(t *testing.T) {
	var handlerRuns atomic.Int32
	mock := newMock(
		makeMultiToolCallResponse(
			30,
			schema.ToolCall{ID: "tc-1", Name: "ask_user", Arguments: `{"question":"a?"}`},
			schema.ToolCall{ID: "tc-2", Name: "ask_user", Arguments: `{"question":"b?"}`},
		),
		makeStopResponse("done", 20),
	)
	store := interrupt.NewMapStore()
	hookMgr, events := eventCollector()

	a := taskagent.New(
		agent.Config{ID: "agent-idem-evt"},
		taskagent.WithCaller(mock),
		taskagent.WithToolRegistry(askUserReg(&handlerRuns)),
		taskagent.WithInterruptStore(store),
		taskagent.WithInterruptToolNames("ask_user"),
		taskagent.WithHookManager(hookMgr),
	)

	first, err := a.Run(context.Background(), &schema.RunRequest{
		SessionID: "sess-idem-evt",
		Messages:  []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "go")},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	interruptID := first.Interrupt.InterruptID

	storedCount := func() int {
		n := 0
		for _, e := range events() {
			if e == schema.EventInterruptDecisionStored {
				n++
			}
		}
		return n
	}

	// First submit: one new decision, one event.
	if _, err := a.ResumeInterrupt(context.Background(), schema.ResumeInterruptRequest{
		InterruptID: interruptID,
		Decisions:   []schema.InterruptDecision{{ToolCallID: "tc-1", Content: "yes"}},
	}); err != nil {
		t.Fatalf("first ResumeInterrupt: %v", err)
	}
	if got := storedCount(); got != 1 {
		t.Fatalf("after first submit: stored events = %d, want 1", got)
	}

	// Replay of exactly the same decision: nothing is written, so nothing
	// is emitted.
	if _, err := a.ResumeInterrupt(context.Background(), schema.ResumeInterruptRequest{
		InterruptID: interruptID,
		Decisions:   []schema.InterruptDecision{{ToolCallID: "tc-1", Content: "yes"}},
	}); err != nil {
		t.Fatalf("replay ResumeInterrupt: %v", err)
	}
	if got := storedCount(); got != 1 {
		t.Errorf("after idempotent replay: stored events = %d, want 1", got)
	}

	// Mixed batch: the replayed item stays silent, the new one emits. The
	// batch is now fully decided, so this call really resumes.
	if _, err := a.ResumeInterrupt(context.Background(), schema.ResumeInterruptRequest{
		InterruptID: interruptID,
		Decisions: []schema.InterruptDecision{
			{ToolCallID: "tc-1", Content: "yes"},
			{ToolCallID: "tc-2", Content: "no"},
		},
	}); err != nil {
		t.Fatalf("mixed ResumeInterrupt: %v", err)
	}
	if got := storedCount(); got != 2 {
		t.Errorf("after mixed batch: stored events = %d, want 2", got)
	}

	rec, gerr := store.Get(context.Background(), interruptID)
	if gerr != nil {
		t.Fatalf("store.Get: %v", gerr)
	}
	if len(rec.Decisions) != 2 {
		t.Errorf("persisted decisions = %d, want 2", len(rec.Decisions))
	}
}

func containsStr(list []string, want string) bool {
	return slices.Contains(list, want)
}

func indexOf(list []string, want string) int {
	return slices.Index(list, want)
}

func lastIndexOf(list []string, want string) int {
	for i, s := range slices.Backward(list) {
		if s == want {
			return i
		}
	}
	return -1
}
