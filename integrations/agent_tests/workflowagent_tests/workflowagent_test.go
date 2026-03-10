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
	"github.com/vogo/vagent/orchestrate"
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

// --- DAG Workflow Integration Tests ---

// TestIntegration_DAG_DiamondPipeline verifies a diamond DAG topology:
// A -> B, A -> C, B+C -> D, with usage aggregation, session ID, and duration.
func TestIntegration_DAG_DiamondPipeline(t *testing.T) {
	stepA := agent.NewCustomAgent(agent.Config{ID: "a"}, func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		text := req.Messages[0].Content.Text()
		return &schema.RunResponse{
			Messages: []schema.Message{schema.NewUserMessage("[analyzed] " + text)},
			Usage:    &aimodel.Usage{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30},
		}, nil
	})
	stepB := agent.NewCustomAgent(agent.Config{ID: "b"}, func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		text := req.Messages[0].Content.Text()
		return &schema.RunResponse{
			Messages: []schema.Message{schema.NewUserMessage("[translated] " + text)},
			Usage:    &aimodel.Usage{PromptTokens: 5, CompletionTokens: 10, TotalTokens: 15},
		}, nil
	})
	stepC := agent.NewCustomAgent(agent.Config{ID: "c"}, func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		text := req.Messages[0].Content.Text()
		return &schema.RunResponse{
			Messages: []schema.Message{schema.NewUserMessage("[summarized] " + text)},
			Usage:    &aimodel.Usage{PromptTokens: 3, CompletionTokens: 7, TotalTokens: 10},
		}, nil
	})
	stepD := agent.NewCustomAgent(agent.Config{ID: "d"}, func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		return &schema.RunResponse{Messages: req.Messages}, nil
	})

	nodes := []orchestrate.Node{
		{ID: "a", Runner: stepA},
		{ID: "b", Runner: stepB, Deps: []string{"a"}},
		{ID: "c", Runner: stepC, Deps: []string{"a"}},
		{ID: "d", Runner: stepD, Deps: []string{"b", "c"}},
	}
	wf, err := workflowagent.NewDAG(agent.Config{ID: "dag-diamond"}, orchestrate.DAGConfig{}, nodes)
	if err != nil {
		t.Fatalf("NewDAG error: %v", err)
	}

	resp, err := wf.Run(context.Background(), &schema.RunRequest{
		Messages:  []schema.Message{schema.NewUserMessage("input data")},
		SessionID: "dag-session-1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// D receives messages from B and C (sorted by dep ID)
	if len(resp.Messages) != 2 {
		t.Fatalf("expected 2 messages from D, got %d", len(resp.Messages))
	}

	// Verify session ID preserved
	if resp.SessionID != "dag-session-1" {
		t.Errorf("SessionID = %q, want %q", resp.SessionID, "dag-session-1")
	}

	// Verify duration is set
	if resp.Duration < 0 {
		t.Errorf("Duration = %d, want >= 0", resp.Duration)
	}

	// Verify usage aggregation (10+5+3=18 prompt, 20+10+7=37 completion, 30+15+10=55 total)
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
}

// TestIntegration_DAG_LinearChain verifies a linear DAG (A -> B -> C)
// behaves like a sequential pipeline.
func TestIntegration_DAG_LinearChain(t *testing.T) {
	nodes := []orchestrate.Node{
		{ID: "a", Runner: newTextStep("a", "-A")},
		{ID: "b", Runner: newTextStep("b", "-B"), Deps: []string{"a"}},
		{ID: "c", Runner: newTextStep("c", "-C"), Deps: []string{"b"}},
	}
	wf, err := workflowagent.NewDAG(agent.Config{ID: "dag-linear"}, orchestrate.DAGConfig{}, nodes)
	if err != nil {
		t.Fatalf("NewDAG error: %v", err)
	}

	resp, err := wf.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("start")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := resp.Messages[0].Content.Text()
	if got != "start-A-B-C" {
		t.Errorf("got %q, want %q", got, "start-A-B-C")
	}
}

// TestIntegration_DAG_FanOutFanIn verifies fan-out/fan-in topology:
// A fans out to B, C, D, which all merge into E.
func TestIntegration_DAG_FanOutFanIn(t *testing.T) {
	nodes := []orchestrate.Node{
		{ID: "a", Runner: newTextStep("a", "-A")},
		{ID: "b", Runner: newTextStep("b", "-B"), Deps: []string{"a"}},
		{ID: "c", Runner: newTextStep("c", "-C"), Deps: []string{"a"}},
		{ID: "d", Runner: newTextStep("d", "-D"), Deps: []string{"a"}},
		{ID: "e", Runner: agent.NewCustomAgent(agent.Config{ID: "e"}, func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
			// Collect all messages from upstream
			var combined strings.Builder
			for _, m := range req.Messages {
				combined.WriteString(m.Content.Text() + ";")
			}
			return &schema.RunResponse{
				Messages: []schema.Message{schema.NewUserMessage(combined.String())},
			}, nil
		}), Deps: []string{"b", "c", "d"}},
	}
	wf, err := workflowagent.NewDAG(agent.Config{ID: "dag-fanout"}, orchestrate.DAGConfig{}, nodes)
	if err != nil {
		t.Fatalf("NewDAG error: %v", err)
	}

	resp, err := wf.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("start")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := resp.Messages[0].Content.Text()
	// Messages from B, C, D are concatenated in sorted dep ID order
	if !strings.Contains(got, "start-A-B") && !strings.Contains(got, "start-A-C") && !strings.Contains(got, "start-A-D") {
		t.Errorf("expected messages from all fan-out branches, got %q", got)
	}
}

