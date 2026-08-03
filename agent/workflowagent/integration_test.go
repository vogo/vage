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
	"sync/atomic"
	"testing"
	"time"

	"github.com/vogo/vage/agent"
	"github.com/vogo/vage/orchestrate"
	"github.com/vogo/vage/schema"
)

// --- Integration test helpers ---

func intMakeStep(id, suffix string) agent.Agent {
	return agent.NewCustomAgent(agent.Config{ID: id}, func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		text := ""
		if len(req.Messages) > 0 {
			text = req.Messages[0].Text()
		}
		return &schema.RunResponse{
			Messages: []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, text+suffix)},
		}, nil
	})
}

func intMakeStepWithUsage(id string, prompt, completion, total int) agent.Agent {
	return agent.NewCustomAgent(agent.Config{ID: id}, func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		return &schema.RunResponse{
			Messages: req.Messages,
			Usage:    &schema.Usage{PromptTokens: prompt, CompletionTokens: completion, TotalTokens: total},
		}, nil
	})
}

func intMakePassthrough(id string) agent.Agent {
	return agent.NewCustomAgent(agent.Config{ID: id}, func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		return &schema.RunResponse{Messages: req.Messages}, nil
	})
}

func intMakeError(id string, err error) agent.Agent {
	return agent.NewCustomAgent(agent.Config{ID: id}, func(_ context.Context, _ *schema.RunRequest) (*schema.RunResponse, error) {
		return nil, err
	})
}

// =============================================================================
// Integration tests: DAG workflows through workflowagent
// =============================================================================

// TestIntegration_DAG_LinearChain tests a 3-node linear DAG (A->B->C) through workflowagent,
// verifying output chaining works end-to-end.
func TestIntegration_DAG_LinearChain(t *testing.T) {
	nodes := []orchestrate.Node{
		{ID: "A", Runner: intMakeStep("a", "-A")},
		{ID: "B", Runner: intMakeStep("b", "-B"), Deps: []string{"A"}},
		{ID: "C", Runner: intMakeStep("c", "-C"), Deps: []string{"B"}},
	}
	wf, err := NewDAG(agent.Config{ID: "wf-dag-linear"}, orchestrate.DAGConfig{}, nodes)
	if err != nil {
		t.Fatalf("NewDAG error: %v", err)
	}
	resp, err := wf.Run(context.Background(), &schema.RunRequest{
		Messages:  []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "start")},
		SessionID: "int-session",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := resp.Messages[0].Text()
	if got != "start-A-B-C" {
		t.Errorf("got %q, want %q", got, "start-A-B-C")
	}
	if resp.SessionID != "int-session" {
		t.Errorf("SessionID = %q, want %q", resp.SessionID, "int-session")
	}
	if resp.Duration < 0 {
		t.Errorf("Duration should be >= 0, got %d", resp.Duration)
	}
}

// TestIntegration_DAG_DiamondWithUsage tests a diamond DAG pattern where B and C run in parallel
// after A, then D fans in, with usage accumulation across all nodes.
func TestIntegration_DAG_DiamondWithUsage(t *testing.T) {
	stepA := agent.NewCustomAgent(agent.Config{ID: "a"}, func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		text := req.Messages[0].Text()
		return &schema.RunResponse{
			Messages: []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, text+"-A")},
			Usage:    &schema.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
		}, nil
	})
	stepB := agent.NewCustomAgent(agent.Config{ID: "b"}, func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		text := req.Messages[0].Text()
		return &schema.RunResponse{
			Messages: []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, text+"-B")},
			Usage:    &schema.Usage{PromptTokens: 20, CompletionTokens: 10, TotalTokens: 30},
		}, nil
	})
	stepC := agent.NewCustomAgent(agent.Config{ID: "c"}, func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		text := req.Messages[0].Text()
		return &schema.RunResponse{
			Messages: []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, text+"-C")},
			Usage:    &schema.Usage{PromptTokens: 15, CompletionTokens: 8, TotalTokens: 23},
		}, nil
	})
	stepD := intMakePassthrough("d")

	nodes := []orchestrate.Node{
		{ID: "A", Runner: stepA},
		{ID: "B", Runner: stepB, Deps: []string{"A"}},
		{ID: "C", Runner: stepC, Deps: []string{"A"}},
		{ID: "D", Runner: stepD, Deps: []string{"B", "C"}},
	}
	wf, err := NewDAG(agent.Config{ID: "wf-dag"}, orchestrate.DAGConfig{}, nodes)
	if err != nil {
		t.Fatalf("NewDAG error: %v", err)
	}
	resp, err := wf.Run(context.Background(), &schema.RunRequest{
		Messages:  []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "start")},
		SessionID: "diamond-session",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// D gets messages from B and C (sorted alphabetically: B, C)
	if len(resp.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(resp.Messages))
	}

	// Usage should be accumulated from all 4 nodes (A + B + C + D, D has no usage)
	if resp.Usage == nil {
		t.Fatal("expected non-nil Usage")
	}
	if resp.Usage.PromptTokens != 45 {
		t.Errorf("PromptTokens = %d, want 45", resp.Usage.PromptTokens)
	}
	if resp.Usage.CompletionTokens != 23 {
		t.Errorf("CompletionTokens = %d, want 23", resp.Usage.CompletionTokens)
	}
	if resp.SessionID != "diamond-session" {
		t.Errorf("SessionID = %q, want %q", resp.SessionID, "diamond-session")
	}
}

