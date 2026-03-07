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

package routeragent

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/vogo/aimodel"
	"github.com/vogo/vagent/agent"
	"github.com/vogo/vagent/schema"
)

// --- Test helpers ---

// makeAgent creates a CustomAgent that appends suffix to the first input message text.
func makeAgent(id, suffix string) agent.Agent {
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

// makeAgentWithUsage creates a CustomAgent that returns fixed usage data.
func makeAgentWithUsage(id string, prompt, completion, total int) agent.Agent {
	return agent.NewCustomAgent(agent.Config{ID: id}, func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		return &schema.RunResponse{
			Messages: req.Messages,
			Usage:    &aimodel.Usage{PromptTokens: prompt, CompletionTokens: completion, TotalTokens: total},
		}, nil
	})
}

// makeErrorAgent creates a CustomAgent that returns an error.
func makeErrorAgent(id string, err error) agent.Agent {
	return agent.NewCustomAgent(agent.Config{ID: id}, func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		return nil, err
	})
}

// makeNilResponseAgent creates a CustomAgent that returns (nil, nil).
func makeNilResponseAgent(id string) agent.Agent {
	return agent.NewCustomAgent(agent.Config{ID: id}, func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		return nil, nil
	})
}

// Routing functions for testing.
func alwaysSelectFirst(_ context.Context, _ *schema.RunRequest, routes []Route) (*RouteResult, error) {
	return &RouteResult{Agent: routes[0].Agent}, nil
}

func alwaysError(_ context.Context, _ *schema.RunRequest, _ []Route) (*RouteResult, error) {
	return nil, errors.New("routing failed")
}

func alwaysNil(_ context.Context, _ *schema.RunRequest, _ []Route) (*RouteResult, error) {
	return nil, nil
}

// selectByIndex returns a RouteFunc that selects routes[i].Agent.
func selectByIndex(i int) RouteFunc {
	return func(_ context.Context, _ *schema.RunRequest, routes []Route) (*RouteResult, error) {
		return &RouteResult{Agent: routes[i].Agent}, nil
	}
}

// --- Tests ---

func TestAgent_Config(t *testing.T) {
	routes := []Route{
		{Agent: agent.NewCustomAgent(agent.Config{ID: "sub-1"}, nil), Description: "route one"},
	}
	a := New(agent.Config{ID: "rt-1", Name: "router", Description: "routes requests"}, routes)
	if a.ID() != "rt-1" {
		t.Errorf("ID = %q, want %q", a.ID(), "rt-1")
	}
	if a.Name() != "router" {
		t.Errorf("Name = %q, want %q", a.Name(), "router")
	}
	if a.Description() != "routes requests" {
		t.Errorf("Description = %q, want %q", a.Description(), "routes requests")
	}
}

func TestAgent_WithFunc(t *testing.T) {
	fn := func(_ context.Context, _ *schema.RunRequest, _ []Route) (*RouteResult, error) {
		return nil, nil
	}
	a := New(agent.Config{}, nil, WithFunc(fn))
	if a.routeFunc == nil {
		t.Error("routeFunc should not be nil")
	}
}

func TestAgent_Run_Success(t *testing.T) {
	sub := makeAgent("sub-1", "-routed")
	routes := []Route{{Agent: sub, Description: "the route"}}
	a := New(agent.Config{ID: "rt"}, routes, WithFunc(alwaysSelectFirst))

	resp, err := a.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("hello")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := resp.Messages[0].Content.Text()
	if got != "hello-routed" {
		t.Errorf("got %q, want %q", got, "hello-routed")
	}
}

func TestAgent_Run_SelectsCorrectRoute(t *testing.T) {
	routes := []Route{
		{Agent: makeAgent("sub-0", "-zero"), Description: "first"},
		{Agent: makeAgent("sub-1", "-one"), Description: "second"},
		{Agent: makeAgent("sub-2", "-two"), Description: "third"},
	}
	a := New(agent.Config{ID: "rt"}, routes, WithFunc(selectByIndex(2)))

	resp, err := a.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("hello")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := resp.Messages[0].Content.Text()
	if got != "hello-two" {
		t.Errorf("got %q, want %q", got, "hello-two")
	}
}

func TestAgent_Run_NilRouteFunc(t *testing.T) {
	routes := []Route{{Agent: makeAgent("sub-1", ""), Description: "route"}}
	a := New(agent.Config{ID: "rt"}, routes) // no WithFunc

	_, err := a.Run(context.Background(), &schema.RunRequest{})
	if err == nil {
		t.Fatal("expected error for nil routeFunc")
	}
	if !strings.Contains(err.Error(), "no routing function") {
		t.Errorf("error = %q, want containing 'no routing function'", err.Error())
	}
}