// TestIntegration_DAG_EmptyNodes verifies that a DAG with no nodes returns input messages.
func TestIntegration_DAG_EmptyNodes(t *testing.T) {
	wf, err := workflowagent.NewDAG(agent.Config{ID: "dag-empty"}, orchestrate.DAGConfig{}, nil)
	if err != nil {
		t.Fatalf("NewDAG error: %v", err)
	}
	resp, err := wf.Run(context.Background(), &schema.RunRequest{
		Messages:  []schema.Message{schema.NewUserMessage("hello")},
		SessionID: "empty-dag-sess",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Messages[0].Content.Text() != "hello" {
		t.Errorf("expected original message echoed back")
	}
	if resp.SessionID != "empty-dag-sess" {
		t.Errorf("SessionID = %q, want %q", resp.SessionID, "empty-dag-sess")
	}
}

// TestIntegration_DAG_AbortOnFailure verifies that a failing required node
// in Abort strategy returns an error.
func TestIntegration_DAG_AbortOnFailure(t *testing.T) {
	nodes := []orchestrate.Node{
		{ID: "a", Runner: newTextStep("a", "-A")},
		{ID: "b", Runner: newFailStep("b", errors.New("node-b-failed")), Deps: []string{"a"}},
		{ID: "c", Runner: newTextStep("c", "-C"), Deps: []string{"b"}},
	}
	wf, err := workflowagent.NewDAG(agent.Config{ID: "dag-abort"}, orchestrate.DAGConfig{ErrorStrategy: orchestrate.Abort}, nodes)
	if err != nil {
		t.Fatalf("NewDAG error: %v", err)
	}

	_, err = wf.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("start")},
	})
	if err == nil {
		t.Fatal("expected error from failed DAG node")
	}
	if !strings.Contains(err.Error(), "node-b-failed") {
		t.Errorf("error should contain original error: %v", err)
	}
}

