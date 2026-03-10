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

package routeragent_tests

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/vogo/aimodel"
	"github.com/vogo/vagent/agent"
	"github.com/vogo/vagent/agent/routeragent"
	"github.com/vogo/vagent/schema"
)

// --- Integration test helpers (black-box, external package) ---

func newTextAgent(id, suffix string) agent.Agent {
	return agent.NewCustomAgent(agent.Config{ID: id}, func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		text := ""
		if len(req.Messages) > 0 {
			text = req.Messages[0].Content.Text()
		}
		return &schema.RunResponse{
			Messages: []schema.Message{schema.NewUserMessage(text + suffix)},
		}, nil
	})
}

func newUsageAgent(id string, prompt, completion, total int) agent.Agent {
	return agent.NewCustomAgent(agent.Config{ID: id}, func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		return &schema.RunResponse{
			Messages: req.Messages,
			Usage:    &aimodel.Usage{PromptTokens: prompt, CompletionTokens: completion, TotalTokens: total},
		}, nil
	})
}

func newFailAgent(id string, err error) agent.Agent {
	return agent.NewCustomAgent(agent.Config{ID: id}, func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		return nil, err
	})
}

func newNilRespAgent(id string) agent.Agent {
	return agent.NewCustomAgent(agent.Config{ID: id}, func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		return nil, nil
	})
}

// Routing functions for error/nil integration tests (not covered by built-in route funcs).
func routeError(_ context.Context, _ *schema.RunRequest, _ []routeragent.Route) (*routeragent.RouteResult, error) {
	return nil, errors.New("routing failed")
}

func routeNil(_ context.Context, _ *schema.RunRequest, _ []routeragent.Route) (*routeragent.RouteResult, error) {
	return nil, nil
}

// --- mockChatCompleter for LLM integration tests ---

type mockChatCompleter struct {
	response *aimodel.ChatResponse
	err      error
	captured *aimodel.ChatRequest
}

func (m *mockChatCompleter) ChatCompletion(_ context.Context, req *aimodel.ChatRequest) (*aimodel.ChatResponse, error) {
	m.captured = req
	if m.err != nil {
		return nil, m.err
	}
	return m.response, nil
}

func (m *mockChatCompleter) ChatCompletionStream(_ context.Context, _ *aimodel.ChatRequest) (*aimodel.Stream, error) {
	return nil, errors.New("not implemented")
}

func llmResponse(text string, prompt, completion, total int) *aimodel.ChatResponse {
	return &aimodel.ChatResponse{
		Choices: []aimodel.Choice{{
			Message:      aimodel.Message{Role: aimodel.RoleAssistant, Content: aimodel.NewTextContent(text)},
			FinishReason: aimodel.FinishReasonStop,
		}},
		Usage: aimodel.Usage{PromptTokens: prompt, CompletionTokens: completion, TotalTokens: total},
	}
}

// --- Integration Tests ---

// TestIntegration_RoutingAndDelegation_EndToEnd verifies the full routing pipeline:
// routing function selects the correct agent, delegation works, response fields are correct.
func TestIntegration_RoutingAndDelegation_EndToEnd(t *testing.T) {
	routes := []routeragent.Route{
		{Agent: newTextAgent("agent-a", "-A"), Description: "Agent A"},
		{Agent: newTextAgent("agent-b", "-B"), Description: "Agent B"},
		{Agent: newTextAgent("agent-c", "-C"), Description: "Agent C"},
	}

	// Route to agent-c (index 2)
	ra := routeragent.New(agent.Config{ID: "router-e2e", Name: "Router", Description: "e2e test"},
		routes, routeragent.WithFunc(routeragent.IndexFunc(2)))

	req := &schema.RunRequest{
		Messages:  []schema.Message{schema.NewUserMessage("hello")},
		SessionID: "session-e2e",
	}
	resp, err := ra.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify correct agent was selected
	got := resp.Messages[0].Content.Text()
	if got != "hello-C" {
		t.Errorf("routed output = %q, want %q", got, "hello-C")
	}

	// Verify session ID preserved
	if resp.SessionID != "session-e2e" {
		t.Errorf("SessionID = %q, want %q", resp.SessionID, "session-e2e")
	}

	// Verify duration is non-negative
	if resp.Duration < 0 {
		t.Errorf("Duration = %d, want >= 0", resp.Duration)
	}
}

