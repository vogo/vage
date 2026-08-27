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
	"io"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/vogo/vage/agent"
	"github.com/vogo/vage/checkpoint"
	"github.com/vogo/vage/hook"
	"github.com/vogo/vage/largemodel"
	"github.com/vogo/vage/memory"
	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/tool"
)

// --- shared helpers ---------------------------------------------------------

// streamChunks scripts one streaming turn that emits "Hello world" and stops.
func streamChunks() []*largemodel.Chunk {
	return []*largemodel.Chunk{
		{TextDelta: "Hello"},
		{TextDelta: " world"},
		{FinishReason: largemodel.FinishReasonStop, Usage: &schema.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}},
	}
}

// streamingMock scripts both entry points off the same fake: Run replays
// Responses, RunStream replays Chunks. Both produce "Hello world" so a test
// can assert that a middleware behaves identically on either path.
func streamingMock() *mockCaller {
	return &mockCaller{FakeCaller: &largemodel.FakeCaller{
		Responses: []*largemodel.Response{stopResponse("Hello world")},
		Chunks:    streamChunks(),
	}}
}

func textRequest(sessionID string) *schema.RunRequest {
	return &schema.RunRequest{
		Messages:  []schema.Message{schema.NewUserMessage(testProtocol, "hi")},
		SessionID: sessionID,
	}
}

// drainStreamEvents consumes a RunStream to completion, returning every event
// and the terminal error (nil on a clean io.EOF close). Unlike the package's
// drainStream helper it returns both, so tests can assert on event order and
// on the error that surfaces after buffered events drain.
func drainStreamEvents(t *testing.T, rs *schema.RunStream) ([]schema.Event, error) {
	t.Helper()

	var events []schema.Event

	for {
		e, err := rs.Recv()
		if errors.Is(err, io.EOF) {
			return events, nil
		}

		if err != nil {
			return events, err
		}

		events = append(events, e)
	}
}

func lastAgentEnd(t *testing.T, events []schema.Event) schema.AgentEndData {
	t.Helper()

	for _, e := range slices.Backward(events) {
		if e.Type != schema.EventAgentEnd {
			continue
		}

		data, ok := e.Data.(schema.AgentEndData)
		if !ok {
			t.Fatalf("AgentEnd data type = %T", e.Data)
		}

		return data
	}

	t.Fatal("no AgentEnd event in stream")

	return schema.AgentEndData{}
}

func streamText(events []schema.Event) string {
	var sb strings.Builder

	for _, e := range events {
		if e.Type != schema.EventTextDelta {
			continue
		}

		if data, ok := e.Data.(schema.TextDeltaData); ok {
			sb.WriteString(data.Delta)
		}
	}

	return sb.String()
}

// replaceMiddleware rewrites the final response text after the run completes.
func replaceMiddleware(text string) agent.Middleware {
	return agent.MiddlewareFunc(func(next agent.RunFunc) agent.RunFunc {
		return func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
			resp, err := next(ctx, req)
			if err != nil {
				return nil, err
			}

			resp.Messages = []schema.Message{schema.NewTextMessage(testProtocol, schema.RoleAssistant, text)}

			return resp, nil
		}
	})
}

// shortCircuitMiddleware answers without ever calling next.
func shortCircuitMiddleware(text string) agent.Middleware {
	return agent.MiddlewareFunc(func(_ agent.RunFunc) agent.RunFunc {
		return func(_ context.Context, _ *schema.RunRequest) (*schema.RunResponse, error) {
			return &schema.RunResponse{
				Messages:   []schema.Message{schema.NewTextMessage(testProtocol, schema.RoleAssistant, text)},
				StopReason: schema.StopReasonComplete,
			}, nil
		}
	})
}

// --- one chain, both paths --------------------------------------------------