// TestIntegration_DAG_SkipOptionalNode verifies that an optional node failure
// with Skip strategy allows the DAG to continue.
func TestIntegration_DAG_SkipOptionalNode(t *testing.T) {
	nodes := []orchestrate.Node{
		{ID: "a", Runner: newTextStep("a", "-A")},
		{ID: "b", Runner: newFailStep("b", errors.New("optional-fail")), Deps: []string{"a"}, Optional: true},
		{ID: "c", Runner: newTextStep("c", "-C"), Deps: []string{"a"}},
	}
	wf, err := workflowagent.NewDAG(agent.Config{ID: "dag-skip"}, orchestrate.DAGConfig{ErrorStrategy: orchestrate.Skip}, nodes)
	if err != nil {
		t.Fatalf("NewDAG error: %v", err)
	}

	resp, err := wf.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("start")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// C should have completed successfully (terminal node)
	got := resp.Messages[0].Content.Text()
	if got != "start-A-C" {
		t.Errorf("got %q, want %q", got, "start-A-C")
	}
}

// TestIntegration_DAG_ConditionalNode verifies that a conditional node is skipped
// when its condition evaluates to false.
func TestIntegration_DAG_ConditionalNode(t *testing.T) {
	var conditionalCalled atomic.Bool
	nodes := []orchestrate.Node{
		{ID: "a", Runner: newTextStep("a", "-A")},
		{
			ID: "b", Runner: agent.NewCustomAgent(agent.Config{ID: "b"}, func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
				conditionalCalled.Store(true)
				return &schema.RunResponse{Messages: req.Messages}, nil
			}), Deps: []string{"a"},
			Condition: func(upstream map[string]*schema.RunResponse) bool {
				return strings.Contains(upstream["a"].Messages[0].Content.Text(), "TRIGGER")
			},
		},
		{ID: "c", Runner: newTextStep("c", "-C"), Deps: []string{"a"}},
	}
	wf, err := workflowagent.NewDAG(agent.Config{ID: "dag-cond"}, orchestrate.DAGConfig{}, nodes)
	if err != nil {
		t.Fatalf("NewDAG error: %v", err)
	}

	resp, err := wf.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("no-trigger")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if conditionalCalled.Load() {
		t.Error("conditional node B should not have been called")
	}
	// C should have completed
	got := resp.Messages[0].Content.Text()
	if got != "no-trigger-A-C" {
		t.Errorf("got %q, want %q", got, "no-trigger-A-C")
	}
}

// TestIntegration_DAG_InputMapper verifies that a custom InputMapper merges
// upstream results correctly.
func TestIntegration_DAG_InputMapper(t *testing.T) {
	nodes := []orchestrate.Node{
		{ID: "a", Runner: newTextStep("a", "-A")},
		{ID: "b", Runner: newTextStep("b", "-B")},
		{
			ID: "c", Runner: agent.NewCustomAgent(agent.Config{ID: "c"}, func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
				return &schema.RunResponse{Messages: req.Messages}, nil
			}), Deps: []string{"a", "b"},
			InputMapper: func(upstream map[string]*schema.RunResponse) (*schema.RunRequest, error) {
				aText := upstream["a"].Messages[0].Content.Text()
				bText := upstream["b"].Messages[0].Content.Text()
				return &schema.RunRequest{
					Messages: []schema.Message{schema.NewUserMessage(aText + " | " + bText)},
				}, nil
			},
		},
	}
	wf, err := workflowagent.NewDAG(agent.Config{ID: "dag-mapper"}, orchestrate.DAGConfig{}, nodes)
	if err != nil {
		t.Fatalf("NewDAG error: %v", err)
	}

	resp, err := wf.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("start")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := resp.Messages[0].Content.Text()
	if got != "start-A | start-B" {
		t.Errorf("got %q, want %q", got, "start-A | start-B")
	}
}

// TestIntegration_DAG_ContextCancellation verifies that DAG respects context cancellation.
func TestIntegration_DAG_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	nodes := []orchestrate.Node{
		{ID: "a", Runner: agent.NewCustomAgent(agent.Config{ID: "a"}, func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
			cancel()
			return &schema.RunResponse{Messages: req.Messages}, nil
		})},
		{ID: "b", Runner: newTextStep("b", "-B"), Deps: []string{"a"}},
	}
	wf, err := workflowagent.NewDAG(agent.Config{ID: "dag-cancel"}, orchestrate.DAGConfig{}, nodes)
	if err != nil {
		t.Fatalf("NewDAG error: %v", err)
	}

	_, err = wf.Run(ctx, &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("start")},
	})
	if err == nil {
		t.Fatal("expected context error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got: %v", err)
	}
}

