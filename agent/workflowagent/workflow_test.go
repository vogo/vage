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

package workflowagent

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

// makeStep creates a CustomAgent that appends suffix to the first input message text.
func makeStep(id, suffix string) agent.Agent {
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

// makeStepWithUsage creates a CustomAgent that returns fixed usage data.
func makeStepWithUsage(id string, prompt, completion, total int) agent.Agent {
	return agent.NewCustomAgent(agent.Config{ID: id}, func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		return &schema.RunResponse{
			Messages: req.Messages,
			Usage:    &aimodel.Usage{PromptTokens: prompt, CompletionTokens: completion, TotalTokens: total},
		}, nil
	})
}

// makeErrorStep creates a CustomAgent that returns an error.
func makeErrorStep(id string, err error) agent.Agent {
	return agent.NewCustomAgent(agent.Config{ID: id}, func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		return nil, err
	})
}

// makeNilResponseStep creates a CustomAgent that returns (nil, nil).
func makeNilResponseStep(id string) agent.Agent {
	return agent.NewCustomAgent(agent.Config{ID: id}, func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		return nil, nil
	})
}

// makeTrackingStep creates a CustomAgent that records whether it was called.
func makeTrackingStep(id string, called *bool) agent.Agent {
	return agent.NewCustomAgent(agent.Config{ID: id}, func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		*called = true
		return &schema.RunResponse{Messages: req.Messages}, nil
	})
}

// --- Tests ---

func TestAgent_Config(t *testing.T) {
	a := New(agent.Config{ID: "wf-1", Name: "workflow", Description: "sequential"})
	if a.ID() != "wf-1" {
		t.Errorf("ID = %q, want %q", a.ID(), "wf-1")
	}
	if a.Name() != "workflow" {
		t.Errorf("Name = %q, want %q", a.Name(), "workflow")
	}
	if a.Description() != "sequential" {
		t.Errorf("Description = %q, want %q", a.Description(), "sequential")
	}
}

func TestAgent_Run_Sequential(t *testing.T) {
	step1 := makeStep("s1", "-a")
	step2 := makeStep("s2", "-b")
	step3 := makeStep("s3", "-c")

	wf := New(agent.Config{ID: "wf"}, step1, step2, step3)
	resp, err := wf.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("start")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := resp.Messages[0].Content.Text()
	want := "start-a-b-c"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestAgent_Run_OutputChaining(t *testing.T) {
	stepA := agent.NewCustomAgent(agent.Config{ID: "a"}, func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		return &schema.RunResponse{
			Messages: []schema.Message{schema.NewUserMessage("hello")},
		}, nil
	})
	stepB := agent.NewCustomAgent(agent.Config{ID: "b"}, func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		text := req.Messages[0].Content.Text()
		return &schema.RunResponse{
			Messages: []schema.Message{schema.NewUserMessage(text + " world")},
		}, nil
	})

	wf := New(agent.Config{ID: "wf"}, stepA, stepB)
	resp, err := wf.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("ignored")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := resp.Messages[0].Content.Text()
	if got != "hello world" {
		t.Errorf("got %q, want %q", got, "hello world")
	}
}

func TestAgent_Run_ErrorPropagation(t *testing.T) {
	step1 := makeStep("s1", "-a")
	step2 := makeErrorStep("s2", errors.New("step2 failed"))
	called := false
	step3 := makeTrackingStep("s3", &called)

	wf := New(agent.Config{ID: "wf"}, step1, step2, step3)
	_, err := wf.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("start")},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "step 2") {
		t.Errorf("error should contain step index: %v", err)
	}
	if !strings.Contains(err.Error(), "s2") {
		t.Errorf("error should contain step ID: %v", err)
	}
	if called {
		t.Error("step 3 should not have been called")
	}
}

func TestAgent_Run_EmptySteps(t *testing.T) {
	wf := New(agent.Config{ID: "wf"})
	req := &schema.RunRequest{
		Messages:  []schema.Message{schema.NewUserMessage("hello")},
		SessionID: "sess-1",
	}
	resp, err := wf.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(resp.Messages))
	}
	if resp.Messages[0].Content.Text() != "hello" {
		t.Errorf("expected original message echoed back")
	}
	if resp.SessionID != "sess-1" {
		t.Errorf("SessionID = %q, want %q", resp.SessionID, "sess-1")
	}
}

func TestAgent_Run_SingleStep(t *testing.T) {
	step := makeStep("s1", "-done")
	wf := New(agent.Config{ID: "wf"}, step)
	resp, err := wf.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("input")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := resp.Messages[0].Content.Text()
	if got != "input-done" {
		t.Errorf("got %q, want %q", got, "input-done")
	}
}

