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

package llmagent

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/vogo/aimodel"
	"github.com/vogo/vagent/agent"
	"github.com/vogo/vagent/guard"
	"github.com/vogo/vagent/hook"
	"github.com/vogo/vagent/schema"
	"github.com/vogo/vagent/tool"
)

// --- budgetTracker unit tests ---

func TestBudgetTracker_Unlimited(t *testing.T) {
	tr := newBudgetTracker(0)
	if tr.Exhausted() {
		t.Error("unlimited tracker should never be exhausted")
	}
	if tr.Budget() != 0 {
		t.Errorf("Budget() = %d, want 0", tr.Budget())
	}
	if tr.Remaining() != -1 {
		t.Errorf("Remaining() = %d, want -1 for unlimited", tr.Remaining())
	}
	if tr.Consumed() != 0 {
		t.Errorf("Consumed() = %d, want 0", tr.Consumed())
	}

	exhausted := tr.Add(100000)
	if exhausted {
		t.Error("Add should return false for unlimited tracker")
	}
	if tr.Exhausted() {
		t.Error("unlimited tracker should never be exhausted even after large Add")
	}
	if tr.Consumed() != 100000 {
		t.Errorf("Consumed() = %d, want 100000", tr.Consumed())
	}
}

func TestBudgetTracker_ExactBudget(t *testing.T) {
	tr := newBudgetTracker(100)
	if tr.Exhausted() {
		t.Error("should not be exhausted initially")
	}
	if tr.Remaining() != 100 {
		t.Errorf("Remaining() = %d, want 100", tr.Remaining())
	}

	exhausted := tr.Add(50)
	if exhausted {
		t.Error("should not be exhausted at 50/100")
	}
	if tr.Remaining() != 50 {
		t.Errorf("Remaining() = %d, want 50", tr.Remaining())
	}

	exhausted = tr.Add(50)
	if !exhausted {
		t.Error("should be exhausted at 100/100")
	}
	if !tr.Exhausted() {
		t.Error("Exhausted() should be true at 100/100")
	}
	if tr.Remaining() != 0 {
		t.Errorf("Remaining() = %d, want 0", tr.Remaining())
	}
}

func TestBudgetTracker_OverBudget(t *testing.T) {
	tr := newBudgetTracker(100)
	exhausted := tr.Add(150)
	if !exhausted {
		t.Error("should be exhausted when over budget")
	}
	if tr.Remaining() != 0 {
		t.Errorf("Remaining() = %d, want 0 when over budget", tr.Remaining())
	}
	if tr.Consumed() != 150 {
		t.Errorf("Consumed() = %d, want 150", tr.Consumed())
	}
}

func TestBudgetTracker_AddReturnValue(t *testing.T) {
	tr := newBudgetTracker(30)

	if tr.Add(10) {
		t.Error("Add(10) should return false for budget=30")
	}
	if tr.Add(10) {
		t.Error("Add(10) should return false for budget=30, consumed=20")
	}
	if !tr.Add(10) {
		t.Error("Add(10) should return true for budget=30, consumed=30")
	}
	if !tr.Add(5) {
		t.Error("Add(5) should return true when already exhausted")
	}
}

// --- Agent budget enforcement tests ---

func stopResponseWithUsage(text string, total int) *aimodel.ChatResponse {
	return &aimodel.ChatResponse{
		Choices: []aimodel.Choice{{
			Message:      aimodel.Message{Role: aimodel.RoleAssistant, Content: aimodel.NewTextContent(text)},
			FinishReason: aimodel.FinishReasonStop,
		}},
		Usage: aimodel.Usage{PromptTokens: total / 2, CompletionTokens: total / 2, TotalTokens: total},
	}
}

func toolCallResponseWithUsage(toolCallID, funcName, args string, total int) *aimodel.ChatResponse {
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
		Usage: aimodel.Usage{PromptTokens: total / 2, CompletionTokens: total / 2, TotalTokens: total},
	}
}