// TestIntegration_DAG_FanOutFanIn tests fan-out from root to many nodes, then fan-in to a single terminal.
func TestIntegration_DAG_FanOutFanIn(t *testing.T) {
	nodes := []orchestrate.Node{
		{ID: "root", Runner: intMakeStep("root", "-root")},
		{ID: "w1", Runner: intMakeStep("w1", "-w1"), Deps: []string{"root"}},
		{ID: "w2", Runner: intMakeStep("w2", "-w2"), Deps: []string{"root"}},
		{ID: "w3", Runner: intMakeStep("w3", "-w3"), Deps: []string{"root"}},
		{ID: "merge", Runner: intMakePassthrough("merge"), Deps: []string{"w1", "w2", "w3"}},
	}
	wf, err := NewDAG(agent.Config{ID: "wf"}, orchestrate.DAGConfig{}, nodes)
	if err != nil {
		t.Fatalf("NewDAG error: %v", err)
	}
	resp, err := wf.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "in")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// merge gets 3 messages from w1, w2, w3 (sorted alphabetically)
	if len(resp.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(resp.Messages))
	}
}

// TestIntegration_DAG_ConditionalSkip tests that a conditional node is skipped when its condition
// returns false, and downstream nodes still execute.
func TestIntegration_DAG_ConditionalSkip(t *testing.T) {
	stepA := intMakeStep("a", "-no")
	stepB := intMakeStep("b", "-B")
	stepC := intMakeStep("c", "-C")

	nodes := []orchestrate.Node{
		{ID: "A", Runner: stepA},
		{
			ID: "B", Runner: stepB, Deps: []string{"A"},
			Condition: func(upstream map[string]*schema.RunResponse) bool {
				return strings.Contains(upstream["A"].Messages[0].Text(), "yes")
			},
		},
		{ID: "C", Runner: stepC, Deps: []string{"A"}},
	}
	wf, err := NewDAG(agent.Config{ID: "wf"}, orchestrate.DAGConfig{}, nodes)
	if err != nil {
		t.Fatalf("NewDAG error: %v", err)
	}
	resp, err := wf.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "start")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Only C should be in the output (B was skipped)
	got := resp.Messages[0].Text()
	if got != "start-no-C" {
		t.Errorf("got %q, want %q", got, "start-no-C")
	}
}

// TestIntegration_DAG_ConditionalExecute tests that a conditional node executes when its condition
// returns true.
func TestIntegration_DAG_ConditionalExecute(t *testing.T) {
	stepA := intMakeStep("a", "-yes")
	stepB := intMakeStep("b", "-B")

	nodes := []orchestrate.Node{
		{ID: "A", Runner: stepA},
		{
			ID: "B", Runner: stepB, Deps: []string{"A"},
			Condition: func(upstream map[string]*schema.RunResponse) bool {
				return strings.Contains(upstream["A"].Messages[0].Text(), "yes")
			},
		},
	}
	wf, err := NewDAG(agent.Config{ID: "wf"}, orchestrate.DAGConfig{}, nodes)
	if err != nil {
		t.Fatalf("NewDAG error: %v", err)
	}
	resp, err := wf.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "start")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := resp.Messages[0].Text()
	if got != "start-yes-B" {
		t.Errorf("got %q, want %q", got, "start-yes-B")
	}
}

// TestIntegration_DAG_AbortStrategy tests that a required node failure aborts the DAG
// and propagates the error through workflowagent.
func TestIntegration_DAG_AbortStrategy(t *testing.T) {
	stepRoot := intMakePassthrough("root")
	stepA := intMakeError("a", errors.New("critical failure"))
	stepB := intMakeStep("b", "-B")

	nodes := []orchestrate.Node{
		{ID: "root", Runner: stepRoot},
		{ID: "A", Runner: stepA, Deps: []string{"root"}},
		{ID: "B", Runner: stepB, Deps: []string{"root"}},
	}
	wf, err := NewDAG(agent.Config{ID: "wf"}, orchestrate.DAGConfig{ErrorStrategy: orchestrate.Abort}, nodes)
	if err != nil {
		t.Fatalf("NewDAG error: %v", err)
	}
	_, err = wf.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "start")},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "critical failure") {
		t.Errorf("error should contain original message: %v", err)
	}
}

