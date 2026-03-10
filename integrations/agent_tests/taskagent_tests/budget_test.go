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
	"io"
	"sync"
	"testing"

	"github.com/vogo/aimodel"
	"github.com/vogo/vagent/agent"
	"github.com/vogo/vagent/agent/taskagent"
	"github.com/vogo/vagent/guard"
	"github.com/vogo/vagent/hook"
	"github.com/vogo/vagent/schema"
	"github.com/vogo/vagent/tool"
)

// mockChatCompleter implements aimodel.ChatCompleter for integration tests.
// It returns pre-configured responses with known usage data.
type mockChatCompleter struct {
	mu        sync.Mutex
	calls     int
	responses []*aimodel.ChatResponse
}

func (m *mockChatCompleter) ChatCompletion(_ context.Context, _ *aimodel.ChatRequest) (*aimodel.ChatResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.calls >= len(m.responses) {
		return nil, errors.New("mock: no more responses")
	}

	resp := m.responses[m.calls]
	m.calls++

	return resp, nil
}

func (m *mockChatCompleter) ChatCompletionStream(_ context.Context, _ *aimodel.ChatRequest) (*aimodel.Stream, error) {
	return nil, errors.New("not implemented in mock")
}

func (m *mockChatCompleter) getCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.calls
}

// makeStopResponse creates a stop response with the given text and total token usage.
func makeStopResponse(text string, totalTokens int) *aimodel.ChatResponse {
	return &aimodel.ChatResponse{
		Choices: []aimodel.Choice{{
			Message:      aimodel.Message{Role: aimodel.RoleAssistant, Content: aimodel.NewTextContent(text)},
			FinishReason: aimodel.FinishReasonStop,
		}},
		Usage: aimodel.Usage{
			PromptTokens:     totalTokens / 2,
			CompletionTokens: totalTokens - totalTokens/2,
			TotalTokens:      totalTokens,
		},
	}
}

// makeToolCallResponse creates a tool call response with the given usage.
func makeToolCallResponse(toolCallID, funcName, args string, totalTokens int) *aimodel.ChatResponse {
	return &aimodel.ChatResponse{
		Choices: []aimodel.Choice{{
			Message: aimodel.Message{
				Role:    aimodel.RoleAssistant,
				Content: aimodel.NewTextContent(""),
				ToolCalls: []aimodel.ToolCall{{
					ID:       toolCallID,
					Type:     "function",
					Function: aimodel.FunctionCall{Name: funcName, Arguments: args},
				}},
			},
			FinishReason: aimodel.FinishReasonToolCalls,
		}},
		Usage: aimodel.Usage{
			PromptTokens:     totalTokens / 2,
			CompletionTokens: totalTokens - totalTokens/2,
			TotalTokens:      totalTokens,
		},
	}
}

// noopTool creates a tool registry with a single no-op tool.
func noopTool(name string) *tool.Registry {
	reg := tool.NewRegistry()
	_ = reg.Register(
		schema.ToolDef{Name: name, Description: "A test tool"},
		func(_ context.Context, _, _ string) (schema.ToolResult, error) {
			return schema.TextResult("", "ok"), nil
		},
	)

	return reg
}

// rewriteGuard is a test output guard that rewrites all output content.
type rewriteGuard struct {
	replacement string
}

func (g *rewriteGuard) Check(_ *guard.Message) (*guard.Result, error) {
	return guard.Rewrite("rewrite-guard", g.replacement, "test rewrite"), nil
}

func (g *rewriteGuard) Name() string { return "rewrite-guard" }

// collectEvents collects all events from the hook manager into a slice.
func collectEvents(hm *hook.Manager) *[]schema.Event {
	var mu sync.Mutex

	events := &[]schema.Event{}
	hm.Register(hook.NewHookFunc(func(_ context.Context, e schema.Event) error {
		mu.Lock()
		*events = append(*events, e)
		mu.Unlock()

		return nil
	}))

	return events
}