// TestIntegration_SelectsFirstRoute verifies routing to the first agent among multiple routes.
func TestIntegration_SelectsFirstRoute(t *testing.T) {
	routes := []routeragent.Route{
		{Agent: newTextAgent("first", "-FIRST"), Description: "first"},
		{Agent: newTextAgent("second", "-SECOND"), Description: "second"},
	}
	ra := routeragent.New(agent.Config{ID: "rt"}, routes, routeragent.WithFunc(routeragent.FirstFunc))

	resp, err := ra.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("input")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := resp.Messages[0].Content.Text(); got != "input-FIRST" {
		t.Errorf("got %q, want %q", got, "input-FIRST")
	}
}

// TestIntegration_EmptyRoutes verifies that an empty routes slice produces an error.
func TestIntegration_EmptyRoutes(t *testing.T) {
	ra := routeragent.New(agent.Config{ID: "rt"}, nil, routeragent.WithFunc(routeragent.FirstFunc))
	_, err := ra.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("test")},
	})
	if err == nil {
		t.Fatal("expected error for empty routes")
	}
	if !strings.Contains(err.Error(), "no routes") {
		t.Errorf("error = %q, want containing 'no routes'", err.Error())
	}
}

// TestIntegration_NilRouteFunc verifies that a nil routing function produces an error.
func TestIntegration_NilRouteFunc(t *testing.T) {
	routes := []routeragent.Route{
		{Agent: newTextAgent("sub", ""), Description: "route"},
	}
	ra := routeragent.New(agent.Config{ID: "rt"}, routes) // no WithFunc
	_, err := ra.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("test")},
	})
	if err == nil {
		t.Fatal("expected error for nil routeFunc")
	}
	if !strings.Contains(err.Error(), "no routing function") {
		t.Errorf("error = %q, want containing 'no routing function'", err.Error())
	}
}

// TestIntegration_RouteFuncError verifies that an error from the routing function
// is wrapped with "route select" context.
func TestIntegration_RouteFuncError(t *testing.T) {
	routes := []routeragent.Route{
		{Agent: newTextAgent("sub", ""), Description: "route"},
	}
	ra := routeragent.New(agent.Config{ID: "rt"}, routes, routeragent.WithFunc(routeError))
	_, err := ra.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("test")},
	})
	if err == nil {
		t.Fatal("expected error from routeFunc")
	}
	if !strings.Contains(err.Error(), "route select") {
		t.Errorf("error = %q, want containing 'route select'", err.Error())
	}
	if !strings.Contains(err.Error(), "routing failed") {
		t.Errorf("error = %q, want containing 'routing failed'", err.Error())
	}
}

// TestIntegration_RouteFuncReturnsNilAgent verifies that a nil agent from the
// routing function produces a descriptive error.
func TestIntegration_RouteFuncReturnsNilAgent(t *testing.T) {
	routes := []routeragent.Route{
		{Agent: newTextAgent("sub", ""), Description: "route"},
	}
	ra := routeragent.New(agent.Config{ID: "rt"}, routes, routeragent.WithFunc(routeNil))
	_, err := ra.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("test")},
	})
	if err == nil {
		t.Fatal("expected error for nil agent")
	}
	if !strings.Contains(err.Error(), "returned nil agent") {
		t.Errorf("error = %q, want containing 'returned nil agent'", err.Error())
	}
}

// TestIntegration_SelectedAgentError verifies that an error from the selected
// agent is propagated directly.
func TestIntegration_SelectedAgentError(t *testing.T) {
	routes := []routeragent.Route{
		{Agent: newFailAgent("fail-sub", errors.New("sub-agent boom")), Description: "route"},
	}
	ra := routeragent.New(agent.Config{ID: "rt"}, routes, routeragent.WithFunc(routeragent.FirstFunc))
	_, err := ra.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("test")},
	})
	if err == nil {
		t.Fatal("expected error from selected agent")
	}
	if !strings.Contains(err.Error(), "sub-agent boom") {
		t.Errorf("error = %q, want containing 'sub-agent boom'", err.Error())
	}
}

// TestIntegration_NilResponseError verifies that a selected agent returning (nil, nil)
// produces a descriptive error.
func TestIntegration_NilResponseError(t *testing.T) {
	routes := []routeragent.Route{
		{Agent: newNilRespAgent("nil-sub"), Description: "route"},
	}
	ra := routeragent.New(agent.Config{ID: "rt"}, routes, routeragent.WithFunc(routeragent.FirstFunc))
	_, err := ra.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("test")},
	})
	if err == nil {
		t.Fatal("expected error for nil response")
	}
	if !strings.Contains(err.Error(), "nil response") {
		t.Errorf("error = %q, want containing 'nil response'", err.Error())
	}
}