func TestAgent_Run_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	step1 := agent.NewCustomAgent(agent.Config{ID: "s1"}, func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		cancel()
		return &schema.RunResponse{Messages: req.Messages}, nil
	})
	called := false
	step2 := makeTrackingStep("s2", &called)

	wf := New(agent.Config{ID: "wf"}, step1, step2)
	_, err := wf.Run(ctx, &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("hello")},
	})
	if err == nil {
		t.Fatal("expected context error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got: %v", err)
	}
	if called {
		t.Error("step 2 should not have been called")
	}
}

func TestAgent_Run_UsageAggregation(t *testing.T) {
	step1 := makeStepWithUsage("s1", 10, 20, 30)
	step2 := makeStepWithUsage("s2", 5, 15, 20)

	wf := New(agent.Config{ID: "wf"}, step1, step2)
	resp, err := wf.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("hello")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Usage == nil {
		t.Fatal("expected non-nil Usage")
	}
	if resp.Usage.PromptTokens != 15 {
		t.Errorf("PromptTokens = %d, want 15", resp.Usage.PromptTokens)
	}
	if resp.Usage.CompletionTokens != 35 {
		t.Errorf("CompletionTokens = %d, want 35", resp.Usage.CompletionTokens)
	}
	if resp.Usage.TotalTokens != 50 {
		t.Errorf("TotalTokens = %d, want 50", resp.Usage.TotalTokens)
	}
}

func TestAgent_Run_Duration(t *testing.T) {
	step := makeStep("s1", "")
	wf := New(agent.Config{ID: "wf"}, step)
	resp, err := wf.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("hello")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Duration < 0 {
		t.Errorf("Duration should be >= 0, got %d", resp.Duration)
	}
}

func TestAgent_Run_NilResponse(t *testing.T) {
	step := makeNilResponseStep("s1")
	wf := New(agent.Config{ID: "wf"}, step)
	_, err := wf.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("hello")},
	})
	if err == nil {
		t.Fatal("expected error for nil response")
	}
	if !strings.Contains(err.Error(), "nil response") {
		t.Errorf("error should mention nil response: %v", err)
	}
}

func TestAgent_Run_SessionIDPreserved(t *testing.T) {
	var receivedSessionID string
	step := agent.NewCustomAgent(agent.Config{ID: "s1"}, func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		receivedSessionID = req.SessionID
		return &schema.RunResponse{Messages: req.Messages}, nil
	})
	wf := New(agent.Config{ID: "wf"}, step)
	resp, err := wf.Run(context.Background(), &schema.RunRequest{
		Messages:  []schema.Message{schema.NewUserMessage("hello")},
		SessionID: "test-session",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if receivedSessionID != "test-session" {
		t.Errorf("step received SessionID %q, want %q", receivedSessionID, "test-session")
	}
	if resp.SessionID != "test-session" {
		t.Errorf("response SessionID %q, want %q", resp.SessionID, "test-session")
	}
}

func TestAgent_Run_UsageNilSkipped(t *testing.T) {
	step1 := makeStepWithUsage("s1", 10, 20, 30)
	step2 := makeStep("s2", "") // no usage

	wf := New(agent.Config{ID: "wf"}, step1, step2)
	resp, err := wf.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("hello")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Usage == nil {
		t.Fatal("expected non-nil Usage (step1 had usage)")
	}
	if resp.Usage.PromptTokens != 10 {
		t.Errorf("PromptTokens = %d, want 10", resp.Usage.PromptTokens)
	}
}

func TestAgent_StreamAgent_Compliance(t *testing.T) {
	var _ agent.StreamAgent = (*Agent)(nil)
}

func TestAgent_RunStream_Basic(t *testing.T) {
	step := makeStep("s1", "-streamed")
	wf := New(agent.Config{ID: "wf"}, step)
	stream, err := wf.RunStream(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("hello")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer stream.Close()

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
	step := makeErrorStep("s1", errors.New("stream step failed"))
	wf := New(agent.Config{ID: "wf"}, step)
	stream, err := wf.RunStream(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("hello")},
	})
	if err != nil {
		t.Fatalf("unexpected error creating stream: %v", err)
	}
	defer stream.Close()

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
	if !strings.Contains(err.Error(), "stream step failed") {
		t.Errorf("error = %q, want containing 'stream step failed'", err.Error())
	}
}

func TestAgent_RunStream_EmptySteps(t *testing.T) {
	wf := New(agent.Config{ID: "wf"})
	stream, err := wf.RunStream(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("hello")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer stream.Close()

	// Expect AgentStart.
	e, err := stream.Recv()
	if err != nil {
		t.Fatalf("recv error: %v", err)
	}
	if e.Type != schema.EventAgentStart {
		t.Errorf("expected AgentStart, got %s", e.Type)
	}

	// Expect AgentEnd.
	e, err = stream.Recv()
	if err != nil {
		t.Fatalf("recv error: %v", err)
	}
	if e.Type != schema.EventAgentEnd {
		t.Errorf("expected AgentEnd, got %s", e.Type)
	}

	// Expect EOF.
	_, err = stream.Recv()
	if !errors.Is(err, io.EOF) {
		t.Errorf("expected io.EOF, got %v", err)
	}
}