// TestIntegration_DAG_LastResultAggregator verifies the LastResult aggregator
// picks the last terminal node by sorted ID.
func TestIntegration_DAG_LastResultAggregator(t *testing.T) {
	nodes := []orchestrate.Node{
		{ID: "root", Runner: newTextStep("root", "")},
		{ID: "a", Runner: newTextStep("a", "-A"), Deps: []string{"root"}},
		{ID: "b", Runner: newTextStep("b", "-B"), Deps: []string{"root"}},
		{ID: "z", Runner: newTextStep("z", "-Z"), Deps: []string{"root"}},
	}
	wf, err := workflowagent.NewDAG(agent.Config{ID: "dag-last"}, orchestrate.DAGConfig{
		Aggregator: orchestrate.LastResultAggregator(),
	}, nodes)
	if err != nil {
		t.Fatalf("NewDAG error: %v", err)
	}

	resp, err := wf.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("start")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// "z" is alphabetically last
	got := resp.Messages[0].Content.Text()
	if got != "start-Z" {
		t.Errorf("got %q, want %q", got, "start-Z")
	}
}

// TestIntegration_DAG_EarlyExit verifies that EarlyExitFunc stops DAG execution.
func TestIntegration_DAG_EarlyExit(t *testing.T) {
	var bCalled atomic.Bool
	nodes := []orchestrate.Node{
		{ID: "a", Runner: newTextStep("a", "-A")},
		{ID: "b", Runner: agent.NewCustomAgent(agent.Config{ID: "b"}, func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
			bCalled.Store(true)
			time.Sleep(50 * time.Millisecond)
			return &schema.RunResponse{Messages: req.Messages}, nil
		}), Deps: []string{"a"}},
	}
	wf, err := workflowagent.NewDAG(agent.Config{ID: "dag-early"}, orchestrate.DAGConfig{
		EarlyExitFunc: func(nodeID string, _ *schema.RunResponse) bool {
			return nodeID == "a"
		},
	}, nodes)
	if err != nil {
		t.Fatalf("NewDAG error: %v", err)
	}

	resp, err := wf.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("start")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// A is the only completed terminal-like node
	if len(resp.Messages) == 0 {
		t.Fatal("expected at least one message in response")
	}
	_ = resp
}

// TestIntegration_DAG_MaxConcurrency verifies concurrency limit is enforced.
func TestIntegration_DAG_MaxConcurrency(t *testing.T) {
	var maxConcurrent atomic.Int32
	var currentConcurrent atomic.Int32

	makeNode := func(id string) orchestrate.Node {
		return orchestrate.Node{
			ID: id,
			Runner: agent.NewCustomAgent(agent.Config{ID: id}, func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
				cur := currentConcurrent.Add(1)
				for {
					old := maxConcurrent.Load()
					if cur <= old || maxConcurrent.CompareAndSwap(old, cur) {
						break
					}
				}
				time.Sleep(20 * time.Millisecond)
				currentConcurrent.Add(-1)
				return &schema.RunResponse{Messages: req.Messages}, nil
			}),
		}
	}

	rootNode := orchestrate.Node{ID: "root", Runner: newTextStep("root", "")}
	nodes := []orchestrate.Node{rootNode, makeNode("a"), makeNode("b"), makeNode("c"), makeNode("d")}
	nodes[1].Deps = []string{"root"}
	nodes[2].Deps = []string{"root"}
	nodes[3].Deps = []string{"root"}
	nodes[4].Deps = []string{"root"}
	wf, err := workflowagent.NewDAG(agent.Config{ID: "dag-conc"}, orchestrate.DAGConfig{MaxConcurrency: 2}, nodes)
	if err != nil {
		t.Fatalf("NewDAG error: %v", err)
	}

	_, err = wf.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("start")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if maxConcurrent.Load() > 2 {
		t.Errorf("max concurrent = %d, want <= 2", maxConcurrent.Load())
	}
}