// Test 1: Budget exhaustion returns partial results (Run).
//
// Configures an TaskAgent with a mock ChatCompleter that returns responses with
// known usage (100 tokens per call). Sets budget to 200 to allow exactly 2 LLM
// calls. Verifies:
//   - The third call is prevented by the pre-call budget check.
//   - RunResponse is non-nil with StopReason == StopReasonBudgetExhausted.
//   - Usage.TotalTokens reflects actual consumption (200).
//   - Messages contains the last assistant response.
//   - No error is returned.
func TestBudgetExhaustion_Run_PartialResults(t *testing.T) {
	mock := &mockChatCompleter{
		responses: []*aimodel.ChatResponse{
			// Iteration 1: tool call using 100 tokens
			makeToolCallResponse("tc-1", "do_thing", "{}", 100),
			// Iteration 2: tool call using 100 tokens (budget hits 200, exhausted)
			makeToolCallResponse("tc-2", "do_thing", "{}", 100),
			// Iteration 3: should NOT be reached
			makeStopResponse("should not reach this", 100),
		},
	}

	a := taskagent.New(agent.Config{ID: "budget-test-agent"},
		taskagent.WithChatCompleter(mock),
		taskagent.WithToolRegistry(noopTool("do_thing")),
		taskagent.WithRunTokenBudget(200),
		taskagent.WithMaxIterations(10),
	)

	resp, err := a.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("do things")},
	})
	// No error should be returned.
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	// Response must be non-nil.
	if resp == nil {
		t.Fatal("expected non-nil response")
	}

	// StopReason must indicate budget exhaustion.
	if resp.StopReason != schema.StopReasonBudgetExhausted {
		t.Errorf("StopReason = %q, want %q", resp.StopReason, schema.StopReasonBudgetExhausted)
	}

	// Usage must reflect actual consumption.
	if resp.Usage == nil {
		t.Fatal("Usage should not be nil")
	}

	if resp.Usage.TotalTokens != 200 {
		t.Errorf("Usage.TotalTokens = %d, want 200", resp.Usage.TotalTokens)
	}

	// The third LLM call must NOT have happened.
	if mock.getCalls() != 2 {
		t.Errorf("LLM calls = %d, want 2", mock.getCalls())
	}

	// Messages should contain the last assistant response.
	if len(resp.Messages) == 0 {
		t.Error("expected at least one message in partial result")
	}
}