// TestIntegration_ContextCancellation verifies that a cancelled context before Run()
// returns context.Canceled without invoking the routing function.
func TestIntegration_ContextCancellation(t *testing.T) {
	routerCalled := false
	routerFn := func(_ context.Context, _ *schema.RunRequest, routes []routeragent.Route) (*routeragent.RouteResult, error) {
		routerCalled = true
		return &routeragent.RouteResult{Agent: routes[0].Agent}, nil
	}

	routes := []routeragent.Route{
		{Agent: newTextAgent("sub", ""), Description: "route"},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before Run

	ra := routeragent.New(agent.Config{ID: "rt"}, routes, routeragent.WithFunc(routerFn))
	_, err := ra.Run(ctx, &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("test")},
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got: %v", err)
	}
	if routerCalled {
		t.Error("routing function should not have been called after context cancellation")
	}
}

// TestIntegration_ContextDeadlineExceeded verifies context.DeadlineExceeded behavior.
func TestIntegration_ContextDeadlineExceeded(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()

	slowAgent := agent.NewCustomAgent(agent.Config{ID: "slow"}, func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		time.Sleep(50 * time.Millisecond) // exceed the deadline
		return &schema.RunResponse{Messages: req.Messages}, nil
	})
	routes := []routeragent.Route{
		{Agent: slowAgent, Description: "slow route"},
	}
	// Need to wait for the deadline to pass before Run is called
	time.Sleep(5 * time.Millisecond)
	ra := routeragent.New(agent.Config{ID: "rt"}, routes, routeragent.WithFunc(routeragent.FirstFunc))
	_, err := ra.Run(ctx, &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("test")},
	})
	if err == nil {
		t.Fatal("expected error due to deadline exceeded")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected context.DeadlineExceeded, got: %v", err)
	}
}

// TestIntegration_SessionIDPreserved verifies that the request SessionID
// is preserved in the response (not overwritten by the sub-agent).
func TestIntegration_SessionIDPreserved(t *testing.T) {
	routes := []routeragent.Route{
		{Agent: newTextAgent("sub", ""), Description: "route"},
	}
	ra := routeragent.New(agent.Config{ID: "rt"}, routes, routeragent.WithFunc(routeragent.FirstFunc))
	resp, err := ra.Run(context.Background(), &schema.RunRequest{
		Messages:  []schema.Message{schema.NewUserMessage("hello")},
		SessionID: "sess-42",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.SessionID != "sess-42" {
		t.Errorf("SessionID = %q, want %q", resp.SessionID, "sess-42")
	}
}

// TestIntegration_UsagePropagation verifies that usage from the selected agent
// is propagated to the response.
func TestIntegration_UsagePropagation(t *testing.T) {
	routes := []routeragent.Route{
		{Agent: newUsageAgent("sub", 10, 20, 30), Description: "route"},
	}
	ra := routeragent.New(agent.Config{ID: "rt"}, routes, routeragent.WithFunc(routeragent.FirstFunc))
	resp, err := ra.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("hello")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Usage == nil {
		t.Fatal("expected non-nil Usage")
	}
	if resp.Usage.PromptTokens != 10 {
		t.Errorf("PromptTokens = %d, want 10", resp.Usage.PromptTokens)
	}
	if resp.Usage.CompletionTokens != 20 {
		t.Errorf("CompletionTokens = %d, want 20", resp.Usage.CompletionTokens)
	}
	if resp.Usage.TotalTokens != 30 {
		t.Errorf("TotalTokens = %d, want 30", resp.Usage.TotalTokens)
	}
}

// TestIntegration_RequestPassthrough verifies that the selected agent receives
// the original request with Options and Metadata intact.
func TestIntegration_RequestPassthrough(t *testing.T) {
	var captured *schema.RunRequest
	sub := agent.NewCustomAgent(agent.Config{ID: "capture"}, func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		captured = req
		return &schema.RunResponse{Messages: req.Messages}, nil
	})
	routes := []routeragent.Route{{Agent: sub, Description: "route"}}
	ra := routeragent.New(agent.Config{ID: "rt"}, routes, routeragent.WithFunc(routeragent.FirstFunc))

	temp := 0.7
	opts := &schema.RunOptions{Model: "gpt-test", Temperature: &temp, MaxTokens: 100}
	meta := map[string]any{"key": "value", "num": 42}
	req := &schema.RunRequest{
		Messages:  []schema.Message{schema.NewUserMessage("hello")},
		SessionID: "sess-passthrough",
		Options:   opts,
		Metadata:  meta,
	}

	_, err := ra.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if captured != req {
		t.Error("selected agent did not receive the original request pointer")
	}
	if captured.Options == nil || captured.Options.Model != "gpt-test" {
		t.Errorf("Options.Model = %v, want 'gpt-test'", captured.Options)
	}
	if captured.Options.MaxTokens != 100 {
		t.Errorf("Options.MaxTokens = %d, want 100", captured.Options.MaxTokens)
	}
	if captured.Metadata == nil || captured.Metadata["key"] != "value" {
		t.Errorf("Metadata missing expected key, got %v", captured.Metadata)
	}
}

// TestIntegration_MetadataPreservedFromSelectedAgent verifies that metadata
// set by the selected agent is available in the final response.
func TestIntegration_MetadataPreservedFromSelectedAgent(t *testing.T) {
	sub := agent.NewCustomAgent(agent.Config{ID: "meta-agent"}, func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		return &schema.RunResponse{
			Messages: req.Messages,
			Metadata: map[string]any{"result_key": "result_value"},
		}, nil
	})
	routes := []routeragent.Route{{Agent: sub, Description: "route"}}
	ra := routeragent.New(agent.Config{ID: "rt"}, routes, routeragent.WithFunc(routeragent.FirstFunc))

	resp, err := ra.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("test")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Metadata == nil || resp.Metadata["result_key"] != "result_value" {
		t.Errorf("expected metadata from selected agent, got %v", resp.Metadata)
	}
}