// TestIntegration_DAG_SkipOptional tests that an optional node failure with Skip strategy
// allows the DAG to continue and complete successfully.
func TestIntegration_DAG_SkipOptional(t *testing.T) {
	stepA := intMakeStep("a", "-A")
	stepFail := intMakeError("fail", errors.New("optional failure"))
	stepC := intMakeStep("c", "-C")

	nodes := []orchestrate.Node{
		{ID: "A", Runner: stepA},
		{ID: "B", Runner: stepFail, Deps: []string{"A"}, Optional: true},
		{ID: "C", Runner: stepC, Deps: []string{"A"}},
	}
	wf, err := NewDAG(agent.Config{ID: "wf"}, orchestrate.DAGConfig{ErrorStrategy: orchestrate.Skip}, nodes)
	if err != nil {
		t.Fatalf("NewDAG error: %v", err)
	}
	resp, err := wf.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "start")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// C should have completed successfully
	got := resp.Messages[0].Text()
	if got != "start-A-C" {
		t.Errorf("got %q, want %q", got, "start-A-C")
	}
}

// TestIntegration_DAG_InputMapper tests custom InputMapper that merges upstream results.
func TestIntegration_DAG_InputMapper(t *testing.T) {
	stepA := intMakeStep("a", "-A")
	stepB := intMakeStep("b", "-B")

	nodes := []orchestrate.Node{
		{ID: "A", Runner: stepA},
		{ID: "B", Runner: stepB},
		{
			ID: "C", Runner: intMakePassthrough("c"), Deps: []string{"A", "B"},
			InputMapper: func(upstream map[string]*schema.RunResponse) (*schema.RunRequest, error) {
				aText := upstream["A"].Messages[0].Text()
				bText := upstream["B"].Messages[0].Text()
				return &schema.RunRequest{
					Messages: []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, aText+"+"+bText)},
				}, nil
			},
		},
	}
	wf, err := NewDAG(agent.Config{ID: "wf"}, orchestrate.DAGConfig{}, nodes)
	if err != nil {
		t.Fatalf("NewDAG error: %v", err)
	}
	resp, err := wf.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "start")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := resp.Messages[0].Text()
	if got != "start-A+start-B" {
		t.Errorf("got %q, want %q", got, "start-A+start-B")
	}
}

// TestIntegration_DAG_LastResultAggregator tests the LastResult aggregator with multiple terminal nodes.
func TestIntegration_DAG_LastResultAggregator(t *testing.T) {
	nodes := []orchestrate.Node{
		{ID: "root", Runner: intMakePassthrough("root")},
		{ID: "A", Runner: intMakeStep("a", "-A"), Deps: []string{"root"}},
		{ID: "B", Runner: intMakeStep("b", "-B"), Deps: []string{"root"}},
	}
	wf, err := NewDAG(agent.Config{ID: "wf"}, orchestrate.DAGConfig{
		Aggregator: orchestrate.LastResultAggregator(),
	}, nodes)
	if err != nil {
		t.Fatalf("NewDAG error: %v", err)
	}
	resp, err := wf.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "start")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// LastResult picks the last by sorted ID, so "B"
	if len(resp.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(resp.Messages))
	}
	got := resp.Messages[0].Text()
	if got != "start-B" {
		t.Errorf("got %q, want %q", got, "start-B")
	}
}

// TestIntegration_DAG_EarlyExit tests that EarlyExitFunc stops DAG execution.
func TestIntegration_DAG_EarlyExit(t *testing.T) {
	var bCalled atomic.Bool
	nodes := []orchestrate.Node{
		{ID: "A", Runner: intMakeStep("a", "-A")},
		{ID: "B", Runner: agent.NewCustomAgent(agent.Config{ID: "b"}, func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
			bCalled.Store(true)
			time.Sleep(50 * time.Millisecond)
			return &schema.RunResponse{Messages: req.Messages}, nil
		}), Deps: []string{"A"}},
	}
	wf, err := NewDAG(agent.Config{ID: "wf"}, orchestrate.DAGConfig{
		EarlyExitFunc: func(nodeID string, _ *schema.RunResponse) bool {
			return nodeID == "A"
		},
	}, nodes)
	if err != nil {
		t.Fatalf("NewDAG error: %v", err)
	}
	resp, err := wf.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "start")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// A is the terminal result since early exit happened
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

// TestIntegration_DAG_MaxConcurrency tests that concurrency is limited in DAG mode.
func TestIntegration_DAG_MaxConcurrency(t *testing.T) {
	var maxConcurrent atomic.Int32
	var currentConcurrent atomic.Int32

	makeRunner := func(id string) agent.Agent {
		return agent.NewCustomAgent(agent.Config{ID: id}, func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
			cur := currentConcurrent.Add(1)
			for {
				old := maxConcurrent.Load()
				if cur <= old {
					break
				}
				if maxConcurrent.CompareAndSwap(old, cur) {
					break
				}
			}
			time.Sleep(20 * time.Millisecond)
			currentConcurrent.Add(-1)
			return &schema.RunResponse{Messages: req.Messages}, nil
		})
	}

	nodes := []orchestrate.Node{
		{ID: "root", Runner: intMakePassthrough("root")},
		{ID: "A", Runner: makeRunner("a"), Deps: []string{"root"}},
		{ID: "B", Runner: makeRunner("b"), Deps: []string{"root"}},
		{ID: "C", Runner: makeRunner("c"), Deps: []string{"root"}},
		{ID: "D", Runner: makeRunner("d"), Deps: []string{"root"}},
	}
	wf, err := NewDAG(agent.Config{ID: "wf"}, orchestrate.DAGConfig{MaxConcurrency: 2}, nodes)
	if err != nil {
		t.Fatalf("NewDAG error: %v", err)
	}
	_, err = wf.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "start")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if maxConcurrent.Load() > 2 {
		t.Errorf("max concurrent = %d, want <= 2", maxConcurrent.Load())
	}
}