// TestMiddleware_RunsExactlyOncePerEntryPoint is the core claim of the unified
// chain: the very same middleware value fires once for Run and once for
// RunStream, and neither ReAct iterations nor tool calls multiply that.
func TestMiddleware_RunsExactlyOncePerEntryPoint(t *testing.T) {
	var calls atomic.Int64

	counter := agent.MiddlewareFunc(func(next agent.RunFunc) agent.RunFunc {
		return func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
			calls.Add(1)

			return next(ctx, req)
		}
	})

	t.Run("Run", func(t *testing.T) {
		calls.Store(0)

		a := New(agent.Config{ID: "mw"}, WithCaller(streamingMock()), WithMiddleware(counter))
		if _, err := a.Run(context.Background(), textRequest("sess-run")); err != nil {
			t.Fatalf("Run: %v", err)
		}

		if got := calls.Load(); got != 1 {
			t.Errorf("middleware calls = %d, want 1", got)
		}
	})

	t.Run("RunStream", func(t *testing.T) {
		calls.Store(0)

		a := New(agent.Config{ID: "mw"}, WithCaller(streamingMock()), WithMiddleware(counter))

		rs, err := a.RunStream(context.Background(), textRequest("sess-stream"))
		if err != nil {
			t.Fatalf("RunStream: %v", err)
		}

		if _, err := drainStreamEvents(t, rs); err != nil {
			t.Fatalf("drain: %v", err)
		}

		if got := calls.Load(); got != 1 {
			t.Errorf("middleware calls = %d, want 1", got)
		}
	})
}

// TestMiddleware_MultiIterationRunStillCallsChainOnce pins the "once per
// top-level call" wording against the loop that would most plausibly break it.
func TestMiddleware_MultiIterationRunStillCallsChainOnce(t *testing.T) {
	var calls atomic.Int64

	counter := agent.MiddlewareFunc(func(next agent.RunFunc) agent.RunFunc {
		return func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
			calls.Add(1)

			return next(ctx, req)
		}
	})

	mock := newMock(
		toolCallResponse("tc-1", "echo", `{"v":"a"}`),
		toolCallResponse("tc-2", "echo", `{"v":"b"}`),
		stopResponse("done"),
	)

	a := New(
		agent.Config{ID: "mw"},
		WithCaller(mock),
		WithToolRegistry(newEchoRegistry()),
		WithMiddleware(counter),
	)

	if _, err := a.Run(context.Background(), textRequest("sess-iter")); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if mock.Calls() != 3 {
		t.Fatalf("model calls = %d, want 3 (the loop must really have iterated)", mock.Calls())
	}

	if got := calls.Load(); got != 1 {
		t.Errorf("middleware calls = %d, want 1", got)
	}
}

// TestMiddleware_OrderIsIdenticalOnBothPaths asserts registration order for
// the pre phase, reverse order for the post phase, and that the sync and the
// streaming path produce byte-identical traces.
func TestMiddleware_OrderIsIdenticalOnBothPaths(t *testing.T) {
	trace := func(dst *[]string, name string) agent.Middleware {
		return agent.MiddlewareFunc(func(next agent.RunFunc) agent.RunFunc {
			return func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
				*dst = append(*dst, name+":pre")
				resp, err := next(ctx, req)
				*dst = append(*dst, name+":post")

				return resp, err
			}
		})
	}

	want := []string{"a:pre", "b:pre", "c:pre", "c:post", "b:post", "a:post"}

	var syncTrace []string

	a := New(agent.Config{ID: "mw"}, WithCaller(streamingMock()),
		WithMiddleware(trace(&syncTrace, "a"), trace(&syncTrace, "b")),
		WithMiddleware(trace(&syncTrace, "c")))

	if _, err := a.Run(context.Background(), textRequest("sess-1")); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !slices.Equal(syncTrace, want) {
		t.Errorf("Run trace = %v, want %v", syncTrace, want)
	}

	var streamTrace []string

	b := New(agent.Config{ID: "mw"}, WithCaller(streamingMock()),
		WithMiddleware(trace(&streamTrace, "a"), trace(&streamTrace, "b")),
		WithMiddleware(trace(&streamTrace, "c")))

	rs, err := b.RunStream(context.Background(), textRequest("sess-1"))
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}

	if _, err := drainStreamEvents(t, rs); err != nil {
		t.Fatalf("drain: %v", err)
	}

	if !slices.Equal(streamTrace, want) {
		t.Errorf("RunStream trace = %v, want %v", streamTrace, want)
	}
}

