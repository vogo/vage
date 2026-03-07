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

package workflowagent_tests

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vogo/aimodel"
	"github.com/vogo/vagent/agent"
	"github.com/vogo/vagent/agent/workflowagent"
	"github.com/vogo/vagent/schema"
)

// --- Integration test helpers (black-box, external package) ---

func newTextStep(id, suffix string) agent.Agent {
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

func newUsageStep(id string, prompt, completion, total int) agent.Agent {
	return agent.NewCustomAgent(agent.Config{ID: id}, func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		return &schema.RunResponse{
			Messages: req.Messages,
			Usage:    &aimodel.Usage{PromptTokens: prompt, CompletionTokens: completion, TotalTokens: total},
		}, nil
	})
}

func newFailStep(id string, err error) agent.Agent {
	return agent.NewCustomAgent(agent.Config{ID: id}, func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		return nil, err
	})
}

func newNilRespStep(id string) agent.Agent {
	return agent.NewCustomAgent(agent.Config{ID: id}, func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		return nil, nil
	})
}

// --- Integration Tests ---

// TestIntegration_SequentialPipeline_EndToEnd verifies the full sequential pipeline:
// three steps chaining output, usage aggregation, duration, and session ID preservation.
func TestIntegration_SequentialPipeline_EndToEnd(t *testing.T) {
	step1 := agent.NewCustomAgent(agent.Config{ID: "translate"}, func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		text := req.Messages[0].Content.Text()
		return &schema.RunResponse{
			Messages: []schema.Message{schema.NewUserMessage("[translated] " + text)},
			Usage:    &aimodel.Usage{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30},
		}, nil
	})
	step2 := agent.NewCustomAgent(agent.Config{ID: "summarize"}, func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		text := req.Messages[0].Content.Text()
		return &schema.RunResponse{
			Messages: []schema.Message{schema.NewUserMessage("[summary] " + text)},
			Usage:    &aimodel.Usage{PromptTokens: 5, CompletionTokens: 10, TotalTokens: 15},
		}, nil
	})
	step3 := agent.NewCustomAgent(agent.Config{ID: "format"}, func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		text := req.Messages[0].Content.Text()
		return &schema.RunResponse{
			Messages: []schema.Message{schema.NewUserMessage("[formatted] " + text)},
			Usage:    &aimodel.Usage{PromptTokens: 3, CompletionTokens: 7, TotalTokens: 10},
		}, nil
	})

	wf := workflowagent.New(agent.Config{ID: "pipeline", Name: "Pipeline", Description: "e2e test"}, step1, step2, step3)

	req := &schema.RunRequest{
		Messages:  []schema.Message{schema.NewUserMessage("hello world")},
		SessionID: "session-e2e",
	}
	resp, err := wf.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify chained output
	got := resp.Messages[0].Content.Text()
	want := "[formatted] [summary] [translated] hello world"
	if got != want {
		t.Errorf("chained output = %q, want %q", got, want)
	}

	// Verify session ID preserved
	if resp.SessionID != "session-e2e" {
		t.Errorf("SessionID = %q, want %q", resp.SessionID, "session-e2e")
	}

	// Verify usage aggregation (10+5+3=18, 20+10+7=37, 30+15+10=55)
	if resp.Usage == nil {
		t.Fatal("expected non-nil Usage")
	}
	if resp.Usage.PromptTokens != 18 {
		t.Errorf("PromptTokens = %d, want 18", resp.Usage.PromptTokens)
	}
	if resp.Usage.CompletionTokens != 37 {
		t.Errorf("CompletionTokens = %d, want 37", resp.Usage.CompletionTokens)
	}
	if resp.Usage.TotalTokens != 55 {
		t.Errorf("TotalTokens = %d, want 55", resp.Usage.TotalTokens)
	}

	// Verify duration is non-negative
	if resp.Duration < 0 {
		t.Errorf("Duration = %d, want >= 0", resp.Duration)
	}
}