// TestIntegration_AgentInterfaceCompliance verifies that *routeragent.Agent
// satisfies both agent.Agent and agent.StreamAgent interfaces.
func TestIntegration_AgentInterfaceCompliance(t *testing.T) {
	ra := routeragent.New(agent.Config{ID: "iface-test", Name: "Router", Description: "compliance"}, nil)

	var a agent.Agent = ra
	if a.ID() != "iface-test" {
		t.Errorf("ID = %q, want %q", a.ID(), "iface-test")
	}
	if a.Name() != "Router" {
		t.Errorf("Name = %q, want %q", a.Name(), "Router")
	}
	if a.Description() != "compliance" {
		t.Errorf("Description = %q, want %q", a.Description(), "compliance")
	}

	var sa agent.StreamAgent = ra
	_ = sa
}

// TestIntegration_RunStream_FullLifecycle verifies that RunStream emits
// AgentStart then AgentEnd events with correct data, then EOF.
func TestIntegration_RunStream_FullLifecycle(t *testing.T) {
	routes := []routeragent.Route{
		{Agent: newTextAgent("sub", "-streamed"), Description: "route"},
	}
	ra := routeragent.New(agent.Config{ID: "stream-router"}, routes, routeragent.WithFunc(routeragent.FirstFunc))

	stream, err := ra.RunStream(context.Background(), &schema.RunRequest{
		Messages:  []schema.Message{schema.NewUserMessage("hello")},
		SessionID: "stream-sess",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = stream.Close() }()

	// Event 1: AgentStart
	e, err := stream.Recv()
	if err != nil {
		t.Fatalf("recv AgentStart error: %v", err)
	}
	if e.Type != schema.EventAgentStart {
		t.Errorf("event type = %q, want %q", e.Type, schema.EventAgentStart)
	}
	if e.AgentID != "stream-router" {
		t.Errorf("AgentID = %q, want %q", e.AgentID, "stream-router")
	}
	if e.SessionID != "stream-sess" {
		t.Errorf("SessionID = %q, want %q", e.SessionID, "stream-sess")
	}

	// Event 2: AgentEnd
	e, err = stream.Recv()
	if err != nil {
		t.Fatalf("recv AgentEnd error: %v", err)
	}
	if e.Type != schema.EventAgentEnd {
		t.Errorf("event type = %q, want %q", e.Type, schema.EventAgentEnd)
	}
	endData, ok := e.Data.(schema.AgentEndData)
	if !ok {
		t.Fatal("expected AgentEndData type")
	}
	if endData.Message != "hello-streamed" {
		t.Errorf("AgentEnd message = %q, want %q", endData.Message, "hello-streamed")
	}
	if endData.Duration < 0 {
		t.Errorf("AgentEnd duration = %d, want >= 0", endData.Duration)
	}

	// Event 3: EOF
	_, err = stream.Recv()
	if !errors.Is(err, io.EOF) {
		t.Errorf("expected io.EOF, got: %v", err)
	}
}

// TestIntegration_RunStream_ErrorSurfaced verifies that an agent error
// during streaming is surfaced through Recv after the AgentStart event.
func TestIntegration_RunStream_ErrorSurfaced(t *testing.T) {
	routes := []routeragent.Route{
		{Agent: newFailAgent("fail-sub", errors.New("integration stream failure")), Description: "route"},
	}
	ra := routeragent.New(agent.Config{ID: "err-stream"}, routes, routeragent.WithFunc(routeragent.FirstFunc))

	stream, err := ra.RunStream(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("test")},
	})
	if err != nil {
		t.Fatalf("unexpected error creating stream: %v", err)
	}
	defer func() { _ = stream.Close() }()

	// AgentStart should still be emitted
	e, err := stream.Recv()
	if err != nil {
		t.Fatalf("recv error: %v", err)
	}
	if e.Type != schema.EventAgentStart {
		t.Errorf("expected AgentStart, got %s", e.Type)
	}

	// Next recv should return the error
	_, err = stream.Recv()
	if err == nil {
		t.Fatal("expected error from stream")
	}
	if !strings.Contains(err.Error(), "integration stream failure") {
		t.Errorf("error = %q, want containing 'integration stream failure'", err.Error())
	}
}