func TestMiddleware_NilEntriesAreIgnored(t *testing.T) {
	var calls atomic.Int64

	counter := agent.MiddlewareFunc(func(next agent.RunFunc) agent.RunFunc {
		return func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
			calls.Add(1)

			return next(ctx, req)
		}
	})

	a := New(agent.Config{ID: "mw"}, WithCaller(streamingMock()), WithMiddleware(nil, counter, nil))

	if len(a.runMiddlewares) != 1 {
		t.Fatalf("registered middlewares = %d, want 1", len(a.runMiddlewares))
	}

	if _, err := a.Run(context.Background(), textRequest("sess-nil")); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := calls.Load(); got != 1 {
		t.Errorf("middleware calls = %d, want 1", got)
	}
}

// --- short circuit ----------------------------------------------------------

// TestMiddleware_ShortCircuit_Run asserts a middleware that never calls next
// costs zero model calls, zero tool executions and zero checkpoint writes.
func TestMiddleware_ShortCircuit_Run(t *testing.T) {
	mock := streamingMock()
	store := checkpoint.NewMapIterationStore()

	var toolCalls atomic.Int64

	reg := tool.NewRegistry()
	if err := reg.Register(schema.ToolDef{Name: "echo"}, func(_ context.Context, _, _ string) (schema.ToolResult, error) {
		toolCalls.Add(1)

		return schema.TextResult("", "ok"), nil
	}); err != nil {
		t.Fatal(err)
	}

	a := New(agent.Config{ID: "mw"},
		WithCaller(mock),
		WithToolRegistry(reg),
		WithIterationStore(store),
		WithMiddleware(shortCircuitMiddleware("canned")))

	resp, err := a.Run(context.Background(), textRequest("sess-short"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := resp.Messages[0].Text(); got != "canned" {
		t.Errorf("response = %q, want %q", got, "canned")
	}
	if n := len(mock.Requests()); n != 0 {
		t.Errorf("model calls = %d, want 0", n)
	}
	if n := toolCalls.Load(); n != 0 {
		t.Errorf("tool executions = %d, want 0", n)
	}

	metas, err := store.List(context.Background(), "sess-short")
	if err != nil && !errors.Is(err, checkpoint.ErrCheckpointNotFound) {
		t.Fatalf("List: %v", err)
	}
	if len(metas) != 0 {
		t.Errorf("checkpoints = %d, want 0", len(metas))
	}
}

// TestMiddleware_ShortCircuit_RunStream asserts the streaming path turns the
// synthesised response into the terminal AgentEnd without ever reaching the
// model — so no TextDelta is emitted either.
func TestMiddleware_ShortCircuit_RunStream(t *testing.T) {
	mock := streamingMock()

	a := New(agent.Config{ID: "mw"},
		WithCaller(mock),
		WithMiddleware(shortCircuitMiddleware("canned")))

	rs, err := a.RunStream(context.Background(), textRequest("sess-short-stream"))
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}

	events, err := drainStreamEvents(t, rs)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}

	if n := len(mock.Requests()); n != 0 {
		t.Errorf("model calls = %d, want 0", n)
	}
	if got := streamText(events); got != "" {
		t.Errorf("stream text = %q, want empty", got)
	}
	if events[0].Type != schema.EventAgentStart {
		t.Errorf("events[0] = %q, want %q", events[0].Type, schema.EventAgentStart)
	}

	end := lastAgentEnd(t, events)
	if end.Message != "canned" {
		t.Errorf("AgentEnd.Message = %q, want %q", end.Message, "canned")
	}
	if end.StopReason != schema.StopReasonComplete {
		t.Errorf("AgentEnd.StopReason = %q, want complete", end.StopReason)
	}
}

// --- terminal rewrite -------------------------------------------------------