// TestIntegration_EmptyPipeline verifies that a workflow with no steps
// returns the original messages, preserves session ID, and has nil usage.
func TestIntegration_EmptyPipeline(t *testing.T) {
	wf := workflowagent.New(agent.Config{ID: "empty-wf"})
	req := &schema.RunRequest{
		Messages:  []schema.Message{schema.NewUserMessage("pass-through")},
		SessionID: "sess-empty",
	}
	resp, err := wf.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(resp.Messages))
	}
	if resp.Messages[0].Content.Text() != "pass-through" {
		t.Errorf("expected original message echoed back, got %q", resp.Messages[0].Content.Text())
	}
	if resp.SessionID != "sess-empty" {
		t.Errorf("SessionID = %q, want %q", resp.SessionID, "sess-empty")
	}
	if resp.Usage != nil {
		t.Errorf("expected nil Usage for empty pipeline, got %+v", resp.Usage)
	}
	if resp.Duration < 0 {
		t.Errorf("Duration = %d, want >= 0", resp.Duration)
	}
}

// TestIntegration_SingleStep verifies that a single-step pipeline works correctly.
func TestIntegration_SingleStep(t *testing.T) {
	step := newTextStep("only", "-processed")
	wf := workflowagent.New(agent.Config{ID: "single-wf"}, step)
	resp, err := wf.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("data")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := resp.Messages[0].Content.Text(); got != "data-processed" {
		t.Errorf("got %q, want %q", got, "data-processed")
	}
}

// TestIntegration_ErrorStopsExecution verifies that an error in a middle step
// stops execution and does not invoke subsequent steps.
func TestIntegration_ErrorStopsExecution(t *testing.T) {
	var step3Called atomic.Bool
	step1 := newTextStep("s1", "-ok")
	step2 := newFailStep("s2", errors.New("boom"))
	step3 := agent.NewCustomAgent(agent.Config{ID: "s3"}, func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		step3Called.Store(true)
		return &schema.RunResponse{Messages: req.Messages}, nil
	})

	wf := workflowagent.New(agent.Config{ID: "err-wf"}, step1, step2, step3)
	_, err := wf.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("input")},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error should contain original error message, got: %v", err)
	}
	if !strings.Contains(err.Error(), "s2") {
		t.Errorf("error should contain step ID 's2', got: %v", err)
	}
	if !strings.Contains(err.Error(), "step 2") {
		t.Errorf("error should contain step index 'step 2', got: %v", err)
	}
	if step3Called.Load() {
		t.Error("step 3 should not have been called after step 2 error")
	}
}

// TestIntegration_NilResponseError verifies that a step returning (nil, nil)
// produces a descriptive error.
func TestIntegration_NilResponseError(t *testing.T) {
	step := newNilRespStep("nil-step")
	wf := workflowagent.New(agent.Config{ID: "nil-wf"}, step)
	_, err := wf.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("test")},
	})
	if err == nil {
		t.Fatal("expected error for nil response")
	}
	if !strings.Contains(err.Error(), "nil response") {
		t.Errorf("error should mention 'nil response', got: %v", err)
	}
}

// TestIntegration_ContextCancellation verifies that cancelling the context
// between steps stops the pipeline and returns context.Canceled.
func TestIntegration_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var step2Called atomic.Bool

	step1 := agent.NewCustomAgent(agent.Config{ID: "s1"}, func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		cancel() // cancel before step 2 runs
		return &schema.RunResponse{Messages: req.Messages}, nil
	})
	step2 := agent.NewCustomAgent(agent.Config{ID: "s2"}, func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		step2Called.Store(true)
		return &schema.RunResponse{Messages: req.Messages}, nil
	})

	wf := workflowagent.New(agent.Config{ID: "cancel-wf"}, step1, step2)
	_, err := wf.Run(ctx, &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("test")},
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got: %v", err)
	}
	if step2Called.Load() {
		t.Error("step 2 should not have been called after cancellation")
	}
}