func TestAgent_Run_BudgetExhausted_AfterTwoIterations(t *testing.T) {
	// Budget allows 2 calls (100 tokens each), exhausts before 3rd call.
	mock := &mockChatCompleter{
		responses: []*aimodel.ChatResponse{
			toolCallResponseWithUsage("tc-1", "do_thing", "{}", 100),
			toolCallResponseWithUsage("tc-2", "do_thing", "{}", 100),
			stopResponseWithUsage("should not reach", 100),
		},
	}

	reg := tool.NewRegistry()
	_ = reg.Register(
		schema.ToolDef{Name: "do_thing"},
		func(_ context.Context, _, _ string) (schema.ToolResult, error) {
			return schema.TextResult("", "ok"), nil
		},
	)

	a := New(agent.Config{ID: "budget-agent"},
		WithChatCompleter(mock),
		WithToolRegistry(reg),
		WithRunTokenBudget(200),
		WithMaxIterations(10),
	)

	resp, err := a.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("do stuff")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StopReason != schema.StopReasonBudgetExhausted {
		t.Errorf("StopReason = %q, want %q", resp.StopReason, schema.StopReasonBudgetExhausted)
	}
	if resp.Usage == nil {
		t.Fatal("Usage should not be nil")
	}
	if resp.Usage.TotalTokens != 200 {
		t.Errorf("TotalTokens = %d, want 200", resp.Usage.TotalTokens)
	}
	// 3rd call should not have happened.
	if mock.calls != 2 {
		t.Errorf("LLM calls = %d, want 2", mock.calls)
	}
}

func TestAgent_Run_BudgetExhausted_SkipsToolCalls(t *testing.T) {
	// Budget exhausted after first tool-call response; tool execution skipped.
	mock := &mockChatCompleter{
		responses: []*aimodel.ChatResponse{
			toolCallResponseWithUsage("tc-1", "expensive", "{}", 500),
		},
	}

	toolExecuted := false
	reg := tool.NewRegistry()
	_ = reg.Register(
		schema.ToolDef{Name: "expensive"},
		func(_ context.Context, _, _ string) (schema.ToolResult, error) {
			toolExecuted = true
			return schema.TextResult("", "result"), nil
		},
	)

	a := New(agent.Config{ID: "budget-agent"},
		WithChatCompleter(mock),
		WithToolRegistry(reg),
		WithRunTokenBudget(100),
	)

	resp, err := a.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("do something expensive")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StopReason != schema.StopReasonBudgetExhausted {
		t.Errorf("StopReason = %q, want %q", resp.StopReason, schema.StopReasonBudgetExhausted)
	}
	if toolExecuted {
		t.Error("tool should not have been executed when budget is exhausted")
	}
}

func TestAgent_Run_BudgetUnlimited_PreservesExistingBehavior(t *testing.T) {
	mock := &mockChatCompleter{responses: []*aimodel.ChatResponse{stopResponse("Hello!")}}
	a := New(agent.Config{ID: "a1"}, WithChatCompleter(mock))

	resp, err := a.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("hi")},
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if resp.StopReason != schema.StopReasonComplete {
		t.Errorf("StopReason = %q, want %q", resp.StopReason, schema.StopReasonComplete)
	}
	if resp.Messages[0].Content.Text() != "Hello!" {
		t.Errorf("Content = %q, want %q", resp.Messages[0].Content.Text(), "Hello!")
	}
}

func TestAgent_Run_BudgetPerRequestOverride(t *testing.T) {
	// Agent default budget is 10000. Request overrides to 50.
	// The response uses 100 tokens, so budget exhausts in post-call check.
	mock := &mockChatCompleter{
		responses: []*aimodel.ChatResponse{
			toolCallResponseWithUsage("tc-1", "tool1", "{}", 100),
		},
	}

	reg := tool.NewRegistry()
	_ = reg.Register(
		schema.ToolDef{Name: "tool1"},
		func(_ context.Context, _, _ string) (schema.ToolResult, error) {
			return schema.TextResult("", "ok"), nil
		},
	)

	a := New(agent.Config{},
		WithChatCompleter(mock),
		WithToolRegistry(reg),
		WithRunTokenBudget(10000),
	)

	resp, err := a.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("hi")},
		Options:  &schema.RunOptions{RunTokenBudget: 50},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StopReason != schema.StopReasonBudgetExhausted {
		t.Errorf("StopReason = %q, want %q", resp.StopReason, schema.StopReasonBudgetExhausted)
	}
}