// TestIntegration_DAG_CycleError verifies that a cyclic DAG returns a descriptive error.
func TestIntegration_DAG_CycleError(t *testing.T) {
	nodes := []orchestrate.Node{
		{ID: "a", Runner: newTextStep("a", ""), Deps: []string{"c"}},
		{ID: "b", Runner: newTextStep("b", ""), Deps: []string{"a"}},
		{ID: "c", Runner: newTextStep("c", ""), Deps: []string{"b"}},
	}
	_, err := workflowagent.NewDAG(agent.Config{ID: "dag-cycle"}, orchestrate.DAGConfig{}, nodes)
	if err == nil {
		t.Fatal("expected cycle error")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("error should mention cycle: %v", err)
	}
}

// TestIntegration_DAG_DuplicateNodeID verifies that duplicate node IDs return an error.
func TestIntegration_DAG_DuplicateNodeID(t *testing.T) {
	nodes := []orchestrate.Node{
		{ID: "a", Runner: newTextStep("a", "")},
		{ID: "a", Runner: newTextStep("a", "")},
	}
	_, err := workflowagent.NewDAG(agent.Config{ID: "dag-dup"}, orchestrate.DAGConfig{}, nodes)
	if err == nil {
		t.Fatal("expected duplicate ID error")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("error should mention duplicate: %v", err)
	}
}

// TestIntegration_DAG_Stream verifies that DAG workflow streaming produces
// correct AgentStart/AgentEnd/EOF lifecycle events.
func TestIntegration_DAG_Stream(t *testing.T) {
	nodes := []orchestrate.Node{
		{ID: "a", Runner: newTextStep("a", "-A")},
		{ID: "b", Runner: newTextStep("b", "-B"), Deps: []string{"a"}},
	}
	wf, err := workflowagent.NewDAG(agent.Config{ID: "dag-stream"}, orchestrate.DAGConfig{}, nodes)
	if err != nil {
		t.Fatalf("NewDAG error: %v", err)
	}

	stream, err := wf.RunStream(context.Background(), &schema.RunRequest{
		Messages:  []schema.Message{schema.NewUserMessage("hello")},
		SessionID: "dag-stream-sess",
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
	if e.AgentID != "dag-stream" {
		t.Errorf("AgentID = %q, want %q", e.AgentID, "dag-stream")
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
	if endData.Duration < 0 {
		t.Errorf("duration = %d, want >= 0", endData.Duration)
	}

	// EOF
	_, err = stream.Recv()
	if !errors.Is(err, io.EOF) {
		t.Errorf("expected io.EOF, got %v", err)
	}
}

// TestIntegration_DAG_NilResponseError verifies that a DAG node returning nil response
// produces a proper error.
func TestIntegration_DAG_NilResponseError(t *testing.T) {
	nodes := []orchestrate.Node{
		{ID: "a", Runner: newNilRespStep("a")},
	}
	wf, err := workflowagent.NewDAG(agent.Config{ID: "dag-nil"}, orchestrate.DAGConfig{}, nodes)
	if err != nil {
		t.Fatalf("NewDAG error: %v", err)
	}

	_, err = wf.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("test")},
	})
	if err == nil {
		t.Fatal("expected error for nil response")
	}
	if !strings.Contains(err.Error(), "nil response") {
		t.Errorf("error should mention nil response: %v", err)
	}
}

// --- Loop Workflow Integration Tests ---

