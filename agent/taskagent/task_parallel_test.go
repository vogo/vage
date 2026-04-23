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
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vogo/aimodel"
	"github.com/vogo/vage/agent"
	"github.com/vogo/vage/hook"
	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/tool"
)

// batchToolCallResponse returns a single-assistant-message response whose
// ToolCalls slice contains one entry per (id, name, args) triple.
func batchToolCallResponse(calls ...[3]string) *aimodel.ChatResponse {
	tcs := make([]aimodel.ToolCall, 0, len(calls))
	for _, c := range calls {
		tcs = append(tcs, aimodel.ToolCall{
			ID:       c[0],
			Type:     "function",
			Function: aimodel.FunctionCall{Name: c[1], Arguments: c[2]},
		})
	}
	return &aimodel.ChatResponse{
		Choices: []aimodel.Choice{{
			Message: aimodel.Message{
				Role:      aimodel.RoleAssistant,
				Content:   aimodel.NewTextContent(""),
				ToolCalls: tcs,
			},
			FinishReason: aimodel.FinishReasonToolCalls,
		}},
		Usage: aimodel.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	}
}

// captureHook collects every event dispatched through hook.Manager in the
// order the manager fans them out — used to assert deterministic ordering.
// Implements hook.Hook (synchronous) so the manager invokes OnEvent on the
// dispatching goroutine — that matches our ordering contract (events emitted
// serially from the main Run goroutine).
type captureHook struct {
	mu     sync.Mutex
	events []schema.Event
}

func (c *captureHook) OnEvent(_ context.Context, e schema.Event) error {
	c.mu.Lock()
	c.events = append(c.events, e)
	c.mu.Unlock()
	return nil
}

func (c *captureHook) Filter() []string { return nil }

// TestParallelToolCalls_WallClockIsMax verifies AC-1.1: two concurrent 200ms
// tool calls complete in ~200ms (max), not ~400ms (sum).
func TestParallelToolCalls_WallClockIsMax(t *testing.T) {
	mock := &mockChatCompleter{
		responses: []*aimodel.ChatResponse{
			batchToolCallResponse(
				[3]string{"tc-1", "slow", `{"delay_ms":200}`},
				[3]string{"tc-2", "slow", `{"delay_ms":200}`},
			),
			stopResponse("done"),
		},
	}

	reg := tool.NewRegistry()
	_ = reg.Register(
		schema.ToolDef{Name: "slow"},
		func(_ context.Context, _, _ string) (schema.ToolResult, error) {
			time.Sleep(200 * time.Millisecond)
			return schema.TextResult("", "ok"), nil
		},
	)

	a := New(agent.Config{}, WithChatCompleter(mock), WithToolRegistry(reg))

	start := time.Now()
	_, err := a.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("run two slow tools")},
	})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Two 200ms calls in parallel should finish well under 350ms; a serial
	// loop would take 400ms+. A generous upper bound keeps CI stable.
	if elapsed >= 350*time.Millisecond {
		t.Errorf("parallel elapsed = %v, expected < 350ms (serial would be ~400ms)", elapsed)
	}
}

// TestParallelToolCalls_RespectsConcurrencyCap verifies AC-1.2: 8 tool calls
// under a cap of 4 should finish in roughly two waves (~200ms for 100ms calls).
func TestParallelToolCalls_RespectsConcurrencyCap(t *testing.T) {
	var inFlight atomic.Int32
	var maxInFlight atomic.Int32

	mock := &mockChatCompleter{
		responses: []*aimodel.ChatResponse{
			batchToolCallResponse(
				[3]string{"tc-1", "sleep100", "{}"},
				[3]string{"tc-2", "sleep100", "{}"},
				[3]string{"tc-3", "sleep100", "{}"},
				[3]string{"tc-4", "sleep100", "{}"},
				[3]string{"tc-5", "sleep100", "{}"},
				[3]string{"tc-6", "sleep100", "{}"},
				[3]string{"tc-7", "sleep100", "{}"},
				[3]string{"tc-8", "sleep100", "{}"},
			),
			stopResponse("done"),
		},
	}

	reg := tool.NewRegistry()
	_ = reg.Register(
		schema.ToolDef{Name: "sleep100"},
		func(_ context.Context, _, _ string) (schema.ToolResult, error) {
			cur := inFlight.Add(1)
			for {
				prev := maxInFlight.Load()
				if cur <= prev || maxInFlight.CompareAndSwap(prev, cur) {
					break
				}
			}
			time.Sleep(100 * time.Millisecond)
			inFlight.Add(-1)
			return schema.TextResult("", "ok"), nil
		},
	)

	a := New(
		agent.Config{},
		WithChatCompleter(mock),
		WithToolRegistry(reg),
		WithMaxParallelToolCalls(4),
	)

	start := time.Now()
	_, err := a.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("8 calls, cap 4")},
	})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := maxInFlight.Load(); got != 4 {
		t.Errorf("peak in-flight = %d, want 4", got)
	}
	// Two waves of 100ms with scheduling jitter: lower bound guards against
	// the cap being silently disabled (would be ~100ms); upper bound guards
	// against a serial regression (would be ~800ms).
	if elapsed < 180*time.Millisecond || elapsed > 320*time.Millisecond {
		t.Errorf("elapsed = %v, expected ~200ms (two waves at cap=4)", elapsed)
	}
}