// TestMiddleware_RewriteFinalResponse_Run asserts the rewritten message is
// what the caller sees, what the AgentEnd hook reports and what lands in
// session memory — one source of truth for all three.
func TestMiddleware_RewriteFinalResponse_Run(t *testing.T) {
	session := memory.NewSessionMemory("mw", "sess-rw")
	mgr := memory.NewManager(memory.WithSession(session))

	hooks, ends := agentEndCollector()

	a := New(agent.Config{ID: "mw"},
		WithCaller(streamingMock()),
		WithMemory(mgr),
		WithHookManager(hooks),
		WithMiddleware(replaceMiddleware("rewritten")))

	resp, err := a.Run(context.Background(), textRequest("sess-rw"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := resp.Messages[0].Text(); got != "rewritten" {
		t.Errorf("response = %q, want %q", got, "rewritten")
	}

	if got := ends.last(t); got != "rewritten" {
		t.Errorf("AgentEnd.Message = %q, want %q", got, "rewritten")
	}

	if got := lastStoredText(t, session); got != "rewritten" {
		t.Errorf("stored message = %q, want %q", got, "rewritten")
	}
}

// TestMiddleware_RewriteFinalResponse_RunStream pins the documented trade-off:
// deltas already on the wire are never retracted, while the terminal AgentEnd
// and the persisted message both carry the rewrite.
func TestMiddleware_RewriteFinalResponse_RunStream(t *testing.T) {
	session := memory.NewSessionMemory("mw", "sess-rw-stream")
	mgr := memory.NewManager(memory.WithSession(session))

	a := New(agent.Config{ID: "mw"},
		WithCaller(streamingMock()),
		WithMemory(mgr),
		WithMiddleware(replaceMiddleware("rewritten")))

	rs, err := a.RunStream(context.Background(), textRequest("sess-rw-stream"))
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}

	events, err := drainStreamEvents(t, rs)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}

	if got := streamText(events); got != "Hello world" {
		t.Errorf("stream text = %q, want %q: deltas must not be replayed or retracted", got, "Hello world")
	}

	if got := lastAgentEnd(t, events).Message; got != "rewritten" {
		t.Errorf("AgentEnd.Message = %q, want %q", got, "rewritten")
	}

	if got := lastStoredText(t, session); got != "rewritten" {
		t.Errorf("stored message = %q, want %q", got, "rewritten")
	}
}

// TestMiddleware_RewriteStillPassesOutputGuards is the safety-boundary claim:
// a middleware cannot use its rewrite to route around the output guards.
func TestMiddleware_RewriteStillPassesOutputGuards(t *testing.T) {
	t.Run("Run", func(t *testing.T) {
		a := New(agent.Config{ID: "mw"},
			WithCaller(streamingMock()),
			WithOutputGuards(&testOutputGuard{rewriteTo: "guarded"}),
			WithMiddleware(replaceMiddleware("rewritten")))

		resp, err := a.Run(context.Background(), textRequest("sess-guard"))
		if err != nil {
			t.Fatalf("Run: %v", err)
		}

		if got := resp.Messages[0].Text(); got != "guarded" {
			t.Errorf("response = %q, want %q: the guard runs after the middleware", got, "guarded")
		}
	})

	t.Run("RunStream-shortCircuit", func(t *testing.T) {
		a := New(agent.Config{ID: "mw"},
			WithCaller(streamingMock()),
			WithOutputGuards(&testOutputGuard{rewriteTo: "guarded"}),
			WithMiddleware(shortCircuitMiddleware("canned")))

		rs, err := a.RunStream(context.Background(), textRequest("sess-guard-stream"))
		if err != nil {
			t.Fatalf("RunStream: %v", err)
		}

		events, err := drainStreamEvents(t, rs)
		if err != nil {
			t.Fatalf("drain: %v", err)
		}

		if got := lastAgentEnd(t, events).Message; got != "guarded" {
			t.Errorf("AgentEnd.Message = %q, want %q", got, "guarded")
		}
	})
}

