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

package orchestrate

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/vogo/vage/schema"
)

func TestExecuteLoop_BasicIteration(t *testing.T) {
	loop := LoopNode{
		Body:     appendRunner("-iter"),
		MaxIters: 3,
	}
	resp, err := ExecuteLoop(context.Background(), loop, makeReq("start"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := resp.Messages[0].Text()
	if got != "start-iter-iter-iter" {
		t.Errorf("got %q, want %q", got, "start-iter-iter-iter")
	}
}

func TestExecuteLoop_ConditionTermination(t *testing.T) {
	callCount := 0
	runner := newMockRunner(func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		callCount++
		text := req.Messages[0].Text()
		return &schema.RunResponse{
			Messages: []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, text+"-iter")},
		}, nil
	})

	loop := LoopNode{
		Body: runner,
		Condition: func(resp *schema.RunResponse) bool {
			if resp == nil {
				return true
			}
			return !strings.Contains(resp.Messages[0].Text(), "-iter-iter")
		},
	}
	resp, err := ExecuteLoop(context.Background(), loop, makeReq("start"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if callCount != 2 {
		t.Errorf("expected 2 iterations, got %d", callCount)
	}
	got := resp.Messages[0].Text()
	if got != "start-iter-iter" {
		t.Errorf("got %q, want %q", got, "start-iter-iter")
	}
}

func TestExecuteLoop_ConvergenceDetection(t *testing.T) {
	callCount := 0
	runner := newMockRunner(func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		callCount++
		if callCount >= 3 {
			return &schema.RunResponse{
				Messages: []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "stable")},
			}, nil
		}
		return &schema.RunResponse{
			Messages: []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, req.Messages[0].Text()+"-change")},
		}, nil
	})

	loop := LoopNode{
		Body: runner,
		ConvergenceFunc: func(prev, curr *schema.RunResponse) bool {
			return prev.Messages[0].Text() == curr.Messages[0].Text()
		},
	}
	resp, err := ExecuteLoop(context.Background(), loop, makeReq("start"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if callCount != 4 {
		t.Errorf("expected 4 iterations (2 changes + 2 stable), got %d", callCount)
	}
	got := resp.Messages[0].Text()
	if got != "stable" {
		t.Errorf("got %q, want %q", got, "stable")
	}
}

func TestExecuteLoop_MaxItersSafety(t *testing.T) {
	callCount := 0
	runner := newMockRunner(func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		callCount++
		return &schema.RunResponse{Messages: req.Messages}, nil
	})

	loop := LoopNode{
		Body:     runner,
		MaxIters: 5,
		Condition: func(_ *schema.RunResponse) bool {
			return true // always continue
		},
	}
	_, err := ExecuteLoop(context.Background(), loop, makeReq("start"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if callCount != 5 {
		t.Errorf("expected 5 iterations, got %d", callCount)
	}
}

func TestExecuteLoop_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	callCount := 0
	runner := newMockRunner(func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		callCount++
		if callCount == 2 {
			cancel()
		}
		return &schema.RunResponse{Messages: req.Messages}, nil
	})

	loop := LoopNode{
		Body: runner,
		Condition: func(_ *schema.RunResponse) bool {
			return true
		},
	}
	_, err := ExecuteLoop(ctx, loop, makeReq("start"))
	if err == nil {
		t.Fatal("expected context error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got: %v", err)
	}
}