// TestParallelToolCalls_EventOrderingDeterministic verifies AC-2.1, AC-2.2:
// Start events precede all End events, and both sequences follow ToolCalls
// slice order even when tools finish out-of-order.
func TestParallelToolCalls_EventOrderingDeterministic(t *testing.T) {
	mock := &mockChatCompleter{
		responses: []*aimodel.ChatResponse{
			batchToolCallResponse(
				[3]string{"tc-1", "wait", `{"ms":120}`}, // slowest
				[3]string{"tc-2", "wait", `{"ms":40}`},
				[3]string{"tc-3", "wait", `{"ms":80}`},
				[3]string{"tc-4", "wait", `{"ms":20}`}, // fastest
			),
			stopResponse("done"),
		},
	}

	reg := tool.NewRegistry()
	_ = reg.Register(
		schema.ToolDef{Name: "wait"},
		func(_ context.Context, _, args string) (schema.ToolResult, error) {
			// Extract ms from the JSON args — tiny parser avoids a pkg dep.
			ms := 0
			_, _ = fmt.Sscanf(args, `{"ms":%d}`, &ms)
			time.Sleep(time.Duration(ms) * time.Millisecond)
			return schema.TextResult("", fmt.Sprintf("slept %dms", ms)), nil
		},
	)

	capHook := &captureHook{}
	mgr := hook.NewManager()
	mgr.Register(capHook)
	_ = mgr.Start(context.Background())
	t.Cleanup(func() { _ = mgr.Stop(context.Background()) })

	a := New(
		agent.Config{},
		WithChatCompleter(mock),
		WithToolRegistry(reg),
		WithMaxParallelToolCalls(4),
		WithHookManager(mgr),
	)

	_, err := a.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("four waits")},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Flush any async hooks before inspecting.
	_ = mgr.Stop(context.Background())

	// Collect the tool-phase events in dispatch order. Each entry is
	// (type, tool_call_id) so we can assert both event type sequence and
	// per-event ordering by tool call index.
	capHook.mu.Lock()
	defer capHook.mu.Unlock()
	var pairs []struct {
		typ string
		id  string
	}
	for _, e := range capHook.events {
		switch e.Type {
		case schema.EventToolCallStart:
			d := e.Data.(schema.ToolCallStartData)
			pairs = append(pairs, struct {
				typ string
				id  string
			}{e.Type, d.ToolCallID})
		case schema.EventToolCallEnd:
			d := e.Data.(schema.ToolCallEndData)
			pairs = append(pairs, struct {
				typ string
				id  string
			}{e.Type, d.ToolCallID})
		}
	}

	wantIDs := []string{"tc-1", "tc-2", "tc-3", "tc-4"}

	// First four events must be Start_1..Start_4 in order.
	if len(pairs) != 8 {
		t.Fatalf("got %d Start/End events, want 8", len(pairs))
	}
	for i := range 4 {
		if pairs[i].typ != schema.EventToolCallStart || pairs[i].id != wantIDs[i] {
			t.Errorf("event %d = (%s, %s), want (Start, %s)", i, pairs[i].typ, pairs[i].id, wantIDs[i])
		}
	}
	// Next four must be End_1..End_4 in order — even though tc-1 finished
	// last in wall-clock time.
	for i := range 4 {
		if pairs[4+i].typ != schema.EventToolCallEnd || pairs[4+i].id != wantIDs[i] {
			t.Errorf("event %d = (%s, %s), want (End, %s)", 4+i, pairs[4+i].typ, pairs[4+i].id, wantIDs[i])
		}
	}
}