// TestMiddleware_CannotForgeSessionIDOrDuration asserts the two framework
// invariants survive a middleware that deliberately lies about both.
func TestMiddleware_CannotForgeSessionIDOrDuration(t *testing.T) {
	liar := agent.MiddlewareFunc(func(next agent.RunFunc) agent.RunFunc {
		return func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
			resp, err := next(ctx, req)
			if err != nil {
				return nil, err
			}

			resp.SessionID = "other-session"
			resp.Duration = 999999

			return resp, nil
		}
	})

	a := New(agent.Config{ID: "mw"}, WithCaller(streamingMock()), WithMiddleware(liar))

	resp, err := a.Run(context.Background(), textRequest("sess-real"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if resp.SessionID != "sess-real" {
		t.Errorf("SessionID = %q, want %q", resp.SessionID, "sess-real")
	}
	if resp.Duration == 999999 {
		t.Error("Duration kept the forged value; the framework must stamp the measured one")
	}
}

// TestMiddleware_CanSetUsageMetadataAndStopReason covers the other half of the
// contract: what a middleware IS allowed to decide about the response.
func TestMiddleware_CanSetUsageMetadataAndStopReason(t *testing.T) {
	synth := agent.MiddlewareFunc(func(_ agent.RunFunc) agent.RunFunc {
		return func(_ context.Context, _ *schema.RunRequest) (*schema.RunResponse, error) {
			return &schema.RunResponse{
				Messages:   []schema.Message{schema.NewTextMessage(testProtocol, schema.RoleAssistant, "cached")},
				Metadata:   map[string]any{"source": "cache"},
				Usage:      &schema.Usage{TotalTokens: 0},
				StopReason: schema.StopReasonComplete,
			}, nil
		}
	})

	hooks, ends := agentEndCollector()

	a := New(agent.Config{ID: "mw"}, WithCaller(streamingMock()), WithHookManager(hooks), WithMiddleware(synth))

	resp, err := a.Run(context.Background(), textRequest("sess-meta"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if resp.Metadata["source"] != "cache" {
		t.Errorf("Metadata = %v, want source=cache", resp.Metadata)
	}
	if resp.StopReason != schema.StopReasonComplete {
		t.Errorf("StopReason = %q, want complete", resp.StopReason)
	}
	if got := ends.last(t); got != "cached" {
		t.Errorf("AgentEnd.Message = %q, want %q", got, "cached")
	}
}

// --- failure modes ----------------------------------------------------------

func TestMiddleware_NilResponseWithoutError(t *testing.T) {
	broken := agent.MiddlewareFunc(func(_ agent.RunFunc) agent.RunFunc {
		return func(_ context.Context, _ *schema.RunRequest) (*schema.RunResponse, error) {
			return nil, nil //nolint:nilnil // exercising the contract violation on purpose
		}
	})

	t.Run("Run", func(t *testing.T) {
		a := New(agent.Config{ID: "mw"}, WithCaller(streamingMock()), WithMiddleware(broken))

		if _, err := a.Run(context.Background(), textRequest("sess-nilresp")); !errors.Is(err, ErrNilMiddlewareResponse) {
			t.Fatalf("err = %v, want ErrNilMiddlewareResponse", err)
		}
	})

	t.Run("RunStream", func(t *testing.T) {
		a := New(agent.Config{ID: "mw"}, WithCaller(streamingMock()), WithMiddleware(broken))

		rs, err := a.RunStream(context.Background(), textRequest("sess-nilresp-stream"))
		if err != nil {
			t.Fatalf("RunStream: %v", err)
		}

		events, err := drainStreamEvents(t, rs)
		if !errors.Is(err, ErrNilMiddlewareResponse) {
			t.Fatalf("stream err = %v, want ErrNilMiddlewareResponse", err)
		}

		assertNoAgentEnd(t, events)
	})
}

// TestMiddleware_ErrorIsARunError asserts a middleware error terminates the
// run on both paths and never fakes a successful terminal event.
func TestMiddleware_ErrorIsARunError(t *testing.T) {
	sentinel := errors.New("policy denied")

	blocker := agent.MiddlewareFunc(func(_ agent.RunFunc) agent.RunFunc {
		return func(_ context.Context, _ *schema.RunRequest) (*schema.RunResponse, error) {
			return nil, sentinel
		}
	})

	t.Run("Run", func(t *testing.T) {
		hooks, ends := agentEndCollector()

		a := New(agent.Config{ID: "mw"},
			WithCaller(streamingMock()),
			WithHookManager(hooks),
			WithMiddleware(blocker))

		if _, err := a.Run(context.Background(), textRequest("sess-err")); !errors.Is(err, sentinel) {
			t.Fatalf("err = %v, want %v", err, sentinel)
		}

		ends.mu.Lock()
		n := len(ends.msgs)
		ends.mu.Unlock()

		if n != 0 {
			t.Errorf("AgentEnd dispatched %d times on a failed run, want 0", n)
		}
	})

	t.Run("RunStream", func(t *testing.T) {
		a := New(agent.Config{ID: "mw"}, WithCaller(streamingMock()), WithMiddleware(blocker))

		rs, err := a.RunStream(context.Background(), textRequest("sess-err-stream"))
		if err != nil {
			t.Fatalf("RunStream: %v", err)
		}

		events, err := drainStreamEvents(t, rs)
		if !errors.Is(err, sentinel) {
			t.Fatalf("stream err = %v, want %v", err, sentinel)
		}

		assertNoAgentEnd(t, events)
	})
}

// TestMiddleware_PostPhaseSeesLoopError asserts a failure inside the ReAct
// loop reaches the middleware's post phase as an error rather than as a
// response it might mistake for success.
func TestMiddleware_PostPhaseSeesLoopError(t *testing.T) {
	sentinel := errors.New("upstream down")

	var (
		sawErr  error
		sawResp *schema.RunResponse
	)

	observer := agent.MiddlewareFunc(func(next agent.RunFunc) agent.RunFunc {
		return func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
			sawResp, sawErr = next(ctx, req)

			return sawResp, sawErr
		}
	})

	a := New(agent.Config{ID: "mw"}, WithCaller(newMockErr(sentinel)), WithMiddleware(observer))

	if _, err := a.Run(context.Background(), textRequest("sess-loop-err")); !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want %v", err, sentinel)
	}

	if !errors.Is(sawErr, sentinel) {
		t.Errorf("middleware saw err = %v, want %v", sawErr, sentinel)
	}
	if sawResp != nil {
		t.Errorf("middleware saw resp = %v, want nil alongside the error", sawResp)
	}
}

// TestMiddleware_StopReasonRewriteDoesNotMaskBudgetExhaustion pins the
// framework-owned side of StopReason: a middleware may change what the
// response reports, but the token-budget event and the guard leniency follow
// what the ReAct loop really did, so a rewrite cannot hide a budget run-out.
func TestMiddleware_StopReasonRewriteDoesNotMaskBudgetExhaustion(t *testing.T) {
	mock := newMock(toolCallResponseWithUsage("tc-1", "echo", "{}", 500))

	var budgetEvents atomic.Int64

	mgr := hook.NewManager()
	mgr.Register(hook.NewHookFunc(func(_ context.Context, e schema.Event) error {
		if e.Type == schema.EventTokenBudgetExhausted {
			budgetEvents.Add(1)
		}

		return nil
	}))

	liar := agent.MiddlewareFunc(func(next agent.RunFunc) agent.RunFunc {
		return func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
			resp, err := next(ctx, req)
			if err != nil {
				return nil, err
			}

			resp.StopReason = schema.StopReasonComplete

			return resp, nil
		}
	})

	a := New(agent.Config{ID: "mw"},
		WithCaller(mock),
		WithRunTokenBudget(100),
		WithToolRegistry(newEchoRegistry()),
		WithHookManager(mgr),
		WithMiddleware(liar))

	resp, err := a.Run(context.Background(), textRequest("sess-budget"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// The middleware's claim wins on the response envelope...
	if resp.StopReason != schema.StopReasonComplete {
		t.Errorf("StopReason = %q, want %q (the middleware decided)", resp.StopReason, schema.StopReasonComplete)
	}

	// ...but the framework's own accounting does not follow the lie.
	if n := budgetEvents.Load(); n != 1 {
		t.Errorf("EventTokenBudgetExhausted dispatched %d times, want 1", n)
	}

	// The loop genuinely ran out of budget, so the partial-response rule still
	// applies: the tool-call assistant message is present even though the
	// response claims a clean completion.
	if len(resp.Messages) == 0 {
		t.Error("expected the partial tool-call message to survive a forged stop reason")
	}
}

// TestMiddleware_PostNextErrorOnStreamDrainsDeltasFirst pins the stream error
// contract for a middleware that fails AFTER calling next: deltas already sent
// during the run are delivered to the consumer before the error surfaces, and
// no AgentEnd is fabricated for the failed run.
func TestMiddleware_PostNextErrorOnStreamDrainsDeltasFirst(t *testing.T) {
	sentinel := errors.New("post-process failed")

	broken := agent.MiddlewareFunc(func(next agent.RunFunc) agent.RunFunc {
		return func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
			if _, err := next(ctx, req); err != nil {
				return nil, err
			}

			return nil, sentinel
		}
	})

	a := New(agent.Config{ID: "mw"},
		WithCaller(streamingMock()),
		WithMiddleware(broken))

	rs, err := a.RunStream(context.Background(), textRequest("sess-posterr"))
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}

	events, err := drainStreamEvents(t, rs)
	if !errors.Is(err, sentinel) {
		t.Fatalf("stream err = %v, want %v", err, sentinel)
	}

	// Deltas emitted during the run reach the consumer before the error.
	if got := streamText(events); got != "Hello world" {
		t.Errorf("stream text = %q, want %q: emitted deltas must drain before the error", got, "Hello world")
	}

	// A post-next failure is still a run failure: no success terminal event.
	assertNoAgentEnd(t, events)
}