// TestIntegration_DAG_EmptyNodes tests that an empty DAG returns the input as output.
func TestIntegration_DAG_EmptyNodes(t *testing.T) {
	wf, err := NewDAG(agent.Config{ID: "wf"}, orchestrate.DAGConfig{}, nil)
	if err != nil {
		t.Fatalf("NewDAG error: %v", err)
	}
	resp, err := wf.Run(context.Background(), &schema.RunRequest{
		Messages:  []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "hello")},
		SessionID: "empty-session",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Messages[0].Text() != "hello" {
		t.Errorf("expected input message echoed back")
	}
	if resp.SessionID != "empty-session" {
		t.Errorf("SessionID = %q, want %q", resp.SessionID, "empty-session")
	}
}

// TestIntegration_DAG_ContextCancellation tests that context cancellation propagates through
// workflowagent DAG mode.
func TestIntegration_DAG_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	stepA := agent.NewCustomAgent(agent.Config{ID: "a"}, func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		cancel()
		return &schema.RunResponse{Messages: req.Messages}, nil
	})

	nodes := []orchestrate.Node{
		{ID: "A", Runner: stepA},
		{ID: "B", Runner: intMakeStep("b", "-B"), Deps: []string{"A"}},
	}
	wf, err := NewDAG(agent.Config{ID: "wf"}, orchestrate.DAGConfig{}, nodes)
	if err != nil {
		t.Fatalf("NewDAG error: %v", err)
	}
	_, err = wf.Run(ctx, &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "start")},
	})
	if err == nil {
		t.Fatal("expected context error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got: %v", err)
	}
}

// TestIntegration_DAG_ValidationErrors tests DAG validation errors (cycle, duplicate, missing dep)
// through workflowagent.
func TestIntegration_DAG_ValidationErrors(t *testing.T) {
	t.Run("CycleDetection", func(t *testing.T) {
		nodes := []orchestrate.Node{
			{ID: "A", Runner: intMakePassthrough("a"), Deps: []string{"C"}},
			{ID: "B", Runner: intMakePassthrough("b"), Deps: []string{"A"}},
			{ID: "C", Runner: intMakePassthrough("c"), Deps: []string{"B"}},
		}
		_, err := NewDAG(agent.Config{ID: "wf"}, orchestrate.DAGConfig{}, nodes)
		if err == nil {
			t.Fatal("expected cycle error")
		}
		if !strings.Contains(err.Error(), "cycle") {
			t.Errorf("error should mention cycle: %v", err)
		}
	})

	t.Run("DuplicateNodeID", func(t *testing.T) {
		nodes := []orchestrate.Node{
			{ID: "A", Runner: intMakePassthrough("a")},
			{ID: "A", Runner: intMakePassthrough("a2")},
		}
		_, err := NewDAG(agent.Config{ID: "wf"}, orchestrate.DAGConfig{}, nodes)
		if err == nil {
			t.Fatal("expected duplicate ID error")
		}
		if !strings.Contains(err.Error(), "duplicate") {
			t.Errorf("error should mention duplicate: %v", err)
		}
	})

	t.Run("MissingDep", func(t *testing.T) {
		nodes := []orchestrate.Node{
			{ID: "A", Runner: intMakePassthrough("a"), Deps: []string{"Z"}},
		}
		_, err := NewDAG(agent.Config{ID: "wf"}, orchestrate.DAGConfig{}, nodes)
		if err == nil {
			t.Fatal("expected missing dep error")
		}
		if !strings.Contains(err.Error(), "unknown node") {
			t.Errorf("error should mention unknown node: %v", err)
		}
	})
}

// =============================================================================
// Integration tests: DAG with edges
// =============================================================================

// TestIntegration_DAG_EdgeLinear tests a linear DAG built with edges.
func TestIntegration_DAG_EdgeLinear(t *testing.T) {
	nodes := []orchestrate.Node{
		{ID: "A", Runner: intMakeStep("a", "-A")},
		{ID: "B", Runner: intMakeStep("b", "-B")},
		{ID: "C", Runner: intMakeStep("c", "-C")},
	}
	edges := []orchestrate.Edge{
		{From: "A", To: "B"},
		{From: "B", To: "C"},
	}
	wf, err := NewDAGWithEdges(agent.Config{ID: "wf-edge-linear"}, orchestrate.DAGConfig{}, nodes, edges)
	if err != nil {
		t.Fatalf("NewDAGWithEdges error: %v", err)
	}
	resp, err := wf.Run(context.Background(), &schema.RunRequest{
		Messages:  []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "start")},
		SessionID: "edge-session",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := resp.Messages[0].Text()
	if got != "start-A-B-C" {
		t.Errorf("got %q, want %q", got, "start-A-B-C")
	}
	if resp.SessionID != "edge-session" {
		t.Errorf("SessionID = %q, want %q", resp.SessionID, "edge-session")
	}
}