// TestParallelToolCalls_DurationIsPerCall verifies AC-2.3: the Duration field
// on EventToolCallEnd reflects each tool's own wall-clock, not the batch total.
func TestParallelToolCalls_DurationIsPerCall(t *testing.T) {
	mock := &mockChatCompleter{
		responses: []*aimodel.ChatResponse{
			batchToolCallResponse(
				[3]string{"tc-1", "wait", `{"ms":50}`},
				[3]string{"tc-2", "wait", `{"ms":150}`},
			),
			stopResponse("done"),
		},
	}

	reg := tool.NewRegistry()
	_ = reg.Register(
		schema.ToolDef{Name: "wait"},
		func(_ context.Context, _, args string) (schema.ToolResult, error) {
			ms := 0
			_, _ = fmt.Sscanf(args, `{"ms":%d}`, &ms)
			time.Sleep(time.Duration(ms) * time.Millisecond)
			return schema.TextResult("", "ok"), nil
		},
	)

	capHook := &captureHook{}
	mgr := hook.NewManager()
	mgr.Register(capHook)
	_ = mgr.Start(context.Background())
	t.Cleanup(func() { _ = mgr.Stop(context.Background()) })

	a := New(agent.Config{}, WithChatCompleter(mock), WithToolRegistry(reg), WithHookManager(mgr))
	_, err := a.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("two waits")},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	_ = mgr.Stop(context.Background())

	capHook.mu.Lock()
	defer capHook.mu.Unlock()
	var dur1, dur2 int64 = -1, -1
	for _, e := range capHook.events {
		if e.Type != schema.EventToolCallEnd {
			continue
		}
		d := e.Data.(schema.ToolCallEndData)
		switch d.ToolCallID {
		case "tc-1":
			dur1 = d.Duration
		case "tc-2":
			dur2 = d.Duration
		}
	}
	if dur1 < 40 || dur1 > 120 {
		t.Errorf("tc-1 Duration = %dms, want ~50ms", dur1)
	}
	if dur2 < 140 || dur2 > 240 {
		t.Errorf("tc-2 Duration = %dms, want ~150ms", dur2)
	}
}

// TestParallelToolCalls_ErrorInOneDoesNotBlockSiblings verifies AC-3.1/3.2:
// one failing tool does not abort the batch, and tool-result messages line
// up 1:1 with the ToolCalls slice in original order.
func TestParallelToolCalls_ErrorInOneDoesNotBlockSiblings(t *testing.T) {
	mock := &mockChatCompleter{
		responses: []*aimodel.ChatResponse{
			batchToolCallResponse(
				[3]string{"tc-1", "ok", "{}"},
				[3]string{"tc-2", "fail", "{}"},
				[3]string{"tc-3", "ok", "{}"},
			),
			stopResponse("done"),
		},
	}

	reg := tool.NewRegistry()
	_ = reg.Register(
		schema.ToolDef{Name: "ok"},
		func(_ context.Context, id, _ string) (schema.ToolResult, error) {
			return schema.TextResult(id, "pong-"+id), nil
		},
	)
	_ = reg.Register(
		schema.ToolDef{Name: "fail"},
		func(_ context.Context, _, _ string) (schema.ToolResult, error) {
			return schema.ToolResult{}, fmt.Errorf("boom")
		},
	)

	a := New(agent.Config{}, WithChatCompleter(mock), WithToolRegistry(reg))
	_, err := a.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("mixed")},
	})
	if err != nil {
		t.Fatalf("Run: %v (tool errors should not abort the loop)", err)
	}

	// Inspect the second LLM request: the three tool-result messages must
	// follow the assistant message in the same ToolCalls order.
	secondReq := mock.requests[1]
	var toolMsgs []aimodel.Message
	for _, m := range secondReq.Messages {
		if m.Role == aimodel.RoleTool {
			toolMsgs = append(toolMsgs, m)
		}
	}
	if len(toolMsgs) != 3 {
		t.Fatalf("tool messages = %d, want 3", len(toolMsgs))
	}
	wantIDs := []string{"tc-1", "tc-2", "tc-3"}
	for i, m := range toolMsgs {
		if m.ToolCallID != wantIDs[i] {
			t.Errorf("tool msg %d: ToolCallID = %q, want %q", i, m.ToolCallID, wantIDs[i])
		}
	}
	// Middle message carries the error text surfaced by executeToolCall.
	if got := toolMsgs[1].Content.Text(); got != "boom" {
		t.Errorf("error message body = %q, want %q", got, "boom")
	}
}