// --- boundaries -------------------------------------------------------------

// TestMiddleware_ResumeSkipsChain pins the documented carve-out: a resumed run
// finishes the work the checkpoint recorded and does not re-enter the chain.
func TestMiddleware_ResumeSkipsChain(t *testing.T) {
	store := checkpoint.NewMapIterationStore()

	var calls atomic.Int64

	counter := agent.MiddlewareFunc(func(next agent.RunFunc) agent.RunFunc {
		return func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
			calls.Add(1)

			return next(ctx, req)
		}
	})

	// First run stops mid-loop by running out of scripted responses, leaving a
	// non-final checkpoint behind.
	first := New(agent.Config{ID: "mw"},
		WithCaller(newMock(toolCallResponse("tc-1", "echo", `{"v":"a"}`))),
		WithToolRegistry(newEchoRegistry()),
		WithIterationStore(store),
		WithMiddleware(counter))

	if _, err := first.Run(context.Background(), textRequest("sess-resume")); err == nil {
		t.Fatal("expected the scripted caller to run out of responses")
	}

	if got := calls.Load(); got != 1 {
		t.Fatalf("middleware calls after Run = %d, want 1", got)
	}

	second := New(agent.Config{ID: "mw"},
		WithCaller(newMock(stopResponse("resumed"))),
		WithToolRegistry(newEchoRegistry()),
		WithIterationStore(store),
		WithMiddleware(counter))

	resp, err := second.Resume(context.Background(), "sess-resume")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}

	if got := resp.Messages[0].Text(); got != "resumed" {
		t.Errorf("resumed response = %q, want %q", got, "resumed")
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("middleware calls after Resume = %d, want 1 (Resume must not re-enter the chain)", got)
	}
}