func TestAgent_Run_EmptyRoutes(t *testing.T) {
	a := New(agent.Config{ID: "rt"}, nil, WithFunc(alwaysSelectFirst))

	_, err := a.Run(context.Background(), &schema.RunRequest{})
	if err == nil {
		t.Fatal("expected error for empty routes")
	}
	if !strings.Contains(err.Error(), "no routes") {
		t.Errorf("error = %q, want containing 'no routes'", err.Error())
	}
}

func TestAgent_Run_RouteFuncError(t *testing.T) {
	routes := []Route{{Agent: makeAgent("sub-1", ""), Description: "route"}}
	a := New(agent.Config{ID: "rt"}, routes, WithFunc(alwaysError))

	_, err := a.Run(context.Background(), &schema.RunRequest{})
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

func TestAgent_Run_RouteFuncReturnsNil(t *testing.T) {
	routes := []Route{{Agent: makeAgent("sub-1", ""), Description: "route"}}
	a := New(agent.Config{ID: "rt"}, routes, WithFunc(alwaysNil))

	_, err := a.Run(context.Background(), &schema.RunRequest{})
	if err == nil {
		t.Fatal("expected error for nil agent")
	}
	if !strings.Contains(err.Error(), "returned nil agent") {
		t.Errorf("error = %q, want containing 'returned nil agent'", err.Error())
	}
}

func TestAgent_Run_SelectedAgentError(t *testing.T) {
	sub := makeErrorAgent("sub-1", errors.New("sub-agent boom"))
	routes := []Route{{Agent: sub, Description: "route"}}
	a := New(agent.Config{ID: "rt"}, routes, WithFunc(alwaysSelectFirst))

	_, err := a.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("hello")},
	})
	if err == nil {
		t.Fatal("expected error from selected agent")
	}
	if !strings.Contains(err.Error(), "sub-agent boom") {
		t.Errorf("error = %q, want containing 'sub-agent boom'", err.Error())
	}
}

func TestAgent_Run_NilResponse(t *testing.T) {
	sub := makeNilResponseAgent("sub-1")
	routes := []Route{{Agent: sub, Description: "route"}}
	a := New(agent.Config{ID: "rt"}, routes, WithFunc(alwaysSelectFirst))

	_, err := a.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("hello")},
	})
	if err == nil {
		t.Fatal("expected error for nil response")
	}
	if !strings.Contains(err.Error(), "nil response") {
		t.Errorf("error = %q, want containing 'nil response'", err.Error())
	}
}