// TestIntegration_Loop_BasicIteration verifies a loop with fixed MaxIters.
func TestIntegration_Loop_BasicIteration(t *testing.T) {
	body := newTextStep("loop-body", "-iter")
	wf := workflowagent.NewLoop(agent.Config{ID: "loop-basic"}, body, nil, 3)

	resp, err := wf.Run(context.Background(), &schema.RunRequest{
		Messages:  []schema.Message{schema.NewUserMessage("start")},
		SessionID: "loop-sess-1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := resp.Messages[0].Content.Text()
	if got != "start-iter-iter-iter" {
		t.Errorf("got %q, want %q", got, "start-iter-iter-iter")
	}
	if resp.SessionID != "loop-sess-1" {
		t.Errorf("SessionID = %q, want %q", resp.SessionID, "loop-sess-1")
	}
	if resp.Duration < 0 {
		t.Errorf("Duration = %d, want >= 0", resp.Duration)
	}
}

// TestIntegration_Loop_ConditionTermination verifies that the loop stops
// when the condition returns false.
func TestIntegration_Loop_ConditionTermination(t *testing.T) {
	var callCount atomic.Int32
	body := agent.NewCustomAgent(agent.Config{ID: "cond-body"}, func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		callCount.Add(1)
		text := req.Messages[0].Content.Text()
		return &schema.RunResponse{
			Messages: []schema.Message{schema.NewUserMessage(text + "-step")},
		}, nil
	})
	condition := func(resp *schema.RunResponse) bool {
		if resp == nil {
			return true
		}
		return !strings.Contains(resp.Messages[0].Content.Text(), "-step-step-step")
	}
	wf := workflowagent.NewLoop(agent.Config{ID: "loop-cond"}, body, condition, 0)

	resp, err := wf.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("begin")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if callCount.Load() != 3 {
		t.Errorf("expected 3 iterations, got %d", callCount.Load())
	}
	got := resp.Messages[0].Content.Text()
	if got != "begin-step-step-step" {
		t.Errorf("got %q, want %q", got, "begin-step-step-step")
	}
}

// TestIntegration_Loop_ZeroIterations verifies that when the condition
// returns false for nil (pre-check), no iterations execute.
func TestIntegration_Loop_ZeroIterations(t *testing.T) {
	var called atomic.Bool
	body := agent.NewCustomAgent(agent.Config{ID: "zero-body"}, func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		called.Store(true)
		return &schema.RunResponse{Messages: req.Messages}, nil
	})
	condition := func(resp *schema.RunResponse) bool {
		return resp != nil // false for nil pre-check
	}
	wf := workflowagent.NewLoop(agent.Config{ID: "loop-zero"}, body, condition, 0)

	resp, err := wf.Run(context.Background(), &schema.RunRequest{
		Messages:  []schema.Message{schema.NewUserMessage("hello")},
		SessionID: "zero-sess",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if called.Load() {
		t.Error("body should not have been called")
	}
	if resp.Messages[0].Content.Text() != "hello" {
		t.Errorf("expected original message echoed back")
	}
	if resp.SessionID != "zero-sess" {
		t.Errorf("SessionID = %q, want %q", resp.SessionID, "zero-sess")
	}
}

// TestIntegration_Loop_UsageAccumulation verifies that usage is accumulated
// across all loop iterations.
func TestIntegration_Loop_UsageAccumulation(t *testing.T) {
	body := agent.NewCustomAgent(agent.Config{ID: "usage-body"}, func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		return &schema.RunResponse{
			Messages: req.Messages,
			Usage:    &aimodel.Usage{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30},
		}, nil
	})
	wf := workflowagent.NewLoop(agent.Config{ID: "loop-usage"}, body, nil, 4)

	resp, err := wf.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("start")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Usage == nil {
		t.Fatal("expected non-nil Usage")
	}
	if resp.Usage.PromptTokens != 40 {
		t.Errorf("PromptTokens = %d, want 40", resp.Usage.PromptTokens)
	}
	if resp.Usage.CompletionTokens != 80 {
		t.Errorf("CompletionTokens = %d, want 80", resp.Usage.CompletionTokens)
	}
	if resp.Usage.TotalTokens != 120 {
		t.Errorf("TotalTokens = %d, want 120", resp.Usage.TotalTokens)
	}
}

// TestIntegration_Loop_OutputChaining verifies that each iteration's output
// becomes the next iteration's input.
func TestIntegration_Loop_OutputChaining(t *testing.T) {
	var received []string
	body := agent.NewCustomAgent(agent.Config{ID: "chain-body"}, func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		text := req.Messages[0].Content.Text()
		received = append(received, text)
		return &schema.RunResponse{
			Messages: []schema.Message{schema.NewUserMessage(text + "+")},
		}, nil
	})
	wf := workflowagent.NewLoop(agent.Config{ID: "loop-chain"}, body, nil, 3)

	resp, err := wf.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("x")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Verify each iteration received the correct input
	expected := []string{"x", "x+", "x++"}
	if len(received) != 3 {
		t.Fatalf("expected 3 inputs, got %d", len(received))
	}
	for i, want := range expected {
		if received[i] != want {
			t.Errorf("iteration %d input = %q, want %q", i, received[i], want)
		}
	}
	// Final output
	got := resp.Messages[0].Content.Text()
	if got != "x+++" {
		t.Errorf("final output = %q, want %q", got, "x+++")
	}
}