// TestIntegration_DAG_EdgeDiamond tests a diamond DAG built with edges.
func TestIntegration_DAG_EdgeDiamond(t *testing.T) {
	nodes := []orchestrate.Node{
		{ID: "A", Runner: intMakeStep("a", "-A")},
		{ID: "B", Runner: intMakeStep("b", "-B")},
		{ID: "C", Runner: intMakeStep("c", "-C")},
		{ID: "D", Runner: intMakePassthrough("d")},
	}
	edges := []orchestrate.Edge{
		{From: "A", To: "B"},
		{From: "A", To: "C"},
		{From: "B", To: "D"},
		{From: "C", To: "D"},
	}
	wf, err := NewDAGWithEdges(agent.Config{ID: "wf-edge-diamond"}, orchestrate.DAGConfig{}, nodes, edges)
	if err != nil {
		t.Fatalf("NewDAGWithEdges error: %v", err)
	}
	resp, err := wf.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "start")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(resp.Messages))
	}
}

// TestIntegration_DAG_EdgeValidationError tests that NewDAGWithEdges returns errors for invalid input.
func TestIntegration_DAG_EdgeValidationError(t *testing.T) {
	t.Run("UnknownNode", func(t *testing.T) {
		nodes := []orchestrate.Node{
			{ID: "A", Runner: intMakePassthrough("a")},
		}
		edges := []orchestrate.Edge{
			{From: "A", To: "Z"},
		}
		_, err := NewDAGWithEdges(agent.Config{ID: "wf"}, orchestrate.DAGConfig{}, nodes, edges)
		if err == nil {
			t.Fatal("expected error for unknown node")
		}
		if !strings.Contains(err.Error(), "unknown node") {
			t.Errorf("error should mention unknown node: %v", err)
		}
	})

	t.Run("DepsMixError", func(t *testing.T) {
		nodes := []orchestrate.Node{
			{ID: "A", Runner: intMakePassthrough("a")},
			{ID: "B", Runner: intMakePassthrough("b"), Deps: []string{"A"}},
		}
		edges := []orchestrate.Edge{
			{From: "A", To: "B"},
		}
		_, err := NewDAGWithEdges(agent.Config{ID: "wf"}, orchestrate.DAGConfig{}, nodes, edges)
		if err == nil {
			t.Fatal("expected error for mixing Deps and edges")
		}
		if !strings.Contains(err.Error(), "Deps set") {
			t.Errorf("error should mention Deps set: %v", err)
		}
	})
}

// =============================================================================
// Integration tests: Loop workflows through workflowagent
// =============================================================================

// TestIntegration_Loop_BasicIteration tests a simple loop with fixed max iterations.
func TestIntegration_Loop_BasicIteration(t *testing.T) {
	body := intMakeStep("loop-body", "-iter")
	wf := NewLoop(agent.Config{ID: "wf-loop"}, body, nil, 3)
	resp, err := wf.Run(context.Background(), &schema.RunRequest{
		Messages:  []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "start")},
		SessionID: "loop-session",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := resp.Messages[0].Text()
	if got != "start-iter-iter-iter" {
		t.Errorf("got %q, want %q", got, "start-iter-iter-iter")
	}
	if resp.SessionID != "loop-session" {
		t.Errorf("SessionID = %q, want %q", resp.SessionID, "loop-session")
	}
	if resp.Duration < 0 {
		t.Errorf("Duration should be >= 0, got %d", resp.Duration)
	}
}

// TestIntegration_Loop_ConditionTermination tests loop termination via a condition function.
func TestIntegration_Loop_ConditionTermination(t *testing.T) {
	callCount := 0
	body := agent.NewCustomAgent(agent.Config{ID: "loop-body"}, func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		callCount++
		text := req.Messages[0].Text()
		return &schema.RunResponse{
			Messages: []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, text+"-iter")},
		}, nil
	})
	condition := func(resp *schema.RunResponse) bool {
		if resp == nil {
			return true
		}
		return !strings.Contains(resp.Messages[0].Text(), "-iter-iter-iter")
	}
	wf := NewLoop(agent.Config{ID: "wf-loop"}, body, condition, 0)
	resp, err := wf.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "start")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if callCount != 3 {
		t.Errorf("expected 3 iterations, got %d", callCount)
	}
	got := resp.Messages[0].Text()
	if got != "start-iter-iter-iter" {
		t.Errorf("got %q, want %q", got, "start-iter-iter-iter")
	}
}