// TestMiddleware_ConcurrentRunsShareOneInstance documents the concurrency
// contract: one middleware value serves parallel runs, so it must be safe for
// concurrent use. Meaningful under -race.
func TestMiddleware_ConcurrentRunsShareOneInstance(t *testing.T) {
	var calls atomic.Int64

	shared := agent.MiddlewareFunc(func(next agent.RunFunc) agent.RunFunc {
		return func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
			calls.Add(1)

			return next(ctx, req)
		}
	})

	a := New(agent.Config{ID: "mw"},
		WithCaller(&mockCaller{FakeCaller: &largemodel.FakeCaller{
			Responses: slices.Repeat([]*largemodel.Response{stopResponse("hi")}, 16),
			Chunks:    streamChunks(),
		}}),
		WithMiddleware(shared))

	var wg sync.WaitGroup
	errs := make(chan error, 8)

	for i := range 8 {
		wg.Add(1)

		go func(i int) {
			defer wg.Done()

			if i%2 == 0 {
				if _, err := a.Run(context.Background(), textRequest("sess-conc")); err != nil {
					errs <- err
				}

				return
			}

			rs, err := a.RunStream(context.Background(), textRequest("sess-conc"))
			if err != nil {
				errs <- err

				return
			}

			// Reuse the package drainStream: it fails the test on error, which
			// is a t.Fatal from a spawned goroutine and therefore a misuse —
			// so collect errors here instead.
			for {
				if _, err := rs.Recv(); err != nil {
					if errors.Is(err, io.EOF) {
						break
					}

					errs <- err

					return
				}
			}
		}(i)
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent run: %v", err)
	}

	if got := calls.Load(); got != 8 {
		t.Errorf("middleware calls = %d, want 8", got)
	}
}