// Test 2: Budget exhaustion emits correct events (Run path with hooks).
//
// Configures an TaskAgent with a mock ChatCompleter and a hook manager.
// Sets a small budget that exhausts after the first LLM call. Verifies:
//   - EventTokenBudgetExhausted appears in the event sequence.
//   - It appears before EventAgentEnd.
//   - The event data contains correct Budget, Used, and Iterations values.
//   - AgentEndData.StopReason is StopReasonBudgetExhausted.
func TestBudgetExhaustion_EmitsCorrectEvents(t *testing.T) {
	hm := hook.NewManager()
	events := collectEvents(hm)

	mock := &mockChatCompleter{
		responses: []*aimodel.ChatResponse{
			// Single tool call that uses 500 tokens, budget is 100
			makeToolCallResponse("tc-1", "tool1", "{}", 500),
		},
	}

	a := taskagent.New(agent.Config{ID: "event-test-agent"},
		taskagent.WithChatCompleter(mock),
		taskagent.WithToolRegistry(noopTool("tool1")),
		taskagent.WithRunTokenBudget(100),
		taskagent.WithHookManager(hm),
	)

	resp, err := a.Run(context.Background(), &schema.RunRequest{
		Messages:  []schema.Message{schema.NewUserMessage("do something")},
		SessionID: "test-session",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.StopReason != schema.StopReasonBudgetExhausted {
		t.Fatalf("StopReason = %q, want %q", resp.StopReason, schema.StopReasonBudgetExhausted)
	}

	// Find budget exhausted and agent end events.
	budgetEventIdx := -1
	agentEndIdx := -1

	for i, e := range *events {
		if e.Type == schema.EventTokenBudgetExhausted {
			budgetEventIdx = i

			data, ok := e.Data.(schema.TokenBudgetExhaustedData)
			if !ok {
				t.Fatalf("EventTokenBudgetExhausted data type = %T, want TokenBudgetExhaustedData", e.Data)
			}

			// Verify event data.
			if data.Budget != 100 {
				t.Errorf("event Budget = %d, want 100", data.Budget)
			}

			if data.Used != 500 {
				t.Errorf("event Used = %d, want 500", data.Used)
			}

			if data.Iterations != 1 {
				t.Errorf("event Iterations = %d, want 1", data.Iterations)
			}
		}

		if e.Type == schema.EventAgentEnd {
			agentEndIdx = i

			endData, ok := e.Data.(schema.AgentEndData)
			if !ok {
				t.Fatalf("EventAgentEnd data type = %T, want AgentEndData", e.Data)
			}

			if endData.StopReason != schema.StopReasonBudgetExhausted {
				t.Errorf("AgentEndData.StopReason = %q, want %q", endData.StopReason, schema.StopReasonBudgetExhausted)
			}
		}
	}

	if budgetEventIdx == -1 {
		t.Error("EventTokenBudgetExhausted was not dispatched")
	}

	if agentEndIdx == -1 {
		t.Error("EventAgentEnd was not dispatched")
	}

	// Budget exhausted event must appear before agent end event.
	if budgetEventIdx >= agentEndIdx {
		t.Errorf("EventTokenBudgetExhausted (idx=%d) should appear before EventAgentEnd (idx=%d)",
			budgetEventIdx, agentEndIdx)
	}
}

// Test 3: Unlimited budget (default) preserves behavior.
//
// Runs an agent with budget=0 (default). Verifies the agent completes normally
// with no budget-related events or stop reasons. This confirms backward
// compatibility.
func TestUnlimitedBudget_PreservesBehavior(t *testing.T) {
	hm := hook.NewManager()
	events := collectEvents(hm)

	mock := &mockChatCompleter{
		responses: []*aimodel.ChatResponse{
			// Tool call + follow-up response, all should complete normally
			makeToolCallResponse("tc-1", "tool1", "{}", 1000),
			makeStopResponse("All done!", 500),
		},
	}

	a := taskagent.New(agent.Config{ID: "unlimited-budget-agent"},
		taskagent.WithChatCompleter(mock),
		taskagent.WithToolRegistry(noopTool("tool1")),
		taskagent.WithHookManager(hm),
		// No WithRunTokenBudget -- defaults to 0 (unlimited)
	)

	resp, err := a.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("do things")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// StopReason must be complete for normal completion.
	if resp.StopReason != schema.StopReasonComplete {
		t.Errorf("StopReason = %q, want %q", resp.StopReason, schema.StopReasonComplete)
	}

	// Response should contain the final assistant message.
	if len(resp.Messages) != 1 {
		t.Fatalf("len(Messages) = %d, want 1", len(resp.Messages))
	}

	if resp.Messages[0].Content.Text() != "All done!" {
		t.Errorf("response text = %q, want %q", resp.Messages[0].Content.Text(), "All done!")
	}

	// No budget-related events should have been emitted.
	for _, e := range *events {
		if e.Type == schema.EventTokenBudgetExhausted {
			t.Error("EventTokenBudgetExhausted should NOT be emitted with unlimited budget")
		}
	}

	// Both LLM calls should have been made.
	if mock.getCalls() != 2 {
		t.Errorf("LLM calls = %d, want 2", mock.getCalls())
	}
}

// Test 4: Per-request override.
//
// Configures an agent with WithRunTokenBudget(10000). Sends a request with
// RunOptions.RunTokenBudget = 50. Verifies the 50-token budget is enforced,
// not the 10000 agent default.
func TestPerRequestBudgetOverride(t *testing.T) {
	mock := &mockChatCompleter{
		responses: []*aimodel.ChatResponse{
			// First call uses 100 tokens, which exceeds the per-request budget of 50
			makeToolCallResponse("tc-1", "tool1", "{}", 100),
			// This should not be reached
			makeStopResponse("should not reach", 100),
		},
	}

	a := taskagent.New(agent.Config{ID: "override-agent"},
		taskagent.WithChatCompleter(mock),
		taskagent.WithToolRegistry(noopTool("tool1")),
		taskagent.WithRunTokenBudget(10000), // Agent default: generous budget
	)

	resp, err := a.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("hi")},
		Options:  &schema.RunOptions{RunTokenBudget: 50}, // Per-request: tight budget
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The per-request budget of 50 should be enforced, not the agent default of 10000.
	if resp.StopReason != schema.StopReasonBudgetExhausted {
		t.Errorf("StopReason = %q, want %q", resp.StopReason, schema.StopReasonBudgetExhausted)
	}

	// Only 1 LLM call should have been made before budget was exhausted.
	if mock.getCalls() != 1 {
		t.Errorf("LLM calls = %d, want 1 (per-request budget should be enforced)", mock.getCalls())
	}

	// Usage should reflect the single call's consumption.
	if resp.Usage == nil {
		t.Fatal("Usage should not be nil")
	}

	if resp.Usage.TotalTokens != 100 {
		t.Errorf("Usage.TotalTokens = %d, want 100", resp.Usage.TotalTokens)
	}
}

// Test 5: MaxIterations exhaustion returns partial result.
//
// Configures an agent with WithMaxIterations(2) and a mock completer that always
// requests tool calls. Verifies:
//   - RunResponse is non-nil (not an error).
//   - StopReason == StopReasonMaxIterations.
//   - Usage is populated.
//   - This confirms the aligned behavior where max iterations returns a partial
//     result instead of an error.
func TestMaxIterationsExhaustion_ReturnsPartialResult(t *testing.T) {
	hm := hook.NewManager()
	events := collectEvents(hm)

	mock := &mockChatCompleter{
		responses: []*aimodel.ChatResponse{
			// Iteration 1: tool call
			makeToolCallResponse("tc-1", "looper", "{}", 50),
			// Iteration 2: tool call (max iterations reached after this)
			makeToolCallResponse("tc-2", "looper", "{}", 50),
			// Iteration 3: should not be reached
			makeStopResponse("should not reach", 50),
		},
	}

	a := taskagent.New(agent.Config{ID: "maxiter-agent"},
		taskagent.WithChatCompleter(mock),
		taskagent.WithToolRegistry(noopTool("looper")),
		taskagent.WithMaxIterations(2),
		taskagent.WithHookManager(hm),
	)

	resp, err := a.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("loop forever")},
	})
	// No error should be returned.
	if err != nil {
		t.Fatalf("expected no error for max iterations, got: %v", err)
	}

	// Response must be non-nil.
	if resp == nil {
		t.Fatal("expected non-nil response")
	}

	// StopReason must indicate max iterations exceeded.
	if resp.StopReason != schema.StopReasonMaxIterations {
		t.Errorf("StopReason = %q, want %q", resp.StopReason, schema.StopReasonMaxIterations)
	}

	// Usage must be populated.
	if resp.Usage == nil {
		t.Fatal("Usage should not be nil")
	}

	if resp.Usage.TotalTokens != 100 {
		t.Errorf("Usage.TotalTokens = %d, want 100", resp.Usage.TotalTokens)
	}

	// Only 2 LLM calls should have been made.
	if mock.getCalls() != 2 {
		t.Errorf("LLM calls = %d, want 2", mock.getCalls())
	}

	// Check AgentEndData.StopReason in events.
	var foundAgentEnd bool

	for _, e := range *events {
		if e.Type == schema.EventAgentEnd {
			foundAgentEnd = true

			endData, ok := e.Data.(schema.AgentEndData)
			if !ok {
				t.Fatalf("AgentEnd data type = %T, want AgentEndData", e.Data)
			}

			if endData.StopReason != schema.StopReasonMaxIterations {
				t.Errorf("AgentEndData.StopReason = %q, want %q",
					endData.StopReason, schema.StopReasonMaxIterations)
			}
		}
	}

	if !foundAgentEnd {
		t.Error("EventAgentEnd was not dispatched")
	}
}