func TestExecuteLoop_UsageAccumulation(t *testing.T) {
	loop := LoopNode{
		Body:     usageRunner(10, 20, 30),
		MaxIters: 3,
	}
	resp, err := ExecuteLoop(context.Background(), loop, makeReq("start"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Usage == nil {
		t.Fatal("expected non-nil Usage")
	}
	if resp.Usage.PromptTokens != 30 {
		t.Errorf("PromptTokens = %d, want 30", resp.Usage.PromptTokens)
	}
	if resp.Usage.CompletionTokens != 60 {
		t.Errorf("CompletionTokens = %d, want 60", resp.Usage.CompletionTokens)
	}
	if resp.Usage.TotalTokens != 90 {
		t.Errorf("TotalTokens = %d, want 90", resp.Usage.TotalTokens)
	}
}

func TestExecuteLoop_ZeroIterations(t *testing.T) {
	callCount := 0
	runner := newMockRunner(func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		callCount++
		return &schema.RunResponse{Messages: req.Messages}, nil
	})

	loop := LoopNode{
		Body: runner,
		Condition: func(resp *schema.RunResponse) bool {
			return resp != nil // returns false for nil (pre-check)
		},
	}
	resp, err := ExecuteLoop(context.Background(), loop, makeReq("hello"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if callCount != 0 {
		t.Errorf("expected 0 iterations, got %d", callCount)
	}
	if resp.Messages[0].Text() != "hello" {
		t.Errorf("expected input message echoed back")
	}
}

func TestExecuteLoop_OutputChaining(t *testing.T) {
	var received []string
	runner := newMockRunner(func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		text := req.Messages[0].Text()
		received = append(received, text)
		return &schema.RunResponse{
			Messages: []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, text+"+")},
		}, nil
	})

	loop := LoopNode{
		Body:     runner,
		MaxIters: 3,
	}
	_, err := ExecuteLoop(context.Background(), loop, makeReq("start"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := []string{"start", "start+", "start++"}
	if len(received) != 3 {
		t.Fatalf("expected 3 inputs, got %d", len(received))
	}
	for i, want := range expected {
		if received[i] != want {
			t.Errorf("iteration %d input = %q, want %q", i, received[i], want)
		}
	}
}

func TestExecuteLoop_NilResponse(t *testing.T) {
	loop := LoopNode{
		Body:     nilResponseRunner(),
		MaxIters: 1,
	}
	_, err := ExecuteLoop(context.Background(), loop, makeReq("start"))
	if err == nil {
		t.Fatal("expected error for nil response")
	}
	if !strings.Contains(err.Error(), "nil response") {
		t.Errorf("error should mention nil response: %v", err)
	}
}

func TestExecuteLoop_NilBody(t *testing.T) {
	loop := LoopNode{MaxIters: 1}
	_, err := ExecuteLoop(context.Background(), loop, makeReq("start"))
	if err == nil {
		t.Fatal("expected error for nil body")
	}
}

func TestExecuteLoop_SessionIDPreserved(t *testing.T) {
	var receivedSession string
	runner := newMockRunner(func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		receivedSession = req.SessionID
		return &schema.RunResponse{Messages: req.Messages}, nil
	})

	loop := LoopNode{
		Body:     runner,
		MaxIters: 2,
	}
	req := &schema.RunRequest{
		Messages:  []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "hello")},
		SessionID: "loop-session",
	}
	resp, err := ExecuteLoop(context.Background(), loop, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if receivedSession != "loop-session" {
		t.Errorf("session = %q, want %q", receivedSession, "loop-session")
	}
	if resp.SessionID != "loop-session" {
		t.Errorf("response session = %q, want %q", resp.SessionID, "loop-session")
	}
}

func TestExecuteLoop_NoUsageWhenNoneProvided(t *testing.T) {
	loop := LoopNode{
		Body:     passthroughRunner(),
		MaxIters: 2,
	}
	resp, err := ExecuteLoop(context.Background(), loop, makeReq("start"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Usage != nil {
		t.Error("expected nil Usage when no runner provides usage")
	}
}

func TestExecuteLoop_MixedUsage(t *testing.T) {
	callCount := 0
	runner := newMockRunner(func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		callCount++
		resp := &schema.RunResponse{Messages: req.Messages}
		if callCount%2 == 1 {
			resp.Usage = &schema.Usage{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30}
		}
		return resp, nil
	})

	loop := LoopNode{
		Body:     runner,
		MaxIters: 4,
	}
	resp, err := ExecuteLoop(context.Background(), loop, makeReq("start"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Usage == nil {
		t.Fatal("expected non-nil Usage")
	}
	// 2 iterations with usage (1 and 3)
	if resp.Usage.PromptTokens != 20 {
		t.Errorf("PromptTokens = %d, want 20", resp.Usage.PromptTokens)
	}
}