// TestIntegration_Loop_ZeroIterations tests that a loop with a condition that returns false
// for nil produces zero iterations and echoes input.
func TestIntegration_Loop_ZeroIterations(t *testing.T) {
	callCount := 0
	body := agent.NewCustomAgent(agent.Config{ID: "loop-body"}, func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		callCount++
		return &schema.RunResponse{Messages: req.Messages}, nil
	})
	condition := func(resp *schema.RunResponse) bool {
		return resp != nil // false for nil pre-check
	}
	wf := NewLoop(agent.Config{ID: "wf-loop"}, body, condition, 0)
	resp, err := wf.Run(context.Background(), &schema.RunRequest{
		Messages:  []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "hello")},
		SessionID: "zero-session",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if callCount != 0 {
		t.Errorf("expected 0 iterations, got %d", callCount)
	}
	if resp.Messages[0].Text() != "hello" {
		t.Errorf("expected input message echoed back")
	}
	if resp.SessionID != "zero-session" {
		t.Errorf("SessionID = %q, want %q", resp.SessionID, "zero-session")
	}
}

// TestIntegration_Loop_OutputChaining tests that each iteration's output feeds the next as input.
func TestIntegration_Loop_OutputChaining(t *testing.T) {
	var received []string
	body := agent.NewCustomAgent(agent.Config{ID: "loop-body"}, func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		text := req.Messages[0].Text()
		received = append(received, text)
		return &schema.RunResponse{
			Messages: []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, text+"+")},
		}, nil
	})
	wf := NewLoop(agent.Config{ID: "wf-loop"}, body, nil, 3)
	_, err := wf.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "start")},
	})
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

// TestIntegration_Loop_UsageAccumulation tests that usage is accumulated across all loop iterations.
func TestIntegration_Loop_UsageAccumulation(t *testing.T) {
	body := intMakeStepWithUsage("loop-body", 10, 20, 30)
	wf := NewLoop(agent.Config{ID: "wf-loop"}, body, nil, 3)
	resp, err := wf.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "start")},
	})
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
}

// TestIntegration_Loop_ContextCancellation tests context cancellation in loop mode.
func TestIntegration_Loop_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	callCount := 0
	body := agent.NewCustomAgent(agent.Config{ID: "loop-body"}, func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		callCount++
		if callCount == 2 {
			cancel()
		}
		return &schema.RunResponse{Messages: req.Messages}, nil
	})
	condition := func(_ *schema.RunResponse) bool { return true }
	wf := NewLoop(agent.Config{ID: "wf-loop"}, body, condition, 0)
	_, err := wf.Run(ctx, &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "start")},
	})
	if err == nil {
		t.Fatal("expected context error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got: %v", err)
	}
}

// TestIntegration_Loop_ErrorPropagation tests that errors from loop body propagate through workflowagent.
func TestIntegration_Loop_ErrorPropagation(t *testing.T) {
	body := intMakeError("loop-body", errors.New("loop body failed"))
	wf := NewLoop(agent.Config{ID: "wf-loop"}, body, nil, 3)
	_, err := wf.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "start")},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "loop body failed") {
		t.Errorf("error should contain original message: %v", err)
	}
}

// =============================================================================
// Integration tests: Sequential workflows (backward compatibility)
// =============================================================================

// TestIntegration_Sequential_BackwardCompat tests that sequential workflow behavior is
// preserved after the refactoring.
func TestIntegration_Sequential_BackwardCompat(t *testing.T) {
	step1 := intMakeStep("s1", "-a")
	step2 := intMakeStep("s2", "-b")
	step3 := intMakeStep("s3", "-c")

	wf := New(agent.Config{ID: "wf"}, step1, step2, step3)
	resp, err := wf.Run(context.Background(), &schema.RunRequest{
		Messages:  []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "start")},
		SessionID: "seq-session",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := resp.Messages[0].Text()
	if got != "start-a-b-c" {
		t.Errorf("got %q, want %q", got, "start-a-b-c")
	}
	if resp.SessionID != "seq-session" {
		t.Errorf("SessionID = %q, want %q", resp.SessionID, "seq-session")
	}
	if resp.Duration < 0 {
		t.Errorf("Duration should be >= 0, got %d", resp.Duration)
	}
}

// TestIntegration_Sequential_ErrorFormat tests that sequential error messages have the expected format.
func TestIntegration_Sequential_ErrorFormat(t *testing.T) {
	step1 := intMakeStep("s1", "-a")
	step2 := intMakeError("s2", errors.New("step2 failed"))

	wf := New(agent.Config{ID: "wf"}, step1, step2)
	_, err := wf.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "start")},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	// Verify the error format matches "workflowagent: workflow step %d (%s): %w"
	if !strings.Contains(err.Error(), "workflow step 2") {
		t.Errorf("error should contain 'workflow step 2': %v", err)
	}
	if !strings.Contains(err.Error(), "s2") {
		t.Errorf("error should contain step ID 's2': %v", err)
	}
	if !strings.Contains(err.Error(), "step2 failed") {
		t.Errorf("error should contain original message: %v", err)
	}
}

// =============================================================================
// Integration tests: Streaming across all modes
// =============================================================================