// Test 6: Output guards run on budget-exhausted results.
//
// Configures an agent with an output guard that rewrites text. Triggers budget
// exhaustion. Verifies the returned messages have been processed by the output
// guard.
func TestBudgetExhaustion_OutputGuardsRun(t *testing.T) {
	mock := &mockChatCompleter{
		responses: []*aimodel.ChatResponse{
			// Tool call response with high usage to exhaust budget.
			// Budget is 100, usage is 500 -- exhausted in post-call check.
			makeToolCallResponse("tc-1", "tool1", "{}", 500),
		},
	}

	outputGuard := &rewriteGuard{replacement: "GUARDED OUTPUT"}

	a := taskagent.New(agent.Config{ID: "guard-budget-agent"},
		taskagent.WithChatCompleter(mock),
		taskagent.WithToolRegistry(noopTool("tool1")),
		taskagent.WithRunTokenBudget(100),
		taskagent.WithOutputGuards(outputGuard),
	)

	resp, err := a.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("do something")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.StopReason != schema.StopReasonBudgetExhausted {
		t.Fatalf("StopReason = %q, want %q", resp.StopReason, schema.StopReasonBudgetExhausted)
	}

	// The output guard should have rewritten the content.
	if len(resp.Messages) == 0 {
		t.Fatal("expected at least one message in response")
	}

	if got := resp.Messages[0].Content.Text(); got != "GUARDED OUTPUT" {
		t.Errorf("message content = %q, want %q (output guard should have rewritten it)", got, "GUARDED OUTPUT")
	}
}