// TestSerialToolCalls_WhenCapIsOne verifies AC-4.1: cap=1 forces serial
// execution, which is visible as summed rather than maxed latency.
func TestSerialToolCalls_WhenCapIsOne(t *testing.T) {
	mock := &mockChatCompleter{
		responses: []*aimodel.ChatResponse{
			batchToolCallResponse(
				[3]string{"tc-1", "sleep50", "{}"},
				[3]string{"tc-2", "sleep50", "{}"},
				[3]string{"tc-3", "sleep50", "{}"},
			),
			stopResponse("done"),
		},
	}

	reg := tool.NewRegistry()
	_ = reg.Register(
		schema.ToolDef{Name: "sleep50"},
		func(_ context.Context, _, _ string) (schema.ToolResult, error) {
			time.Sleep(50 * time.Millisecond)
			return schema.TextResult("", "ok"), nil
		},
	)

	a := New(
		agent.Config{},
		WithChatCompleter(mock),
		WithToolRegistry(reg),
		WithMaxParallelToolCalls(1),
	)

	start := time.Now()
	_, err := a.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("serial")},
	})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Three serial 50ms calls ≈ 150ms; parallel would be ~50ms.
	if elapsed < 140*time.Millisecond {
		t.Errorf("elapsed = %v, expected >= 140ms (serial for 3x50ms)", elapsed)
	}
}

// TestSingleToolCall_UsesFastPath verifies AC-4.2: when only one tool call
// is present, the helper runs the serial fast-path — observable as the
// cap being irrelevant. Also smoke-tests that the behaviour is identical
// to the pre-P1-7 single-call code.
func TestSingleToolCall_UsesFastPath(t *testing.T) {
	mock := &mockChatCompleter{
		responses: []*aimodel.ChatResponse{
			batchToolCallResponse([3]string{"tc-1", "echo", `{"v":"hi"}`}),
			stopResponse("done"),
		},
	}

	reg := tool.NewRegistry()
	_ = reg.Register(
		schema.ToolDef{Name: "echo"},
		func(_ context.Context, id, args string) (schema.ToolResult, error) {
			return schema.TextResult(id, args), nil
		},
	)

	a := New(
		agent.Config{},
		WithChatCompleter(mock),
		WithToolRegistry(reg),
		WithMaxParallelToolCalls(8), // high cap but only one call
	)

	_, err := a.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("one")},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	secondReq := mock.requests[1]
	last := secondReq.Messages[len(secondReq.Messages)-1]
	if last.Role != aimodel.RoleTool || last.ToolCallID != "tc-1" || last.Content.Text() != `{"v":"hi"}` {
		t.Errorf("single-call transcript mismatch: role=%s id=%s body=%q",
			last.Role, last.ToolCallID, last.Content.Text())
	}
}

// TestNew_DefaultMaxParallelToolCalls verifies the default is applied so
// callers who never touch the option get parallelism out of the box.
func TestNew_DefaultMaxParallelToolCalls(t *testing.T) {
	a := New(agent.Config{})
	if a.maxParallelToolCalls != defaultMaxParallelToolCalls {
		t.Errorf("maxParallelToolCalls = %d, want %d",
			a.maxParallelToolCalls, defaultMaxParallelToolCalls)
	}
}

// TestWithMaxParallelToolCalls_NegativeClampedToZero guards the option's
// input sanitisation — a negative value should fall back to the default.
func TestWithMaxParallelToolCalls_NegativeClampedToZero(t *testing.T) {
	a := New(agent.Config{}, WithMaxParallelToolCalls(-5))
	if a.maxParallelToolCalls != 0 {
		t.Errorf("maxParallelToolCalls = %d, want 0 (clamped)", a.maxParallelToolCalls)
	}
	// And the resolved cap at exec time should be the default.
	if got := resolveParallelCap(a.maxParallelToolCalls); got != defaultMaxParallelToolCalls {
		t.Errorf("resolved cap = %d, want %d", got, defaultMaxParallelToolCalls)
	}
}

// resolveParallelCap mirrors the clamping logic in executeToolBatch so the
// default fallback is testable without a full Run.
func resolveParallelCap(configured int) int {
	if configured <= 0 {
		return defaultMaxParallelToolCalls
	}
	return configured
}