// TestIntegration_DAG_StreamLifecycle tests that DAG mode streaming produces correct events.
func TestIntegration_DAG_StreamLifecycle(t *testing.T) {
	nodes := []orchestrate.Node{
		{ID: "A", Runner: intMakeStep("a", "-A")},
		{ID: "B", Runner: intMakeStep("b", "-B"), Deps: []string{"A"}},
	}
	wf, err := NewDAG(agent.Config{ID: "wf"}, orchestrate.DAGConfig{}, nodes)
	if err != nil {
		t.Fatalf("NewDAG error: %v", err)
	}
	stream, err := wf.RunStream(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "hello")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = stream.Close() }()

	// Expect AgentStart
	e, err := stream.Recv()
	if err != nil {
		t.Fatalf("recv error: %v", err)
	}
	if e.Type != schema.EventAgentStart {
		t.Errorf("expected AgentStart, got %s", e.Type)
	}

	// Expect AgentEnd
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
	if endData.Message != "hello-A-B" {
		t.Errorf("got message %q, want %q", endData.Message, "hello-A-B")
	}

	// Expect EOF
	_, err = stream.Recv()
	if !errors.Is(err, io.EOF) {
		t.Errorf("expected io.EOF, got %v", err)
	}
}

// TestIntegration_Loop_StreamLifecycle tests that loop mode streaming produces correct events.
func TestIntegration_Loop_StreamLifecycle(t *testing.T) {
	body := intMakeStep("loop-body", "-iter")
	wf := NewLoop(agent.Config{ID: "wf-loop"}, body, nil, 2)
	stream, err := wf.RunStream(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "hello")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = stream.Close() }()

	// Expect AgentStart
	e, err := stream.Recv()
	if err != nil {
		t.Fatalf("recv error: %v", err)
	}
	if e.Type != schema.EventAgentStart {
		t.Errorf("expected AgentStart, got %s", e.Type)
	}

	// Expect AgentEnd
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
		t.Errorf("got message %q, want %q", endData.Message, "hello-iter-iter")
	}

	// Expect EOF
	_, err = stream.Recv()
	if !errors.Is(err, io.EOF) {
		t.Errorf("expected io.EOF, got %v", err)
	}
}

// TestIntegration_DAG_StreamError tests that DAG errors propagate correctly through streaming.
func TestIntegration_DAG_StreamError(t *testing.T) {
	nodes := []orchestrate.Node{
		{ID: "A", Runner: intMakeError("a", errors.New("dag stream error"))},
	}
	wf, err := NewDAG(agent.Config{ID: "wf"}, orchestrate.DAGConfig{}, nodes)
	if err != nil {
		t.Fatalf("NewDAG error: %v", err)
	}
	stream, err := wf.RunStream(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "hello")},
	})
	if err != nil {
		t.Fatalf("unexpected error creating stream: %v", err)
	}
	defer func() { _ = stream.Close() }()

	// Expect AgentStart
	e, err := stream.Recv()
	if err != nil {
		t.Fatalf("recv error: %v", err)
	}
	if e.Type != schema.EventAgentStart {
		t.Errorf("expected AgentStart, got %s", e.Type)
	}

	// Expect error from producer
	_, err = stream.Recv()
	if err == nil {
		t.Fatal("expected error from stream")
	}
	if !strings.Contains(err.Error(), "dag stream error") {
		t.Errorf("error = %q, want containing 'dag stream error'", err.Error())
	}
}

// =============================================================================
// Integration tests: Cross-cutting concerns
// =============================================================================

// TestIntegration_AllModes_Steps tests that Steps() returns the correct value for each mode.
func TestIntegration_AllModes_Steps(t *testing.T) {
	// Sequential mode
	s1 := intMakeStep("s1", "")
	s2 := intMakeStep("s2", "")
	seqWf := New(agent.Config{ID: "wf"}, s1, s2)
	if len(seqWf.Steps()) != 2 {
		t.Errorf("sequential Steps() = %d, want 2", len(seqWf.Steps()))
	}

	// DAG mode
	dagWf, err := NewDAG(agent.Config{ID: "wf"}, orchestrate.DAGConfig{}, nil)
	if err != nil {
		t.Fatalf("NewDAG error: %v", err)
	}
	if len(dagWf.Steps()) != 0 {
		t.Errorf("DAG Steps() = %d, want 0", len(dagWf.Steps()))
	}

	// Loop mode
	loopWf := NewLoop(agent.Config{ID: "wf"}, intMakeStep("body", ""), nil, 1)
	if len(loopWf.Steps()) != 0 {
		t.Errorf("Loop Steps() = %d, want 0", len(loopWf.Steps()))
	}
}

// TestIntegration_AllModes_InterfaceCompliance verifies that Agent satisfies
// both agent.Agent and agent.StreamAgent for all workflow modes.
func TestIntegration_AllModes_InterfaceCompliance(t *testing.T) {
	var _ agent.Agent = (*Agent)(nil)
	var _ agent.StreamAgent = (*Agent)(nil)

	// Also test runtime type assertions with real instances
	seqWf := New(agent.Config{ID: "wf"}, intMakeStep("s1", ""))
	dagWf, err := NewDAG(agent.Config{ID: "wf"}, orchestrate.DAGConfig{}, nil)
	if err != nil {
		t.Fatalf("NewDAG error: %v", err)
	}
	loopWf := NewLoop(agent.Config{ID: "wf"}, intMakeStep("body", ""), nil, 1)

	for _, wf := range []*Agent{seqWf, dagWf, loopWf} {
		if _, ok := any(wf).(agent.Agent); !ok {
			t.Errorf("Agent should implement agent.Agent")
		}
		if _, ok := any(wf).(agent.StreamAgent); !ok {
			t.Errorf("Agent should implement agent.StreamAgent")
		}
	}
}