// TestIntegration_RunStream_Close verifies that closing a stream early
// prevents further events.
func TestIntegration_RunStream_Close(t *testing.T) {
	slowAgent := agent.NewCustomAgent(agent.Config{ID: "slow"}, func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	routes := []routeragent.Route{{Agent: slowAgent, Description: "slow route"}}
	ra := routeragent.New(agent.Config{ID: "close-rt"}, routes, routeragent.WithFunc(routeragent.FirstFunc))

	stream, closeErr := ra.RunStream(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("test")},
	})
	if closeErr != nil {
		t.Fatalf("unexpected error: %v", closeErr)
	}

	// Read the AgentStart event
	e, recvErr := stream.Recv()
	if recvErr != nil {
		t.Fatalf("recv error: %v", recvErr)
	}
	if e.Type != schema.EventAgentStart {
		t.Errorf("expected AgentStart, got %s", e.Type)
	}

	// Close the stream
	if closeErr = stream.Close(); closeErr != nil {
		t.Fatalf("close error: %v", closeErr)
	}

	// Subsequent Recv should return an error (ErrRunStreamClosed or EOF)
	_, recvErr = stream.Recv()
	if !errors.Is(recvErr, schema.ErrRunStreamClosed) {
		if recvErr == nil {
			t.Error("expected error after Close, got nil")
		}
	}
}

// TestIntegration_RunText_Convenience verifies the agent.RunText convenience function
// works with a router agent.
func TestIntegration_RunText_Convenience(t *testing.T) {
	routes := []routeragent.Route{
		{Agent: newTextAgent("sub", "-via-RunText"), Description: "route"},
	}
	ra := routeragent.New(agent.Config{ID: "conv-rt"}, routes, routeragent.WithFunc(routeragent.FirstFunc))

	resp, err := agent.RunText(context.Background(), ra, "hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := resp.Messages[0].Content.Text()
	if got != "hello-via-RunText" {
		t.Errorf("got %q, want %q", got, "hello-via-RunText")
	}
}

// TestIntegration_RunStreamText_Convenience verifies the agent.RunStreamText convenience
// function works with a router agent.
func TestIntegration_RunStreamText_Convenience(t *testing.T) {
	routes := []routeragent.Route{
		{Agent: newTextAgent("sub", "-via-stream"), Description: "route"},
	}
	ra := routeragent.New(agent.Config{ID: "stream-conv-rt"}, routes, routeragent.WithFunc(routeragent.FirstFunc))

	stream, err := agent.RunStreamText(context.Background(), ra, "world")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = stream.Close() }()

	var events []schema.Event
	for {
		e, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("recv error: %v", err)
		}
		events = append(events, e)
	}

	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].Type != schema.EventAgentStart {
		t.Errorf("event[0] type = %q, want AgentStart", events[0].Type)
	}
	if events[1].Type != schema.EventAgentEnd {
		t.Errorf("event[1] type = %q, want AgentEnd", events[1].Type)
	}
	endData, ok := events[1].Data.(schema.AgentEndData)
	if !ok {
		t.Fatal("expected AgentEndData")
	}
	if endData.Message != "world-via-stream" {
		t.Errorf("message = %q, want %q", endData.Message, "world-via-stream")
	}
}