// TestIntegration_ContextDeadlineExceeded verifies context.DeadlineExceeded behavior.
func TestIntegration_ContextDeadlineExceeded(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()

	step1 := agent.NewCustomAgent(agent.Config{ID: "slow"}, func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		time.Sleep(50 * time.Millisecond) // exceed the deadline
		return &schema.RunResponse{Messages: req.Messages}, nil
	})
	step2 := newTextStep("s2", "-never")

	wf := workflowagent.New(agent.Config{ID: "timeout-wf"}, step1, step2)
	_, err := wf.Run(ctx, &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("test")},
	})
	// After step1 completes (past deadline), the ctx.Err() check before step2 should catch it
	if err == nil {
		t.Fatal("expected error due to deadline exceeded")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected context.DeadlineExceeded, got: %v", err)
	}
}

// TestIntegration_UsageAggregation_MixedNilAndNonNil verifies that usage is
// correctly aggregated when some steps return nil usage and some do not.
func TestIntegration_UsageAggregation_MixedNilAndNonNil(t *testing.T) {
	step1 := newUsageStep("u1", 10, 20, 30)
	step2 := newTextStep("no-usage", "") // nil usage
	step3 := newUsageStep("u3", 5, 10, 15)

	wf := workflowagent.New(agent.Config{ID: "usage-wf"}, step1, step2, step3)
	resp, err := wf.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("test")},
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
	if resp.Usage.CompletionTokens != 30 {
		t.Errorf("CompletionTokens = %d, want 30", resp.Usage.CompletionTokens)
	}
	if resp.Usage.TotalTokens != 45 {
		t.Errorf("TotalTokens = %d, want 45", resp.Usage.TotalTokens)
	}
}

// TestIntegration_UsageAllNil verifies that when no step returns usage,
// the final response usage is nil.
func TestIntegration_UsageAllNil(t *testing.T) {
	step1 := newTextStep("s1", "-a")
	step2 := newTextStep("s2", "-b")

	wf := workflowagent.New(agent.Config{ID: "no-usage-wf"}, step1, step2)
	resp, err := wf.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("test")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Usage != nil {
		t.Errorf("expected nil Usage when no step provides usage, got %+v", resp.Usage)
	}
}

// TestIntegration_SessionIDPassedToEachStep verifies that the original session ID
// is forwarded to each step in the pipeline.
func TestIntegration_SessionIDPassedToEachStep(t *testing.T) {
	var sessionIDs []string

	capture := func(id string) agent.Agent {
		return agent.NewCustomAgent(agent.Config{ID: id}, func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
			sessionIDs = append(sessionIDs, req.SessionID)
			return &schema.RunResponse{Messages: req.Messages}, nil
		})
	}

	wf := workflowagent.New(agent.Config{ID: "session-wf"}, capture("s1"), capture("s2"), capture("s3"))
	resp, err := wf.Run(context.Background(), &schema.RunRequest{
		Messages:  []schema.Message{schema.NewUserMessage("test")},
		SessionID: "my-session",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// All steps should have received the same session ID
	for i, sid := range sessionIDs {
		if sid != "my-session" {
			t.Errorf("step %d received SessionID %q, want %q", i, sid, "my-session")
		}
	}
	// Final response should preserve session ID
	if resp.SessionID != "my-session" {
		t.Errorf("response SessionID = %q, want %q", resp.SessionID, "my-session")
	}
}

// TestIntegration_OptionsAndMetadataPassedToEachStep verifies that Options and Metadata
// from the original request are forwarded to each step.
func TestIntegration_OptionsAndMetadataPassedToEachStep(t *testing.T) {
	type captured struct {
		options  *schema.RunOptions
		metadata map[string]any
	}
	var caps []captured

	capStep := func(id string) agent.Agent {
		return agent.NewCustomAgent(agent.Config{ID: id}, func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
			caps = append(caps, captured{options: req.Options, metadata: req.Metadata})
			return &schema.RunResponse{
				Messages: []schema.Message{schema.NewUserMessage("out-" + id)},
			}, nil
		})
	}

	temp := 0.5
	opts := &schema.RunOptions{Model: "gpt-test", Temperature: &temp}
	meta := map[string]any{"key": "value"}

	wf := workflowagent.New(agent.Config{ID: "opts-wf"}, capStep("s1"), capStep("s2"))
	_, err := wf.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("test")},
		Options:  opts,
		Metadata: meta,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(caps) != 2 {
		t.Fatalf("expected 2 captures, got %d", len(caps))
	}
	for i, c := range caps {
		if c.options == nil || c.options.Model != "gpt-test" {
			t.Errorf("step %d: Options.Model = %v, want 'gpt-test'", i, c.options)
		}
		if c.metadata == nil || c.metadata["key"] != "value" {
			t.Errorf("step %d: Metadata missing expected key", i)
		}
	}
}