// TestMiddleware_AbsentChainKeepsBaselineBehaviour guards against the refactor
// having shifted the no-middleware response or event sequence.
func TestMiddleware_AbsentChainKeepsBaselineBehaviour(t *testing.T) {
	a := New(agent.Config{ID: "mw"}, WithCaller(streamingMock()))

	resp, err := a.Run(context.Background(), textRequest("sess-base"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := resp.Messages[0].Text(); got != "Hello world" {
		t.Errorf("response = %q, want %q", got, "Hello world")
	}
	if resp.SessionID != "sess-base" {
		t.Errorf("SessionID = %q, want %q", resp.SessionID, "sess-base")
	}
	if resp.StopReason != schema.StopReasonComplete {
		t.Errorf("StopReason = %q, want complete", resp.StopReason)
	}

	b := New(agent.Config{ID: "mw"}, WithCaller(streamingMock()))

	rs, err := b.RunStream(context.Background(), textRequest("sess-base"))
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}

	events, err := drainStreamEvents(t, rs)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}

	wantTypes := []string{
		schema.EventAgentStart,
		schema.EventIterationStart,
		schema.EventTextDelta,
		schema.EventTextDelta,
		schema.EventLLMCallEnd,
		schema.EventAgentEnd,
	}

	var gotTypes []string
	for _, e := range events {
		gotTypes = append(gotTypes, e.Type)
	}

	if !slices.Equal(gotTypes, wantTypes) {
		t.Errorf("event types = %v, want %v", gotTypes, wantTypes)
	}
}

// --- small local helpers ----------------------------------------------------

func assertNoAgentEnd(t *testing.T, events []schema.Event) {
	t.Helper()

	for _, e := range events {
		if e.Type == schema.EventAgentEnd {
			t.Error("AgentEnd emitted on a failed run")
		}
	}
}

// lastStoredText reads back the highest-keyed message the run promoted to
// session memory, i.e. the assistant answer the framework persisted.
func lastStoredText(t *testing.T, session memory.Memory) string {
	t.Helper()

	entries, err := session.List(context.Background(), "msg:")
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(entries) == 0 {
		t.Fatal("no messages promoted to session memory")
	}

	slices.SortFunc(entries, func(a, b memory.Entry) int { return strings.Compare(a.Key, b.Key) })

	msg, ok := entries[len(entries)-1].Value.(schema.Message)
	if !ok {
		t.Fatalf("stored value type = %T, want schema.Message", entries[len(entries)-1].Value)
	}

	return msg.Text()
}

// agentEndCollector wires a hook.Manager whose sync hook records every
// AgentEnd message the framework dispatches. It returns the manager for
// WithHookManager and the recorder to assert against.
func agentEndCollector() (*hook.Manager, *agentEndRecorder) {
	r := &agentEndRecorder{}

	mgr := hook.NewManager()
	mgr.Register(hook.NewHookFunc(func(_ context.Context, e schema.Event) error {
		if e.Type != schema.EventAgentEnd {
			return nil
		}

		data, ok := e.Data.(schema.AgentEndData)
		if !ok {
			return nil
		}

		r.mu.Lock()
		defer r.mu.Unlock()

		r.msgs = append(r.msgs, data.Message)

		return nil
	}))

	return mgr, r
}

// agentEndRecorder captures AgentEnd messages seen by the hook pipeline.
type agentEndRecorder struct {
	mu   sync.Mutex
	msgs []string
}

func (r *agentEndRecorder) last(t *testing.T) string {
	t.Helper()

	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.msgs) == 0 {
		t.Fatal("no AgentEnd dispatched to hooks")
	}

	return r.msgs[len(r.msgs)-1]
}