// TestIntegration_ContentBasedRouting verifies KeywordFunc inspects
// message content to select the appropriate agent.
func TestIntegration_ContentBasedRouting(t *testing.T) {
	routes := []routeragent.Route{
		{Agent: newTextAgent("math-agent", " [math]"), Description: "math"},
		{Agent: newTextAgent("code-agent", " [code]"), Description: "code"},
		{Agent: newTextAgent("chat-agent", " [chat]"), Description: "chat"},
	}
	ra := routeragent.New(agent.Config{ID: "content-rt"}, routes, routeragent.WithFunc(routeragent.KeywordFunc(-1)))

	tests := []struct {
		input string
		want  string
	}{
		{"solve math problem", "solve math problem [math]"},
		{"write code for me", "write code for me [code]"},
		{"let's chat about weather", "let's chat about weather [chat]"},
	}

	for _, tt := range tests {
		resp, err := ra.Run(context.Background(), &schema.RunRequest{
			Messages: []schema.Message{schema.NewUserMessage(tt.input)},
		})
		if err != nil {
			t.Fatalf("input=%q: unexpected error: %v", tt.input, err)
		}
		got := resp.Messages[0].Content.Text()
		if got != tt.want {
			t.Errorf("input=%q: got %q, want %q", tt.input, got, tt.want)
		}
	}

	// No matching route
	_, err := ra.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("unrelated topic")},
	})
	if err == nil {
		t.Fatal("expected error for no matching route")
	}
	if !strings.Contains(err.Error(), "no route matched") {
		t.Errorf("error = %q, want containing 'no route matched'", err.Error())
	}
}

// TestIntegration_ContentBasedRouting_WithFallback verifies KeywordFunc fallback behavior.
func TestIntegration_ContentBasedRouting_WithFallback(t *testing.T) {
	routes := []routeragent.Route{
		{Agent: newTextAgent("math-agent", " [math]"), Description: "math"},
		{Agent: newTextAgent("chat-agent", " [chat]"), Description: "chat"},
	}
	ra := routeragent.New(agent.Config{ID: "fb-rt"}, routes, routeragent.WithFunc(routeragent.KeywordFunc(1)))

	// Matched route
	resp, err := ra.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("solve math")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := resp.Messages[0].Content.Text(); got != "solve math [math]" {
		t.Errorf("got %q, want %q", got, "solve math [math]")
	}

	// Unmatched route falls back to index 1 (chat-agent)
	resp, err = ra.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("unrelated topic")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := resp.Messages[0].Content.Text(); got != "unrelated topic [chat]" {
		t.Errorf("got %q, want %q", got, "unrelated topic [chat]")
	}
}

// TestIntegration_RouterWithWorkflowSubAgent verifies that a router can delegate
// to a workflow agent, composing agent types.
func TestIntegration_RouterWithWorkflowSubAgent(t *testing.T) {
	// Import workflowagent indirectly through a CustomAgent that acts like a mini-pipeline
	pipeline := agent.NewCustomAgent(agent.Config{ID: "pipeline"}, func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		text := req.Messages[0].Content.Text()
		return &schema.RunResponse{
			Messages: []schema.Message{schema.NewUserMessage("[processed] " + text)},
		}, nil
	})

	routes := []routeragent.Route{
		{Agent: newTextAgent("simple", "-simple"), Description: "simple"},
		{Agent: pipeline, Description: "complex"},
	}
	ra := routeragent.New(agent.Config{ID: "composed-rt"}, routes, routeragent.WithFunc(routeragent.IndexFunc(1)))

	resp, err := ra.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("data")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := resp.Messages[0].Content.Text()
	if got != "[processed] data" {
		t.Errorf("got %q, want %q", got, "[processed] data")
	}
}

// TestIntegration_ValidationOrder verifies that empty routes is checked before nil routeFunc.
func TestIntegration_ValidationOrder(t *testing.T) {
	// Both errors present: empty routes AND nil routeFunc
	ra := routeragent.New(agent.Config{ID: "rt"}, nil) // no routes, no func
	_, err := ra.Run(context.Background(), &schema.RunRequest{})
	if err == nil {
		t.Fatal("expected error")
	}
	// Empty routes should be checked first
	if !strings.Contains(err.Error(), "no routes") {
		t.Errorf("expected 'no routes' error first, got: %v", err)
	}
}

// TestIntegration_NilAgentPanic verifies that New panics when a route has a nil Agent.
func TestIntegration_NilAgentPanic(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic for nil Agent in route")
		}
		msg, ok := r.(string)
		if !ok || !strings.Contains(msg, "route[1] has nil Agent") {
			t.Errorf("unexpected panic message: %v", r)
		}
	}()
	routeragent.New(agent.Config{ID: "rt"}, []routeragent.Route{
		{Agent: newTextAgent("ok", ""), Description: "ok"},
		{Agent: nil, Description: "bad"},
	})
}

// --- LLM Routing Integration Tests ---