// TestIntegration_DAG_ComplexGraph tests a complex DAG with mixed patterns:
// fan-out, fan-in, conditional, optional nodes, and custom aggregation.
func TestIntegration_DAG_ComplexGraph(t *testing.T) {
	//       root
	//      / | \
	//     A  B  C(optional)
	//      \ | /
	//       merge
	nodes := []orchestrate.Node{
		{ID: "root", Runner: intMakeStep("root", "-root")},
		{ID: "A", Runner: intMakeStep("a", "-A"), Deps: []string{"root"}},
		{ID: "B", Runner: intMakeStep("b", "-B"), Deps: []string{"root"}},
		{ID: "C", Runner: intMakeError("c", errors.New("c failed")), Deps: []string{"root"}, Optional: true},
		{ID: "merge", Runner: intMakePassthrough("merge"), Deps: []string{"A", "B", "C"}},
	}
	wf, err := NewDAG(agent.Config{ID: "wf"}, orchestrate.DAGConfig{ErrorStrategy: orchestrate.Skip}, nodes)
	if err != nil {
		t.Fatalf("NewDAG error: %v", err)
	}
	resp, err := wf.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "start")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// merge should get A and B results (C was skipped)
	if len(resp.Messages) < 2 {
		t.Fatalf("expected at least 2 messages, got %d", len(resp.Messages))
	}
}

// TestIntegration_DAG_SingleNode tests a DAG with a single node.
func TestIntegration_DAG_SingleNode(t *testing.T) {
	nodes := []orchestrate.Node{
		{ID: "only", Runner: intMakeStep("only", "-only")},
	}
	wf, err := NewDAG(agent.Config{ID: "wf"}, orchestrate.DAGConfig{}, nodes)
	if err != nil {
		t.Fatalf("NewDAG error: %v", err)
	}
	resp, err := wf.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "start")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := resp.Messages[0].Text()
	if got != "start-only" {
		t.Errorf("got %q, want %q", got, "start-only")
	}
}

// TestIntegration_Loop_SingleIteration tests a loop with MaxIters=1 (single iteration).
func TestIntegration_Loop_SingleIteration(t *testing.T) {
	body := intMakeStep("body", "-done")
	wf := NewLoop(agent.Config{ID: "wf"}, body, nil, 1)
	resp, err := wf.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "start")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := resp.Messages[0].Text()
	if got != "start-done" {
		t.Errorf("got %q, want %q", got, "start-done")
	}
}

// TestIntegration_Loop_MaxItersSafety tests that MaxIters prevents infinite loops
// even when condition always returns true.
func TestIntegration_Loop_MaxItersSafety(t *testing.T) {
	callCount := 0
	body := agent.NewCustomAgent(agent.Config{ID: "body"}, func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		callCount++
		return &schema.RunResponse{Messages: req.Messages}, nil
	})
	condition := func(_ *schema.RunResponse) bool { return true } // always continue
	wf := NewLoop(agent.Config{ID: "wf"}, body, condition, 5)
	_, err := wf.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "start")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if callCount != 5 {
		t.Errorf("expected 5 iterations, got %d", callCount)
	}
}

// TestIntegration_DAG_DisconnectedNode tests that a DAG with a disconnected node returns an error.
func TestIntegration_DAG_DisconnectedNode(t *testing.T) {
	nodes := []orchestrate.Node{
		{ID: "A", Runner: intMakeStep("a", "-A")},
		{ID: "B", Runner: intMakeStep("b", "-B"), Deps: []string{"A"}},
		{ID: "C", Runner: intMakeStep("c", "-C")}, // disconnected from A-B
	}
	_, err := NewDAG(agent.Config{ID: "wf"}, orchestrate.DAGConfig{}, nodes)
	if err == nil {
		t.Fatal("expected error for disconnected node")
	}
	if !strings.Contains(err.Error(), "disconnected") {
		t.Errorf("error should mention disconnected: %v", err)
	}
}

// TestIntegration_DAG_EdgeDisconnectedNode tests that NewDAGWithEdges detects disconnected nodes.
func TestIntegration_DAG_EdgeDisconnectedNode(t *testing.T) {
	nodes := []orchestrate.Node{
		{ID: "A", Runner: intMakeStep("a", "-A")},
		{ID: "B", Runner: intMakeStep("b", "-B")},
		{ID: "C", Runner: intMakeStep("c", "-C")},
	}
	edges := []orchestrate.Edge{
		{From: "A", To: "B"},
	}
	_, err := NewDAGWithEdges(agent.Config{ID: "wf"}, orchestrate.DAGConfig{}, nodes, edges)
	if err == nil {
		t.Fatal("expected error for disconnected node C")
	}
	if !strings.Contains(err.Error(), "disconnected") {
		t.Errorf("error should mention disconnected: %v", err)
	}
}