func TestAgent_Run_BudgetExhausted_EmitsEvents(t *testing.T) {
	hm := hook.NewManager()
	var capturedEvents []schema.Event
	hm.Register(hook.NewHookFunc(func(_ context.Context, e schema.Event) error {
		capturedEvents = append(capturedEvents, e)
		return nil
	}))

	mock2 := &mockChatCompleter{
		responses: []*aimodel.ChatResponse{
			toolCallResponseWithUsage("tc-1", "t", "{}", 100),
		},
	}
	reg := tool.NewRegistry()
	_ = reg.Register(schema.ToolDef{Name: "t"}, func(_ context.Context, _, _ string) (schema.ToolResult, error) {
		return schema.TextResult("", "ok"), nil
	})

	a2 := New(agent.Config{ID: "evt-agent"},
		WithChatCompleter(mock2),
		WithToolRegistry(reg),
		WithRunTokenBudget(50),
		WithHookManager(hm),
	)

	resp2, err := a2.Run(context.Background(), &schema.RunRequest{
		Messages:  []schema.Message{schema.NewUserMessage("hi")},
		SessionID: "s1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp2.StopReason != schema.StopReasonBudgetExhausted {
		t.Fatalf("StopReason = %q, want %q", resp2.StopReason, schema.StopReasonBudgetExhausted)
	}

	// Check that budget exhausted event was dispatched.
	var foundBudgetEvent, foundAgentEnd bool
	for _, e := range capturedEvents {
		if e.Type == schema.EventTokenBudgetExhausted {
			foundBudgetEvent = true
			data, ok := e.Data.(schema.TokenBudgetExhaustedData)
			if !ok {
				t.Fatalf("event data type = %T", e.Data)
			}
			if data.Budget != 50 {
				t.Errorf("Budget = %d, want 50", data.Budget)
			}
			if data.Used != 100 {
				t.Errorf("Used = %d, want 100", data.Used)
			}
			if data.Iterations != 1 {
				t.Errorf("Iterations = %d, want 1", data.Iterations)
			}
		}
		if e.Type == schema.EventAgentEnd {
			endData, ok := e.Data.(schema.AgentEndData)
			if ok && endData.StopReason == schema.StopReasonBudgetExhausted {
				foundAgentEnd = true
			}
		}
	}
	if !foundBudgetEvent {
		t.Error("EventTokenBudgetExhausted not dispatched")
	}
	if !foundAgentEnd {
		t.Error("EventAgentEnd with StopReasonBudgetExhausted not dispatched")
	}
}

func TestAgent_Run_BudgetExhausted_OutputGuardsRun(t *testing.T) {
	// First response: tool call with high usage to exhaust budget.
	// Second response: text "partial result" (should not be called because budget is exhausted).
	// The pre-call budget check on the second iteration triggers buildBudgetExhaustedResult
	// with lastAssistantMsg from iteration 1.
	mock := &mockChatCompleter{
		responses: []*aimodel.ChatResponse{
			toolCallResponseWithUsage("tc-1", "t", "{}", 500),
			stopResponseWithUsage("should not reach", 100),
		},
	}

	reg := tool.NewRegistry()
	_ = reg.Register(schema.ToolDef{Name: "t"}, func(_ context.Context, _, _ string) (schema.ToolResult, error) {
		return schema.TextResult("", "ok"), nil
	})

	// Output guard that rewrites content.
	rewriteGuard := &testOutputGuard{
		rewriteTo: "guarded text",
	}

	a := New(agent.Config{ID: "guard-agent"},
		WithChatCompleter(mock),
		WithToolRegistry(reg),
		WithRunTokenBudget(100),
		WithOutputGuards(rewriteGuard),
	)

	resp, err := a.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("hi")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StopReason != schema.StopReasonBudgetExhausted {
		t.Fatalf("StopReason = %q, want %q", resp.StopReason, schema.StopReasonBudgetExhausted)
	}
	// The tool call response has empty text content, so output guard runs on empty text.
	// The guard should still rewrite it.
	// However, with an empty text message, the guard still runs since respMsgs is non-empty.
	if len(resp.Messages) == 0 {
		t.Fatal("expected at least one message")
	}
	// The guard rewrites the text content.
	if got := resp.Messages[0].Content.Text(); got != "guarded text" {
		t.Errorf("Content = %q, want %q (output guard should have run)", got, "guarded text")
	}
}

// testOutputGuard is a simple guard that rewrites output content for testing.
type testOutputGuard struct {
	rewriteTo string
}

func (g *testOutputGuard) Check(_ *guard.Message) (*guard.Result, error) {
	return &guard.Result{Action: guard.ActionRewrite, Content: g.rewriteTo}, nil
}

func (g *testOutputGuard) Name() string { return "test-output-guard" }

func TestAgent_Run_WithRunTokenBudgetOption(t *testing.T) {
	a := New(agent.Config{}, WithRunTokenBudget(5000))
	if a.runTokenBudget != 5000 {
		t.Errorf("runTokenBudget = %d, want 5000", a.runTokenBudget)
	}
}

func TestAgent_ResolveRunParams_RunTokenBudget(t *testing.T) {
	a := New(agent.Config{}, WithRunTokenBudget(10000))

	// No options -- uses agent default.
	p := a.resolveRunParams(nil)
	if p.runTokenBudget != 10000 {
		t.Errorf("runTokenBudget = %d, want 10000", p.runTokenBudget)
	}

	// Options with override.
	p = a.resolveRunParams(&schema.RunOptions{RunTokenBudget: 500})
	if p.runTokenBudget != 500 {
		t.Errorf("runTokenBudget = %d, want 500", p.runTokenBudget)
	}

	// Options with zero -- keeps agent default.
	p = a.resolveRunParams(&schema.RunOptions{RunTokenBudget: 0})
	if p.runTokenBudget != 10000 {
		t.Errorf("runTokenBudget = %d, want 10000", p.runTokenBudget)
	}
}

func TestAgent_RunStream_BudgetExhausted_WithTextContent(t *testing.T) {
	// First response: text with enough bytes to exhaust budget, then tool call
	// We'll use two response sets: first returns text + tool call, budget exhausts.
	// Actually, let's make a text response that exhausts budget, then a second call
	// won't happen because budget is checked pre-call.

	// Response 1: returns text "Hello world" (11 bytes => ceil(11/4)=3 tokens)
	// followed by tool call. Budget = 2.
	// After consuming response 1, tracker has 3 tokens, budget=2, so exhausted.
	// Tool calls should be skipped.

	// Actually, for streaming, the text and tool call come in the same response.
	// Let's use two separate responses: first is text+tool_call, second would be final.
	// But with streaming, tool_call chunks don't have text deltas.
	// Let's use a simpler approach: first response is a tool call (0 text tokens),
	// second response has text that exhausts budget.

	// Simplest test: text response with large enough content.
	// "Hello world, this is a test message!" = 36 bytes => 9 tokens
	// Budget = 5. After first response, tracker has 9, budget exhausted.
	// Second call is prevented by pre-call check.

	textChunks1 := []string{
		textDeltaChunk("Hello world, this is a test message!"),
		`{"id":"1","object":"chat.completion.chunk","created":1,"model":"test","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"tc-1","type":"function","function":{"name":"tool1","arguments":"{}"}}]},"finish_reason":null}]}`,
		`{"id":"1","object":"chat.completion.chunk","created":1,"model":"test","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	}

	srv := sseStreamServer(t, [][]string{textChunks1})
	defer srv.Close()

	client, err := aimodel.NewClient(aimodel.WithAPIKey("test"), aimodel.WithBaseURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}

	reg := tool.NewRegistry()
	toolExecuted := false
	_ = reg.Register(
		schema.ToolDef{Name: "tool1"},
		func(_ context.Context, _, _ string) (schema.ToolResult, error) {
			toolExecuted = true
			return schema.TextResult("", "ok"), nil
		},
	)

	a := New(agent.Config{ID: "stream-budget"},
		WithChatCompleter(client),
		WithToolRegistry(reg),
		WithRunTokenBudget(5),
	)

	rs, err := a.RunStream(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("hi")},
	})
	if err != nil {
		t.Fatalf("RunStream error: %v", err)
	}

	var events []schema.Event
	for {
		e, recvErr := rs.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			t.Fatalf("unexpected error: %v", recvErr)
		}
		events = append(events, e)
	}

	if toolExecuted {
		t.Error("tool should not have been executed when budget is exhausted")
	}

	// Check events for budget exhaustion.
	var foundBudget, foundAgentEnd bool
	for _, e := range events {
		if e.Type == schema.EventTokenBudgetExhausted {
			foundBudget = true
			data := e.Data.(schema.TokenBudgetExhaustedData)
			if data.Budget != 5 {
				t.Errorf("Budget = %d, want 5", data.Budget)
			}
		}
		if e.Type == schema.EventAgentEnd {
			foundAgentEnd = true
			endData := e.Data.(schema.AgentEndData)
			if endData.StopReason != schema.StopReasonBudgetExhausted {
				t.Errorf("StopReason = %q, want %q", endData.StopReason, schema.StopReasonBudgetExhausted)
			}
		}
	}
	if !foundBudget {
		t.Error("EventTokenBudgetExhausted not found in stream events")
	}
	if !foundAgentEnd {
		t.Error("EventAgentEnd not found in stream events")
	}
}