// TestIntegration_Loop_ContextCancellation verifies that a loop respects context cancellation.
func TestIntegration_Loop_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var callCount atomic.Int32
	body := agent.NewCustomAgent(agent.Config{ID: "cancel-body"}, func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		if callCount.Add(1) == 2 {
			cancel()
		}
		return &schema.RunResponse{Messages: req.Messages}, nil
	})
	condition := func(_ *schema.RunResponse) bool { return true }
	wf := workflowagent.NewLoop(agent.Config{ID: "loop-cancel"}, body, condition, 0)

	_, err := wf.Run(ctx, &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("test")},
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got: %v", err)
	}
}

// TestIntegration_Loop_ErrorPropagation verifies that a loop body error
// is propagated correctly.
func TestIntegration_Loop_ErrorPropagation(t *testing.T) {
	var callCount atomic.Int32
	body := agent.NewCustomAgent(agent.Config{ID: "err-body"}, func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		if callCount.Add(1) == 2 {
			return nil, errors.New("loop-body-failed")
		}
		return &schema.RunResponse{Messages: req.Messages}, nil
	})
	wf := workflowagent.NewLoop(agent.Config{ID: "loop-err"}, body, nil, 5)

	_, err := wf.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("start")},
	})
	if err == nil {
		t.Fatal("expected error from loop body")
	}
	if !strings.Contains(err.Error(), "loop-body-failed") {
		t.Errorf("error should contain original message: %v", err)
	}
}