// TestIntegration_LLMRouting_EndToEnd verifies the full LLM routing pipeline:
// LLM selects the correct agent, usage is aggregated, response is correct.
func TestIntegration_LLMRouting_EndToEnd(t *testing.T) {
	routes := []routeragent.Route{
		{Agent: newTextAgent("weather-agent", " [weather]"), Description: "Handles weather queries"},
		{Agent: newTextAgent("calendar-agent", " [calendar]"), Description: "Handles calendar operations"},
		{Agent: newTextAgent("email-agent", " [email]"), Description: "Handles email operations"},
	}

	mock := &mockChatCompleter{response: llmResponse("0", 8, 2, 10)}
	ra := routeragent.New(
		agent.Config{ID: "llm-router", Name: "LLM Router", Description: "routes via LLM"},
		routes,
		routeragent.WithFunc(routeragent.LLMFunc(mock, "gpt-test", -1)),
	)

	resp, err := ra.Run(context.Background(), &schema.RunRequest{
		Messages:  []schema.Message{schema.NewUserMessage("What's the weather?")},
		SessionID: "llm-sess",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := resp.Messages[0].Content.Text()
	if got != "What's the weather? [weather]" {
		t.Errorf("output = %q, want %q", got, "What's the weather? [weather]")
	}
	if resp.SessionID != "llm-sess" {
		t.Errorf("SessionID = %q, want %q", resp.SessionID, "llm-sess")
	}
}

// TestIntegration_LLMRouting_UsageAggregation verifies that usage from the LLM
// routing decision is aggregated with usage from the selected agent.
func TestIntegration_LLMRouting_UsageAggregation(t *testing.T) {
	routes := []routeragent.Route{
		{Agent: newUsageAgent("sub", 20, 30, 50), Description: "agent with usage"},
	}

	// LLM routing costs (8, 2, 10)
	mock := &mockChatCompleter{response: llmResponse("0", 8, 2, 10)}
	ra := routeragent.New(
		agent.Config{ID: "rt"},
		routes,
		routeragent.WithFunc(routeragent.LLMFunc(mock, "gpt-test", -1)),
	)

	resp, err := ra.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("test")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Usage == nil {
		t.Fatal("expected non-nil Usage")
	}
	// LLM routing (8,2,10) + agent (20,30,50) = (28,32,60)
	if resp.Usage.PromptTokens != 28 {
		t.Errorf("PromptTokens = %d, want 28", resp.Usage.PromptTokens)
	}
	if resp.Usage.CompletionTokens != 32 {
		t.Errorf("CompletionTokens = %d, want 32", resp.Usage.CompletionTokens)
	}
	if resp.Usage.TotalTokens != 60 {
		t.Errorf("TotalTokens = %d, want 60", resp.Usage.TotalTokens)
	}
}

// TestIntegration_LLMRouting_UsageAggregation_AgentNoUsage verifies usage aggregation
// when the selected agent returns no usage but the LLM routing does.
func TestIntegration_LLMRouting_UsageAggregation_AgentNoUsage(t *testing.T) {
	routes := []routeragent.Route{
		{Agent: newTextAgent("sub", ""), Description: "agent without usage"},
	}

	mock := &mockChatCompleter{response: llmResponse("0", 8, 2, 10)}
	ra := routeragent.New(
		agent.Config{ID: "rt"},
		routes,
		routeragent.WithFunc(routeragent.LLMFunc(mock, "gpt-test", -1)),
	)

	resp, err := ra.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("test")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Usage == nil {
		t.Fatal("expected non-nil Usage from routing")
	}
	if resp.Usage.PromptTokens != 8 {
		t.Errorf("PromptTokens = %d, want 8", resp.Usage.PromptTokens)
	}
}

// TestIntegration_LLMRouting_Fallback verifies that when the LLM fails,
// the fallback agent is used.
func TestIntegration_LLMRouting_Fallback(t *testing.T) {
	routes := []routeragent.Route{
		{Agent: newTextAgent("default", " [default]"), Description: "default agent"},
		{Agent: newTextAgent("other", " [other]"), Description: "other agent"},
	}

	mock := &mockChatCompleter{err: errors.New("LLM unavailable")}
	ra := routeragent.New(
		agent.Config{ID: "rt"},
		routes,
		routeragent.WithFunc(routeragent.LLMFunc(mock, "gpt-test", 0)), // fallback to index 0
	)

	resp, err := ra.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("test")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := resp.Messages[0].Content.Text()
	if got != "test [default]" {
		t.Errorf("got %q, want %q", got, "test [default]")
	}
}

// TestIntegration_LLMRouting_NoFallback_Error verifies that when the LLM fails
// and no fallback is configured, an error is returned.
func TestIntegration_LLMRouting_NoFallback_Error(t *testing.T) {
	routes := []routeragent.Route{
		{Agent: newTextAgent("sub", ""), Description: "agent"},
	}

	mock := &mockChatCompleter{err: errors.New("LLM unavailable")}
	ra := routeragent.New(
		agent.Config{ID: "rt"},
		routes,
		routeragent.WithFunc(routeragent.LLMFunc(mock, "gpt-test", -1)),
	)

	_, err := ra.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("test")},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "LLM routing") {
		t.Errorf("error = %q, want containing 'LLM routing'", err.Error())
	}
}