func TestAgent_Run_ContextCancellation(t *testing.T) {
	routes := []Route{{Agent: makeAgent("sub-1", ""), Description: "route"}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before Run

	a := New(agent.Config{ID: "rt"}, routes, WithFunc(alwaysSelectFirst))
	_, err := a.Run(ctx, &schema.RunRequest{})
	if err == nil {
		t.Fatal("expected context error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got: %v", err)
	}
}

func TestAgent_Run_SessionIDPreserved(t *testing.T) {
	sub := makeAgent("sub-1", "")
	routes := []Route{{Agent: sub, Description: "route"}}
	a := New(agent.Config{ID: "rt"}, routes, WithFunc(alwaysSelectFirst))

	resp, err := a.Run(context.Background(), &schema.RunRequest{
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

func TestAgent_Run_Duration(t *testing.T) {
	sub := makeAgent("sub-1", "")
	routes := []Route{{Agent: sub, Description: "route"}}
	a := New(agent.Config{ID: "rt"}, routes, WithFunc(alwaysSelectFirst))

	resp, err := a.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("hello")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Duration < 0 {
		t.Errorf("Duration should be >= 0, got %d", resp.Duration)
	}
}

func TestAgent_Run_UsagePropagation(t *testing.T) {
	sub := makeAgentWithUsage("sub-1", 10, 20, 30)
	routes := []Route{{Agent: sub, Description: "route"}}
	a := New(agent.Config{ID: "rt"}, routes, WithFunc(alwaysSelectFirst))

	resp, err := a.Run(context.Background(), &schema.RunRequest{
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

func TestAgent_Run_UsageAggregation(t *testing.T) {
	sub := makeAgentWithUsage("sub-1", 10, 20, 30)
	routes := []Route{{Agent: sub, Description: "route"}}

	// Route func returns its own usage (simulating LLM routing cost).
	routeWithUsage := func(_ context.Context, _ *schema.RunRequest, routes []Route) (*RouteResult, error) {
		return &RouteResult{
			Agent: routes[0].Agent,
			Usage: &aimodel.Usage{PromptTokens: 5, CompletionTokens: 3, TotalTokens: 8},
		}, nil
	}
	a := New(agent.Config{ID: "rt"}, routes, WithFunc(routeWithUsage))

	resp, err := a.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("hello")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Usage == nil {
		t.Fatal("expected non-nil Usage")
	}
	// Routing usage (5,3,8) + agent usage (10,20,30) = (15,23,38)
	if resp.Usage.PromptTokens != 15 {
		t.Errorf("PromptTokens = %d, want 15", resp.Usage.PromptTokens)
	}
	if resp.Usage.CompletionTokens != 23 {
		t.Errorf("CompletionTokens = %d, want 23", resp.Usage.CompletionTokens)
	}
	if resp.Usage.TotalTokens != 38 {
		t.Errorf("TotalTokens = %d, want 38", resp.Usage.TotalTokens)
	}
}

func TestAgent_Run_UsageAggregation_RouteOnlyUsage(t *testing.T) {
	// Sub-agent returns no usage, but route func does.
	sub := makeAgent("sub-1", "")
	routes := []Route{{Agent: sub, Description: "route"}}

	routeWithUsage := func(_ context.Context, _ *schema.RunRequest, routes []Route) (*RouteResult, error) {
		return &RouteResult{
			Agent: routes[0].Agent,
			Usage: &aimodel.Usage{PromptTokens: 5, CompletionTokens: 3, TotalTokens: 8},
		}, nil
	}
	a := New(agent.Config{ID: "rt"}, routes, WithFunc(routeWithUsage))

	resp, err := a.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("hello")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Usage == nil {
		t.Fatal("expected non-nil Usage")
	}
	if resp.Usage.PromptTokens != 5 {
		t.Errorf("PromptTokens = %d, want 5", resp.Usage.PromptTokens)
	}
}

func TestAgent_Run_RequestPassthrough(t *testing.T) {
	var captured *schema.RunRequest
	sub := agent.NewCustomAgent(agent.Config{ID: "sub-1"}, func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		captured = req
		return &schema.RunResponse{Messages: req.Messages}, nil
	})
	routes := []Route{{Agent: sub, Description: "route"}}
	a := New(agent.Config{ID: "rt"}, routes, WithFunc(alwaysSelectFirst))

	meta := map[string]any{"key": "value"}
	opts := &schema.RunOptions{MaxTokens: 100}
	req := &schema.RunRequest{
		Messages:  []schema.Message{schema.NewUserMessage("hello")},
		SessionID: "sess-99",
		Options:   opts,
		Metadata:  meta,
	}

	_, err := a.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if captured != req {
		t.Error("selected agent did not receive the original request")
	}
}

func TestAgent_RunStream_Basic(t *testing.T) {
	sub := makeAgent("sub-1", "-streamed")
	routes := []Route{{Agent: sub, Description: "route"}}
	a := New(agent.Config{ID: "rt"}, routes, WithFunc(alwaysSelectFirst))

	stream, err := a.RunStream(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("hello")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = stream.Close() }()

	// Expect AgentStart event.
	e, err := stream.Recv()
	if err != nil {
		t.Fatalf("recv error: %v", err)
	}
	if e.Type != schema.EventAgentStart {
		t.Errorf("expected AgentStart, got %s", e.Type)
	}

	// Expect AgentEnd event.
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
	if endData.Message != "hello-streamed" {
		t.Errorf("got message %q, want %q", endData.Message, "hello-streamed")
	}

	// Expect EOF.
	_, err = stream.Recv()
	if !errors.Is(err, io.EOF) {
		t.Errorf("expected io.EOF, got %v", err)
	}
}

func TestAgent_RunStream_Error(t *testing.T) {
	sub := makeErrorAgent("sub-1", errors.New("stream route failed"))
	routes := []Route{{Agent: sub, Description: "route"}}
	a := New(agent.Config{ID: "rt"}, routes, WithFunc(alwaysSelectFirst))

	stream, err := a.RunStream(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("hello")},
	})
	if err != nil {
		t.Fatalf("unexpected error creating stream: %v", err)
	}
	defer func() { _ = stream.Close() }()

	// Expect AgentStart event.
	e, err := stream.Recv()
	if err != nil {
		t.Fatalf("recv error: %v", err)
	}
	if e.Type != schema.EventAgentStart {
		t.Errorf("expected AgentStart, got %s", e.Type)
	}

	// Expect error from producer.
	_, err = stream.Recv()
	if err == nil {
		t.Fatal("expected error from stream")
	}
	if !strings.Contains(err.Error(), "stream route failed") {
		t.Errorf("error = %q, want containing 'stream route failed'", err.Error())
	}
}

func TestNew_NilAgentPanic(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic for nil Agent in route")
		}
		msg, ok := r.(string)
		if !ok || !strings.Contains(msg, "route[0] has nil Agent") {
			t.Errorf("unexpected panic message: %v", r)
		}
	}()
	New(agent.Config{ID: "rt"}, []Route{{Agent: nil, Description: "bad"}})
}