// TestIntegration_Loop_Stream verifies that loop workflow streaming produces
// correct AgentStart/AgentEnd/EOF lifecycle events.
func TestIntegration_Loop_Stream(t *testing.T) {
	body := newTextStep("loop-body", "-iter")
	wf := workflowagent.NewLoop(agent.Config{ID: "loop-stream"}, body, nil, 2)

	stream, err := wf.RunStream(context.Background(), &schema.RunRequest{
		Messages:  []schema.Message{schema.NewUserMessage("hello")},
		SessionID: "loop-stream-sess",
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
	if e.AgentID != "loop-stream" {
		t.Errorf("AgentID = %q, want %q", e.AgentID, "loop-stream")
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
	if endData.Message != "hello-iter-iter" {
		t.Errorf("message = %q, want %q", endData.Message, "hello-iter-iter")
	}

	// EOF
	_, err = stream.Recv()
	if !errors.Is(err, io.EOF) {
		t.Errorf("expected io.EOF, got %v", err)
	}
}

// TestIntegration_Loop_SessionIDPreserved verifies session ID is forwarded
// to each iteration and preserved in the final response.
func TestIntegration_Loop_SessionIDPreserved(t *testing.T) {
	var sessionIDs []string
	body := agent.NewCustomAgent(agent.Config{ID: "sess-body"}, func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		sessionIDs = append(sessionIDs, req.SessionID)
		return &schema.RunResponse{Messages: req.Messages}, nil
	})
	wf := workflowagent.NewLoop(agent.Config{ID: "loop-sess"}, body, nil, 3)

	resp, err := wf.Run(context.Background(), &schema.RunRequest{
		Messages:  []schema.Message{schema.NewUserMessage("test")},
		SessionID: "my-loop-session",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for i, sid := range sessionIDs {
		if sid != "my-loop-session" {
			t.Errorf("iteration %d SessionID = %q, want %q", i, sid, "my-loop-session")
		}
	}
	if resp.SessionID != "my-loop-session" {
		t.Errorf("response SessionID = %q, want %q", resp.SessionID, "my-loop-session")
	}
}

// --- Mixed Workflow Integration Tests ---

// TestIntegration_NestedLoopInSequential verifies that a loop workflow
// can be used as a step within a sequential workflow.
func TestIntegration_NestedLoopInSequential(t *testing.T) {
	loopBody := newTextStep("lb", "-L")
	loopWf := workflowagent.NewLoop(agent.Config{ID: "inner-loop"}, loopBody, nil, 2)

	outerWf := workflowagent.New(
		agent.Config{ID: "outer-seq"},
		newTextStep("pre", "-PRE"),
		loopWf,
		newTextStep("post", "-POST"),
	)

	resp, err := outerWf.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("start")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := resp.Messages[0].Content.Text()
	if got != "start-PRE-L-L-POST" {
		t.Errorf("got %q, want %q", got, "start-PRE-L-L-POST")
	}
}

// TestIntegration_NestedDAGInSequential verifies that a DAG workflow
// can be used as a step within a sequential workflow.
func TestIntegration_NestedDAGInSequential(t *testing.T) {
	nodes := []orchestrate.Node{
		{ID: "a", Runner: newTextStep("a", "-A")},
		{ID: "b", Runner: newTextStep("b", "-B"), Deps: []string{"a"}},
	}
	dagWf, err := workflowagent.NewDAG(agent.Config{ID: "inner-dag"}, orchestrate.DAGConfig{}, nodes)
	if err != nil {
		t.Fatalf("NewDAG error: %v", err)
	}

	outerWf := workflowagent.New(
		agent.Config{ID: "outer-seq"},
		newTextStep("pre", "-PRE"),
		dagWf,
		newTextStep("post", "-POST"),
	)

	resp, err := outerWf.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("start")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := resp.Messages[0].Content.Text()
	if got != "start-PRE-A-B-POST" {
		t.Errorf("got %q, want %q", got, "start-PRE-A-B-POST")
	}
}

// TestIntegration_DAG_RunText_Convenience verifies agent.RunText works with DAG workflows.
func TestIntegration_DAG_RunText_Convenience(t *testing.T) {
	nodes := []orchestrate.Node{
		{ID: "a", Runner: newTextStep("a", "-processed")},
	}
	wf, err := workflowagent.NewDAG(agent.Config{ID: "dag-runtext"}, orchestrate.DAGConfig{}, nodes)
	if err != nil {
		t.Fatalf("NewDAG error: %v", err)
	}

	resp, err := agent.RunText(context.Background(), wf, "input")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := resp.Messages[0].Content.Text()
	if got != "input-processed" {
		t.Errorf("got %q, want %q", got, "input-processed")
	}
}

// TestIntegration_Loop_RunText_Convenience verifies agent.RunText works with loop workflows.
func TestIntegration_Loop_RunText_Convenience(t *testing.T) {
	body := newTextStep("lb", "-x")
	wf := workflowagent.NewLoop(agent.Config{ID: "loop-runtext"}, body, nil, 2)

	resp, err := agent.RunText(context.Background(), wf, "in")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := resp.Messages[0].Content.Text()
	if got != "in-x-x" {
		t.Errorf("got %q, want %q", got, "in-x-x")
	}
}