// TestIntegration_LLMRouting_InvalidResponse_Fallback verifies that when the LLM
// returns a non-numeric response, the fallback agent is used.
func TestIntegration_LLMRouting_InvalidResponse_Fallback(t *testing.T) {
	routes := []routeragent.Route{
		{Agent: newTextAgent("default", " [default]"), Description: "default"},
		{Agent: newTextAgent("other", " [other]"), Description: "other"},
	}

	mock := &mockChatCompleter{response: llmResponse("I think you should use agent 0", 5, 3, 8)}
	ra := routeragent.New(
		agent.Config{ID: "rt"},
		routes,
		routeragent.WithFunc(routeragent.LLMFunc(mock, "gpt-test", 1)), // fallback to "other"
	)

	resp, err := ra.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("test")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := resp.Messages[0].Content.Text()
	if got != "test [other]" {
		t.Errorf("got %q, want %q", got, "test [other]")
	}
}

// TestIntegration_LLMRouting_PromptVerification verifies that the LLM receives
// a properly formatted prompt with all route descriptions.
func TestIntegration_LLMRouting_PromptVerification(t *testing.T) {
	routes := []routeragent.Route{
		{Agent: newTextAgent("a", ""), Description: "Handles weather queries"},
		{Agent: newTextAgent("b", ""), Description: "Manages calendar events"},
		{Agent: newTextAgent("c", ""), Description: "Sends emails"},
	}

	mock := &mockChatCompleter{response: llmResponse("0", 5, 3, 8)}
	fn := routeragent.LLMFunc(mock, "my-model", -1)

	ra := routeragent.New(agent.Config{ID: "rt"}, routes, routeragent.WithFunc(fn))

	_, err := ra.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("What's the forecast?")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if mock.captured == nil {
		t.Fatal("expected captured request")
	}
	if mock.captured.Model != "my-model" {
		t.Errorf("Model = %q, want %q", mock.captured.Model, "my-model")
	}

	sysText := mock.captured.Messages[0].Content.Text()
	for _, desc := range []string{"Handles weather queries", "Manages calendar events", "Sends emails"} {
		if !strings.Contains(sysText, desc) {
			t.Errorf("system prompt missing description %q", desc)
		}
	}
	// Verify indices are present
	for _, idx := range []string{"0:", "1:", "2:"} {
		if !strings.Contains(sysText, idx) {
			t.Errorf("system prompt missing index %q", idx)
		}
	}

	userText := mock.captured.Messages[1].Content.Text()
	if userText != "What's the forecast?" {
		t.Errorf("user text = %q, want %q", userText, "What's the forecast?")
	}
}

// TestIntegration_LLMRouting_RunStream verifies that LLM routing works with RunStream.
func TestIntegration_LLMRouting_RunStream(t *testing.T) {
	routes := []routeragent.Route{
		{Agent: newTextAgent("sub", "-llm-streamed"), Description: "agent"},
	}

	mock := &mockChatCompleter{response: llmResponse("0", 5, 3, 8)}
	ra := routeragent.New(
		agent.Config{ID: "llm-stream-rt"},
		routes,
		routeragent.WithFunc(routeragent.LLMFunc(mock, "gpt-test", -1)),
	)

	stream, err := ra.RunStream(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("hello")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = stream.Close() }()

	// AgentStart
	e, err := stream.Recv()
	if err != nil {
		t.Fatalf("recv error: %v", err)
	}
	if e.Type != schema.EventAgentStart {
		t.Errorf("expected AgentStart, got %s", e.Type)
	}

	// AgentEnd
	e, err = stream.Recv()
	if err != nil {
		t.Fatalf("recv error: %v", err)
	}
	if e.Type != schema.EventAgentEnd {
		t.Errorf("expected AgentEnd, got %s", e.Type)
	}
	endData, ok := e.Data.(schema.AgentEndData)
	if !ok {
		t.Fatal("expected AgentEndData")
	}
	if endData.Message != "hello-llm-streamed" {
		t.Errorf("message = %q, want %q", endData.Message, "hello-llm-streamed")
	}

	// EOF
	_, err = stream.Recv()
	if !errors.Is(err, io.EOF) {
		t.Errorf("expected io.EOF, got: %v", err)
	}
}