// TestIntegration_AgentInterfaceCompliance verifies that *workflowagent.Agent
// satisfies both agent.Agent and agent.StreamAgent interfaces.
func TestIntegration_AgentInterfaceCompliance(t *testing.T) {
	wf := workflowagent.New(agent.Config{ID: "iface-test", Name: "Test", Description: "compliance"})

	// agent.Agent interface
	var a agent.Agent = wf
	if a.ID() != "iface-test" {
		t.Errorf("ID = %q, want %q", a.ID(), "iface-test")
	}
	if a.Name() != "Test" {
		t.Errorf("Name = %q, want %q", a.Name(), "Test")
	}
	if a.Description() != "compliance" {
		t.Errorf("Description = %q, want %q", a.Description(), "compliance")
	}

	// agent.StreamAgent interface
	var sa agent.StreamAgent = wf
	_ = sa
}

// TestIntegration_RunStream_FullLifecycle verifies that RunStream emits
// AgentStart then AgentEnd events with correct data, then EOF.
func TestIntegration_RunStream_FullLifecycle(t *testing.T) {
	step1 := newTextStep("s1", "-A")
	step2 := newTextStep("s2", "-B")

	wf := workflowagent.New(agent.Config{ID: "stream-wf"}, step1, step2)
	stream, err := wf.RunStream(context.Background(), &schema.RunRequest{
		Messages:  []schema.Message{schema.NewUserMessage("start")},
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
	if e.AgentID != "stream-wf" {
		t.Errorf("AgentID = %q, want %q", e.AgentID, "stream-wf")
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
	if endData.Message != "start-A-B" {
		t.Errorf("AgentEnd message = %q, want %q", endData.Message, "start-A-B")
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

// TestIntegration_RunStream_EmptySteps verifies streaming with no steps
// still emits AgentStart and AgentEnd events.
func TestIntegration_RunStream_EmptySteps(t *testing.T) {
	wf := workflowagent.New(agent.Config{ID: "empty-stream"})
	stream, err := wf.RunStream(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("test")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = stream.Close() }()

	e, err := stream.Recv()
	if err != nil {
		t.Fatalf("recv error: %v", err)
	}
	if e.Type != schema.EventAgentStart {
		t.Errorf("expected AgentStart, got %s", e.Type)
	}

	e, err = stream.Recv()
	if err != nil {
		t.Fatalf("recv error: %v", err)
	}
	if e.Type != schema.EventAgentEnd {
		t.Errorf("expected AgentEnd, got %s", e.Type)
	}

	_, err = stream.Recv()
	if !errors.Is(err, io.EOF) {
		t.Errorf("expected io.EOF, got: %v", err)
	}
}

// TestIntegration_RunStream_ErrorSurfaced verifies that a step error
// during streaming is surfaced through Recv after the AgentStart event.
func TestIntegration_RunStream_ErrorSurfaced(t *testing.T) {
	step := newFailStep("fail-step", errors.New("integration stream failure"))
	wf := workflowagent.New(agent.Config{ID: "err-stream"}, step)
	stream, err := wf.RunStream(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("test")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
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

	// Next recv should return the step error
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
	step := agent.NewCustomAgent(agent.Config{ID: "slow-step"}, func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		// Wait for context cancellation (from Close)
		<-ctx.Done()
		return nil, ctx.Err()
	})

	wf := workflowagent.New(agent.Config{ID: "close-wf"}, step)
	stream, closeErr := wf.RunStream(context.Background(), &schema.RunRequest{
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

	// Subsequent Recv should return ErrRunStreamClosed
	_, recvErr = stream.Recv()
	if !errors.Is(recvErr, schema.ErrRunStreamClosed) {
		// It may also return io.EOF or another error depending on timing;
		// the key is that it does not hang and does not return (event, nil).
		if recvErr == nil {
			t.Error("expected error after Close, got nil")
		}
	}
}

// TestIntegration_RunText_Convenience verifies the agent.RunText convenience function
// works with a workflow agent.
func TestIntegration_RunText_Convenience(t *testing.T) {
	step := newTextStep("s1", "-via-RunText")
	wf := workflowagent.New(agent.Config{ID: "convenience-wf"}, step)

	resp, err := agent.RunText(context.Background(), wf, "hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := resp.Messages[0].Content.Text()
	if got != "hello-via-RunText" {
		t.Errorf("got %q, want %q", got, "hello-via-RunText")
	}
}

// TestIntegration_RunStreamText_Convenience verifies the agent.RunStreamText convenience
// function works with a workflow agent.
func TestIntegration_RunStreamText_Convenience(t *testing.T) {
	step := newTextStep("s1", "-via-stream")
	wf := workflowagent.New(agent.Config{ID: "stream-conv-wf"}, step)

	stream, err := agent.RunStreamText(context.Background(), wf, "world")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = stream.Close() }()

	// Drain events
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

// TestIntegration_NestedWorkflow verifies that a workflow can contain another
// workflow as a step, forming a nested pipeline.
func TestIntegration_NestedWorkflow(t *testing.T) {
	inner := workflowagent.New(
		agent.Config{ID: "inner"},
		newTextStep("i1", "-inner1"),
		newTextStep("i2", "-inner2"),
	)
	outer := workflowagent.New(
		agent.Config{ID: "outer"},
		newTextStep("o1", "-outer1"),
		inner,
		newTextStep("o3", "-outer3"),
	)

	resp, err := outer.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("root")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := resp.Messages[0].Content.Text()
	want := "root-outer1-inner1-inner2-outer3"
	if got != want {
		t.Errorf("nested output = %q, want %q", got, want)
	}
}

// TestIntegration_LargeStepCount verifies the workflow handles many steps without issues.
func TestIntegration_LargeStepCount(t *testing.T) {
	const n = 100
	steps := make([]agent.Agent, n)
	for i := range n {
		steps[i] = newTextStep("s", ".")
	}

	wf := workflowagent.New(agent.Config{ID: "large-wf"}, steps...)
	resp, err := wf.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("x")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := resp.Messages[0].Content.Text()
	// "x" + 100 dots
	want := "x" + strings.Repeat(".", n)
	if got != want {
		t.Errorf("got length %d, want length %d", len(got), len(want))
	}
}

// TestIntegration_MetadataPreservedFromLastStep verifies that metadata set by
// the last step is available in the final response.
func TestIntegration_MetadataPreservedFromLastStep(t *testing.T) {
	step := agent.NewCustomAgent(agent.Config{ID: "meta-step"}, func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		return &schema.RunResponse{
			Messages: req.Messages,
			Metadata: map[string]any{"result_key": "result_value"},
		}, nil
	})

	wf := workflowagent.New(agent.Config{ID: "meta-wf"}, step)
	resp, err := wf.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("test")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Metadata == nil || resp.Metadata["result_key"] != "result_value" {
		t.Errorf("expected metadata from last step to be preserved, got %v", resp.Metadata)
	}
}