// Test 7: Budget exhaustion via RunToStream closes the stream cleanly.
//
// Uses agent.RunToStream to wrap the sync Run call. Sets a budget that exhausts
// after the first LLM call. Verifies:
//   - The stream closes cleanly with EOF (no error).
//   - Events are emitted (AgentStart, AgentEnd).
//   - The hook manager receives the budget-exhausted events (EventTokenBudgetExhausted
//     before EventAgentEnd with the correct StopReason).
//
// Note: RunToStream wraps the sync Run path, so budget-related events are
// dispatched through the hook manager, not through the stream itself. The stream
// only emits the wrapper's AgentStart/AgentEnd events.
func TestBudgetExhaustion_RunToStream_CleanClose(t *testing.T) {
	hm := hook.NewManager()
	hookEvents := collectEvents(hm)

	mock := &mockChatCompleter{
		responses: []*aimodel.ChatResponse{
			// Tool call with 500 tokens, budget is 100 -> exhausted
			makeToolCallResponse("tc-1", "tool1", "{}", 500),
		},
	}

	a := taskagent.New(agent.Config{ID: "stream-budget-agent"},
		taskagent.WithChatCompleter(mock),
		taskagent.WithToolRegistry(noopTool("tool1")),
		taskagent.WithRunTokenBudget(100),
		taskagent.WithHookManager(hm),
	)

	req := &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("do something")},
	}

	rs := agent.RunToStream(context.Background(), a, req)

	var streamEvents []schema.Event

	for {
		e, recvErr := rs.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}

		if recvErr != nil {
			t.Fatalf("unexpected stream error: %v", recvErr)
		}

		streamEvents = append(streamEvents, e)
	}

	// Stream should have emitted at least AgentStart and AgentEnd from RunToStream wrapper.
	if len(streamEvents) < 2 {
		t.Fatalf("stream events = %d, want >= 2", len(streamEvents))
	}

	if streamEvents[0].Type != schema.EventAgentStart {
		t.Errorf("first stream event = %q, want %q", streamEvents[0].Type, schema.EventAgentStart)
	}

	if streamEvents[len(streamEvents)-1].Type != schema.EventAgentEnd {
		t.Errorf("last stream event = %q, want %q",
			streamEvents[len(streamEvents)-1].Type, schema.EventAgentEnd)
	}

	// Verify hook events contain budget exhaustion events in the correct order.
	budgetEventIdx := -1
	agentEndWithBudgetIdx := -1

	for i, e := range *hookEvents {
		if e.Type == schema.EventTokenBudgetExhausted {
			budgetEventIdx = i

			data, ok := e.Data.(schema.TokenBudgetExhaustedData)
			if !ok {
				t.Fatalf("budget event data type = %T", e.Data)
			}

			if data.Budget != 100 {
				t.Errorf("budget event Budget = %d, want 100", data.Budget)
			}
		}

		if e.Type == schema.EventAgentEnd {
			endData, ok := e.Data.(schema.AgentEndData)
			if ok && endData.StopReason == schema.StopReasonBudgetExhausted {
				agentEndWithBudgetIdx = i
			}
		}
	}

	if budgetEventIdx == -1 {
		t.Error("EventTokenBudgetExhausted not dispatched via hooks")
	}

	if agentEndWithBudgetIdx == -1 {
		t.Error("EventAgentEnd with StopReasonBudgetExhausted not dispatched via hooks")
	}

	if budgetEventIdx >= 0 && agentEndWithBudgetIdx >= 0 && budgetEventIdx >= agentEndWithBudgetIdx {
		t.Errorf("budget event (idx=%d) should appear before agent end (idx=%d)",
			budgetEventIdx, agentEndWithBudgetIdx)
	}
}

// Test 8: Budget with exactly enough tokens for all calls completes normally.
//
// Edge case: budget is exactly equal to total token consumption across all calls.
// The agent should complete the second call (which reaches the budget limit) but
// then be stopped before a third call. Since the second call finishes with a stop
// reason, the agent should return normally.
func TestBudgetExact_CompletesNormally(t *testing.T) {
	mock := &mockChatCompleter{
		responses: []*aimodel.ChatResponse{
			// Iteration 1: tool call using 100 tokens
			makeToolCallResponse("tc-1", "tool1", "{}", 100),
			// Iteration 2: final text response using 100 tokens (total = 200 = budget)
			makeStopResponse("Done!", 100),
		},
	}

	a := taskagent.New(agent.Config{ID: "exact-budget-agent"},
		taskagent.WithChatCompleter(mock),
		taskagent.WithToolRegistry(noopTool("tool1")),
		taskagent.WithRunTokenBudget(200),
		taskagent.WithMaxIterations(10),
	)

	resp, err := a.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("do stuff")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The second call returns a stop response (not a tool call), so the agent
	// completes normally even though budget is now exactly at the limit.
	if resp.StopReason != schema.StopReasonComplete {
		t.Errorf("StopReason = %q, want %q", resp.StopReason, schema.StopReasonComplete)
	}

	if resp.Messages[0].Content.Text() != "Done!" {
		t.Errorf("response text = %q, want %q", resp.Messages[0].Content.Text(), "Done!")
	}

	if mock.getCalls() != 2 {
		t.Errorf("LLM calls = %d, want 2", mock.getCalls())
	}
}
