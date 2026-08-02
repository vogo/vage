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

package orchestrate_tests

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vogo/vage/orchestrate"
	"github.com/vogo/vage/schema"
)

// =============================================================================
// Test helpers
// =============================================================================

type mockRunner struct {
	fn func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error)
}

func (m *mockRunner) Run(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
	return m.fn(ctx, req)
}

func newMockRunner(fn func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error)) *mockRunner {
	return &mockRunner{fn: fn}
}

func appendRunner(suffix string) *mockRunner {
	return newMockRunner(func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		text := ""
		if len(req.Messages) > 0 {
			text = req.Messages[0].Text()
		}
		return &schema.RunResponse{
			Messages: []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, text+suffix)},
		}, nil
	})
}

func passthroughRunner() *mockRunner {
	return newMockRunner(func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		return &schema.RunResponse{Messages: req.Messages}, nil
	})
}

func errorRunner(err error) *mockRunner {
	return newMockRunner(func(_ context.Context, _ *schema.RunRequest) (*schema.RunResponse, error) {
		return nil, err
	})
}

func makeReq(text string) *schema.RunRequest {
	return &schema.RunRequest{
		Messages:  []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, text)},
		SessionID: "test-session",
	}
}

// mockCompensatableRunner implements Runner, Compensatable, and IdempotentChecker.
type mockCompensatableRunner struct {
	runFn        func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error)
	compensateFn func(ctx context.Context, resp *schema.RunResponse) error
	idempotent   bool
}

func (m *mockCompensatableRunner) Run(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
	return m.runFn(ctx, req)
}

func (m *mockCompensatableRunner) Compensate(ctx context.Context, resp *schema.RunResponse) error {
	return m.compensateFn(ctx, resp)
}

func (m *mockCompensatableRunner) Idempotent() bool {
	return m.idempotent
}

// testEventHandler records DAG lifecycle events.
type testEventHandler struct {
	mu               sync.Mutex
	starts           []string
	completes        []eventComplete
	checkpointErrors []eventCheckpointError
}

type eventComplete struct {
	nodeID string
	status orchestrate.NodeStatus
	err    error
}

type eventCheckpointError struct {
	nodeID string
	err    error
}

func (h *testEventHandler) OnNodeStart(nodeID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.starts = append(h.starts, nodeID)
}

func (h *testEventHandler) OnNodeComplete(nodeID string, status orchestrate.NodeStatus, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.completes = append(h.completes, eventComplete{nodeID, status, err})
}

func (h *testEventHandler) OnCheckpointError(nodeID string, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.checkpointErrors = append(h.checkpointErrors, eventCheckpointError{nodeID, err})
}

// failingCheckpointStore wraps InMemoryCheckpointStore but fails on Save.
type failingCheckpointStore struct {
	*orchestrate.InMemoryCheckpointStore
	saveErr error
}

func (s *failingCheckpointStore) Save(_ context.Context, _, _ string, _ *schema.RunResponse) error {
	return s.saveErr
}

// =============================================================================
// RunDAG functional options tests
// =============================================================================

func TestRunDAG_Basic(t *testing.T) {
	nodes := []orchestrate.Node{
		{ID: "A", Runner: appendRunner("-A")},
		{ID: "B", Runner: appendRunner("-B"), Deps: []string{"A"}},
	}
	result, err := orchestrate.RunDAG(context.Background(), nodes, makeReq("start"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := result.FinalOutput.Messages[0].Text()
	if got != "start-A-B" {
		t.Errorf("got %q, want %q", got, "start-A-B")
	}
}

func TestRunDAG_WithMaxConcurrency(t *testing.T) {
	var maxConcurrent atomic.Int32
	var currentConcurrent atomic.Int32

	runner := newMockRunner(func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
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
	})

	nodes := []orchestrate.Node{
		{ID: "root", Runner: passthroughRunner()},
		{ID: "A", Runner: runner, Deps: []string{"root"}},
		{ID: "B", Runner: runner, Deps: []string{"root"}},
		{ID: "C", Runner: runner, Deps: []string{"root"}},
		{ID: "D", Runner: runner, Deps: []string{"root"}},
	}
	_, err := orchestrate.RunDAG(context.Background(), nodes, makeReq("start"),
		orchestrate.WithMaxConcurrency(2),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if maxConcurrent.Load() > 2 {
		t.Errorf("max concurrent = %d, want <= 2", maxConcurrent.Load())
	}
}

func TestRunDAG_WithAggregator(t *testing.T) {
	nodes := []orchestrate.Node{
		{ID: "root", Runner: passthroughRunner()},
		{ID: "A", Runner: appendRunner("-A"), Deps: []string{"root"}},
		{ID: "B", Runner: appendRunner("-B"), Deps: []string{"root"}},
	}
	result, err := orchestrate.RunDAG(context.Background(), nodes, makeReq("start"),
		orchestrate.WithAggregator(orchestrate.LastResultAggregator()),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := result.FinalOutput.Messages[0].Text()
	if got != "start-B" {
		t.Errorf("got %q, want %q", got, "start-B")
	}
}

func TestRunDAG_WithEarlyExit(t *testing.T) {
	nodes := []orchestrate.Node{
		{ID: "A", Runner: appendRunner("-A")},
		{ID: "B", Runner: appendRunner("-B"), Deps: []string{"A"}},
	}
	result, err := orchestrate.RunDAG(context.Background(), nodes, makeReq("start"),
		orchestrate.WithEarlyExit(func(nodeID string, _ *schema.RunResponse) bool {
			return nodeID == "A"
		}),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.NodeStatus["A"] != orchestrate.NodeDone {
		t.Errorf("A should be Done")
	}
}

func TestRunDAG_WithCheckpointStore(t *testing.T) {
	store := orchestrate.NewInMemoryCheckpointStore()
	nodes := []orchestrate.Node{
		{ID: "A", Runner: appendRunner("-A")},
		{ID: "B", Runner: appendRunner("-B"), Deps: []string{"A"}},
	}
	result, err := orchestrate.RunDAG(context.Background(), nodes, makeReq("start"),
		orchestrate.WithCheckpointStore(store),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.NodeStatus["B"] != orchestrate.NodeDone {
		t.Errorf("B status = %d, want NodeDone", result.NodeStatus["B"])
	}
	saved, _ := store.LoadAll(context.Background(), "test-session")
	if len(saved) != 2 {
		t.Errorf("expected 2 checkpoints, got %d", len(saved))
	}
}

func TestRunDAG_WithPriorityScheduling(t *testing.T) {
	var mu sync.Mutex
	var order []string

	makeOrderRunner := func(id string) *mockRunner {
		return newMockRunner(func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
			mu.Lock()
			order = append(order, id)
			mu.Unlock()
			return &schema.RunResponse{Messages: req.Messages}, nil
		})
	}

	nodes := []orchestrate.Node{
		{ID: "root", Runner: passthroughRunner()},
		{ID: "low", Runner: makeOrderRunner("low"), Deps: []string{"root"}, Priority: 1},
		{ID: "high", Runner: makeOrderRunner("high"), Deps: []string{"root"}, Priority: 100},
		{ID: "mid", Runner: makeOrderRunner("mid"), Deps: []string{"root"}, Priority: 50},
	}
	_, err := orchestrate.RunDAG(context.Background(), nodes, makeReq("start"),
		orchestrate.WithPriorityScheduling(false),
		orchestrate.WithMaxConcurrency(1),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(order) != 3 {
		t.Fatalf("expected 3 entries, got %d: %v", len(order), order)
	}
	if order[0] != "high" {
		t.Errorf("expected high first, got order: %v", order)
	}
}

func TestRunDAG_WithResourceLimits(t *testing.T) {
	var maxConcurrentGPU atomic.Int32
	var currentGPU atomic.Int32

	gpuRunner := newMockRunner(func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		cur := currentGPU.Add(1)
		for {
			old := maxConcurrentGPU.Load()
			if cur <= old || maxConcurrentGPU.CompareAndSwap(old, cur) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		currentGPU.Add(-1)
		return &schema.RunResponse{Messages: req.Messages}, nil
	})

	nodes := []orchestrate.Node{
		{ID: "root", Runner: passthroughRunner()},
		{ID: "A", Runner: gpuRunner, Deps: []string{"root"}, ResourceTags: []string{"gpu"}},
		{ID: "B", Runner: gpuRunner, Deps: []string{"root"}, ResourceTags: []string{"gpu"}},
		{ID: "C", Runner: gpuRunner, Deps: []string{"root"}, ResourceTags: []string{"gpu"}},
	}

	_, err := orchestrate.RunDAG(context.Background(), nodes, makeReq("start"),
		orchestrate.WithResourceLimits(map[string]int{"gpu": 1}),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if maxConcurrentGPU.Load() > 1 {
		t.Errorf("max concurrent GPU = %d, want <= 1", maxConcurrentGPU.Load())
	}
}

func TestRunDAG_WithCompensation(t *testing.T) {
	var compensated []string
	var mu sync.Mutex

	compRunner := &mockCompensatableRunner{
		runFn: func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
			return &schema.RunResponse{Messages: req.Messages}, nil
		},
		compensateFn: func(_ context.Context, _ *schema.RunResponse) error {
			mu.Lock()
			compensated = append(compensated, "A")
			mu.Unlock()
			return nil
		},
		idempotent: true,
	}

	nodes := []orchestrate.Node{
		{ID: "A", Runner: compRunner},
		{ID: "B", Runner: errorRunner(errors.New("fail")), Deps: []string{"A"}},
	}

	_, err := orchestrate.RunDAG(context.Background(), nodes, makeReq("start"),
		orchestrate.WithCompensation(&orchestrate.CompensateConfig{
			Strategy:   orchestrate.BackwardCompensate,
			MaxRetries: 1,
		}),
	)
	if err == nil {
		t.Fatal("expected error")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(compensated) != 1 || compensated[0] != "A" {
		t.Errorf("expected A to be compensated, got %v", compensated)
	}
}

func TestRunDAG_MultipleOptions(t *testing.T) {
	store := orchestrate.NewInMemoryCheckpointStore()
	nodes := []orchestrate.Node{
		{ID: "A", Runner: appendRunner("-A")},
		{ID: "B", Runner: appendRunner("-B"), Deps: []string{"A"}},
		{ID: "C", Runner: appendRunner("-C"), Deps: []string{"A"}},
	}
	result, err := orchestrate.RunDAG(context.Background(), nodes, makeReq("start"),
		orchestrate.WithMaxConcurrency(2),
		orchestrate.WithCheckpointStore(store),
		orchestrate.WithAggregator(orchestrate.ConcatMessagesAggregator()),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.FinalOutput.Messages) != 2 {
		t.Errorf("expected 2 messages, got %d", len(result.FinalOutput.Messages))
	}
}

// =============================================================================
// DAGEventHandler integration tests
// =============================================================================

func TestEventHandler_StartAndComplete(t *testing.T) {
	handler := &testEventHandler{}
	nodes := []orchestrate.Node{
		{ID: "A", Runner: appendRunner("-A")},
		{ID: "B", Runner: appendRunner("-B"), Deps: []string{"A"}},
	}
	_, err := orchestrate.RunDAG(context.Background(), nodes, makeReq("start"),
		orchestrate.WithEventHandler(handler),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	handler.mu.Lock()
	defer handler.mu.Unlock()

	if len(handler.starts) != 2 {
		t.Errorf("expected 2 start events, got %d: %v", len(handler.starts), handler.starts)
	}
	doneCount := 0
	for _, c := range handler.completes {
		if c.status == orchestrate.NodeDone {
			doneCount++
		}
	}
	if doneCount < 2 {
		t.Errorf("expected at least 2 NodeDone completes, got %d", doneCount)
	}
}

func TestEventHandler_OnNodeFailed(t *testing.T) {
	handler := &testEventHandler{}
	nodes := []orchestrate.Node{
		{ID: "A", Runner: errorRunner(errors.New("fail"))},
	}
	_, err := orchestrate.RunDAG(context.Background(), nodes, makeReq("start"),
		orchestrate.WithEventHandler(handler),
	)
	if err == nil {
		t.Fatal("expected error")
	}

	handler.mu.Lock()
	defer handler.mu.Unlock()

	var failedFound bool
	for _, c := range handler.completes {
		if c.nodeID == "A" && c.status == orchestrate.NodeFailed && c.err != nil {
			failedFound = true
		}
	}
	if !failedFound {
		t.Errorf("expected A to have NodeFailed complete event, got %v", handler.completes)
	}
}

func TestEventHandler_ParallelNodes(t *testing.T) {
	handler := &testEventHandler{}
	nodes := []orchestrate.Node{
		{ID: "root", Runner: passthroughRunner()},
		{ID: "A", Runner: appendRunner("-A"), Deps: []string{"root"}},
		{ID: "B", Runner: appendRunner("-B"), Deps: []string{"root"}},
		{ID: "C", Runner: appendRunner("-C"), Deps: []string{"root"}},
	}
	_, err := orchestrate.RunDAG(context.Background(), nodes, makeReq("start"),
		orchestrate.WithEventHandler(handler),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	handler.mu.Lock()
	defer handler.mu.Unlock()

	if len(handler.starts) != 4 {
		t.Errorf("expected 4 start events, got %d: %v", len(handler.starts), handler.starts)
	}
	doneCount := 0
	for _, c := range handler.completes {
		if c.status == orchestrate.NodeDone {
			doneCount++
		}
	}
	if doneCount != 4 {
		t.Errorf("expected 4 NodeDone events, got %d", doneCount)
	}
}

func TestEventHandler_CheckpointError(t *testing.T) {
	handler := &testEventHandler{}
	store := &failingCheckpointStore{
		InMemoryCheckpointStore: orchestrate.NewInMemoryCheckpointStore(),
		saveErr:                 errors.New("disk full"),
	}

	nodes := []orchestrate.Node{
		{ID: "A", Runner: appendRunner("-A")},
	}

	_, err := orchestrate.RunDAG(context.Background(), nodes, makeReq("start"),
		orchestrate.WithCheckpointStore(store),
		orchestrate.WithEventHandler(handler),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	handler.mu.Lock()
	defer handler.mu.Unlock()

	if len(handler.checkpointErrors) == 0 {
		t.Error("expected at least one checkpoint error event")
	} else {
		if handler.checkpointErrors[0].nodeID != "A" {
			t.Errorf("checkpoint error node = %q, want A", handler.checkpointErrors[0].nodeID)
		}
		if !strings.Contains(handler.checkpointErrors[0].err.Error(), "disk full") {
			t.Errorf("checkpoint error = %v, want 'disk full'", handler.checkpointErrors[0].err)
		}
	}
}

func TestEventHandler_SkippedConditional(t *testing.T) {
	handler := &testEventHandler{}
	nodes := []orchestrate.Node{
		{ID: "A", Runner: appendRunner("-A")},
		{
			ID: "B", Runner: appendRunner("-B"), Deps: []string{"A"},
			Condition: func(_ map[string]*schema.RunResponse) bool { return false },
		},
		{ID: "C", Runner: appendRunner("-C"), Deps: []string{"A"}},
	}
	result, err := orchestrate.RunDAG(context.Background(), nodes, makeReq("start"),
		orchestrate.WithEventHandler(handler),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.NodeStatus["B"] != orchestrate.NodeSkipped {
		t.Errorf("B status = %d, want NodeSkipped", result.NodeStatus["B"])
	}
}

// =============================================================================
// Resource deadlock prevention integration test
// =============================================================================

func TestDAG_ResourceOverlappingTags_NoDeadlock(t *testing.T) {
	runner := newMockRunner(func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		time.Sleep(10 * time.Millisecond)
		return &schema.RunResponse{Messages: req.Messages}, nil
	})

	nodes := []orchestrate.Node{
		{ID: "root", Runner: passthroughRunner()},
		{ID: "A", Runner: runner, Deps: []string{"root"}, ResourceTags: []string{"gpu", "memory"}},
		{ID: "B", Runner: runner, Deps: []string{"root"}, ResourceTags: []string{"memory", "gpu"}},
		{ID: "C", Runner: runner, Deps: []string{"root"}, ResourceTags: []string{"gpu", "memory"}},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := orchestrate.RunDAG(ctx, nodes, makeReq("start"),
		orchestrate.WithResourceLimits(map[string]int{"gpu": 2, "memory": 2}),
	)
	if err != nil {
		t.Fatalf("unexpected error (possible deadlock): %v", err)
	}
	for _, id := range []string{"A", "B", "C"} {
		if result.NodeStatus[id] != orchestrate.NodeDone {
			t.Errorf("node %s status = %d, want NodeDone", id, result.NodeStatus[id])
		}
	}
}

func TestDAG_ResourceWithRateLimits(t *testing.T) {
	var callCount atomic.Int32
	runner := newMockRunner(func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		callCount.Add(1)
		return &schema.RunResponse{Messages: req.Messages}, nil
	})

	nodes := []orchestrate.Node{
		{ID: "root", Runner: passthroughRunner()},
		{ID: "A", Runner: runner, Deps: []string{"root"}, ResourceTags: []string{"api"}},
		{ID: "B", Runner: runner, Deps: []string{"root"}, ResourceTags: []string{"api"}},
	}

	result, err := orchestrate.RunDAG(context.Background(), nodes, makeReq("start"),
		orchestrate.WithResourceLimits(map[string]int{"api": 1}),
		orchestrate.WithResourceRateLimits(map[string]float64{"api": 100.0}),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if callCount.Load() != 2 {
		t.Errorf("expected 2 calls, got %d", callCount.Load())
	}
	for _, id := range []string{"A", "B"} {
		if result.NodeStatus[id] != orchestrate.NodeDone {
			t.Errorf("node %s status = %d, want NodeDone", id, result.NodeStatus[id])
		}
	}
}

// =============================================================================
// IdempotentChecker integration tests
// =============================================================================

func TestIdempotentChecker_CompensateWithRetry(t *testing.T) {
	var compensateCount atomic.Int32
	compRunner := &mockCompensatableRunner{
		runFn: func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
			return &schema.RunResponse{Messages: req.Messages}, nil
		},
		compensateFn: func(_ context.Context, _ *schema.RunResponse) error {
			count := compensateCount.Add(1)
			if count <= 1 {
				return errors.New("transient error")
			}
			return nil
		},
		idempotent: true,
	}

	nodes := []orchestrate.Node{
		{ID: "A", Runner: compRunner},
		{ID: "B", Runner: errorRunner(errors.New("fail")), Deps: []string{"A"}},
	}

	_, err := orchestrate.ExecuteDAG(context.Background(), orchestrate.DAGConfig{
		ErrorStrategy: orchestrate.Compensate,
		CompensateCfg: &orchestrate.CompensateConfig{
			Strategy:   orchestrate.BackwardCompensate,
			MaxRetries: 3,
		},
	}, nodes, makeReq("start"))

	if err == nil {
		t.Fatal("expected error from failed node B")
	}
	if compensateCount.Load() < 2 {
		t.Errorf("expected at least 2 compensation attempts, got %d", compensateCount.Load())
	}
}

func TestIdempotentChecker_NonIdempotent_NoRetry(t *testing.T) {
	var compensateCount atomic.Int32
	compRunner := &mockCompensatableRunner{
		runFn: func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
			return &schema.RunResponse{Messages: req.Messages}, nil
		},
		compensateFn: func(_ context.Context, _ *schema.RunResponse) error {
			compensateCount.Add(1)
			return errors.New("compensation failed")
		},
		idempotent: false,
	}

	nodes := []orchestrate.Node{
		{ID: "A", Runner: compRunner},
		{ID: "B", Runner: errorRunner(errors.New("fail")), Deps: []string{"A"}},
	}

	_, _ = orchestrate.ExecuteDAG(context.Background(), orchestrate.DAGConfig{
		ErrorStrategy: orchestrate.Compensate,
		CompensateCfg: &orchestrate.CompensateConfig{
			Strategy:   orchestrate.BackwardCompensate,
			MaxRetries: 5,
		},
	}, nodes, makeReq("start"))

	if compensateCount.Load() != 1 {
		t.Errorf("expected exactly 1 compensation attempt for non-idempotent, got %d", compensateCount.Load())
	}
}

// =============================================================================
// Multi-feature combination integration tests
// =============================================================================

func TestIntegration_CheckpointWithEventHandler(t *testing.T) {
	handler := &testEventHandler{}
	store := orchestrate.NewInMemoryCheckpointStore()

	nodes := []orchestrate.Node{
		{ID: "A", Runner: appendRunner("-A")},
		{ID: "B", Runner: appendRunner("-B"), Deps: []string{"A"}},
		{ID: "C", Runner: appendRunner("-C"), Deps: []string{"A"}},
	}

	result, err := orchestrate.RunDAG(context.Background(), nodes, makeReq("start"),
		orchestrate.WithCheckpointStore(store),
		orchestrate.WithEventHandler(handler),
		orchestrate.WithMaxConcurrency(2),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, id := range []string{"A", "B", "C"} {
		if result.NodeStatus[id] != orchestrate.NodeDone {
			t.Errorf("node %s status = %d, want NodeDone", id, result.NodeStatus[id])
		}
	}

	handler.mu.Lock()
	if len(handler.starts) != 3 {
		t.Errorf("expected 3 start events, got %d", len(handler.starts))
	}
	handler.mu.Unlock()

	saved, _ := store.LoadAll(context.Background(), "test-session")
	if len(saved) != 3 {
		t.Errorf("expected 3 checkpoints, got %d", len(saved))
	}

	if len(result.Timeline) != 3 {
		t.Errorf("expected 3 timeline entries, got %d", len(result.Timeline))
	}
}

func TestIntegration_PriorityWithResources(t *testing.T) {
	var mu sync.Mutex
	var order []string

	makeOrderRunner := func(id string) *mockRunner {
		return newMockRunner(func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
			mu.Lock()
			order = append(order, id)
			mu.Unlock()
			time.Sleep(5 * time.Millisecond)
			return &schema.RunResponse{Messages: req.Messages}, nil
		})
	}

	nodes := []orchestrate.Node{
		{ID: "root", Runner: passthroughRunner()},
		{ID: "low", Runner: makeOrderRunner("low"), Deps: []string{"root"}, Priority: 1, ResourceTags: []string{"gpu"}},
		{ID: "high", Runner: makeOrderRunner("high"), Deps: []string{"root"}, Priority: 100, ResourceTags: []string{"gpu"}},
	}

	result, err := orchestrate.RunDAG(context.Background(), nodes, makeReq("start"),
		orchestrate.WithPriorityScheduling(false),
		orchestrate.WithMaxConcurrency(1),
		orchestrate.WithResourceLimits(map[string]int{"gpu": 1}),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, id := range []string{"low", "high"} {
		if result.NodeStatus[id] != orchestrate.NodeDone {
			t.Errorf("node %s status = %d, want NodeDone", id, result.NodeStatus[id])
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(order) == 2 && order[0] != "high" {
		t.Errorf("expected high first, got order: %v", order)
	}
}

func TestIntegration_ConditionalSkipWithCheckpoint(t *testing.T) {
	store := orchestrate.NewInMemoryCheckpointStore()
	handler := &testEventHandler{}

	nodes := []orchestrate.Node{
		{ID: "A", Runner: appendRunner("-A")},
		{
			ID: "skip", Runner: appendRunner("-skip"), Deps: []string{"A"},
			Condition: func(_ map[string]*schema.RunResponse) bool { return false },
		},
		{ID: "C", Runner: appendRunner("-C"), Deps: []string{"A"}},
	}

	result, err := orchestrate.RunDAG(context.Background(), nodes, makeReq("start"),
		orchestrate.WithCheckpointStore(store),
		orchestrate.WithEventHandler(handler),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.NodeStatus["skip"] != orchestrate.NodeSkipped {
		t.Errorf("skip status = %d, want NodeSkipped", result.NodeStatus["skip"])
	}

	saved, _ := store.LoadAll(context.Background(), "test-session")
	if _, ok := saved["skip"]; ok {
		t.Error("skipped node should not be checkpointed")
	}
	if _, ok := saved["A"]; !ok {
		t.Error("node A should be checkpointed")
	}
	if _, ok := saved["C"]; !ok {
		t.Error("node C should be checkpointed")
	}
}

func TestIntegration_BackpressureWithTimeline(t *testing.T) {
	runner := newMockRunner(func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		time.Sleep(10 * time.Millisecond)
		return &schema.RunResponse{Messages: req.Messages}, nil
	})

	nodes := []orchestrate.Node{
		{ID: "root", Runner: passthroughRunner()},
		{ID: "A", Runner: runner, Deps: []string{"root"}},
		{ID: "B", Runner: runner, Deps: []string{"root"}},
	}

	result, err := orchestrate.RunDAG(context.Background(), nodes, makeReq("start"),
		orchestrate.WithBackpressure(&orchestrate.BackpressureConfig{
			InitialConcurrency: 2,
			MinConcurrency:     1,
			MaxConcurrency:     4,
			LatencyThreshold:   time.Second,
			AdjustInterval:     100 * time.Millisecond,
		}),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Timeline) != 3 {
		t.Errorf("expected 3 timeline entries, got %d", len(result.Timeline))
	}
	for _, id := range []string{"root", "A", "B"} {
		if result.NodeStatus[id] != orchestrate.NodeDone {
			t.Errorf("node %s status = %d, want NodeDone", id, result.NodeStatus[id])
		}
	}
}

func TestIntegration_ForwardRecoveryWithEventHandler(t *testing.T) {
	handler := &testEventHandler{}
	var callCount atomic.Int32

	flakyRunner := newMockRunner(func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		count := callCount.Add(1)
		if count <= 1 {
			return nil, errors.New("transient")
		}
		return &schema.RunResponse{Messages: req.Messages}, nil
	})

	nodes := []orchestrate.Node{
		{ID: "A", Runner: flakyRunner},
	}

	result, err := orchestrate.ExecuteDAG(context.Background(), orchestrate.DAGConfig{
		ErrorStrategy: orchestrate.Compensate,
		CompensateCfg: &orchestrate.CompensateConfig{
			Strategy:   orchestrate.ForwardRecovery,
			MaxRetries: 3,
		},
		EventHandler: handler,
	}, nodes, makeReq("start"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.NodeStatus["A"] != orchestrate.NodeDone {
		t.Errorf("A status = %d, want NodeDone", result.NodeStatus["A"])
	}

	handler.mu.Lock()
	defer handler.mu.Unlock()

	var hasFailed, hasDone bool
	for _, c := range handler.completes {
		if c.nodeID == "A" && c.status == orchestrate.NodeFailed {
			hasFailed = true
		}
		if c.nodeID == "A" && c.status == orchestrate.NodeDone {
			hasDone = true
		}
	}
	if !hasFailed {
		t.Error("expected NodeFailed event for A before recovery")
	}
	if !hasDone {
		t.Error("expected NodeDone event for A after recovery")
	}
}

func TestIntegration_CheckpointResume_WithEventHandler(t *testing.T) {
	store := orchestrate.NewInMemoryCheckpointStore()
	handler := &testEventHandler{}

	nodes := []orchestrate.Node{
		{ID: "A", Runner: appendRunner("-A")},
		{ID: "B", Runner: appendRunner("-B"), Deps: []string{"A"}},
		{ID: "C", Runner: appendRunner("-C"), Deps: []string{"B"}},
	}

	_ = store.Save(context.Background(), "test-session", "A", &schema.RunResponse{
		Messages: []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "start-A")},
	})
	_ = store.Save(context.Background(), "test-session", "B", &schema.RunResponse{
		Messages: []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "start-A-B")},
	})

	result, err := orchestrate.RunDAG(context.Background(), nodes, makeReq("start"),
		orchestrate.WithCheckpointStore(store),
		orchestrate.WithEventHandler(handler),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, id := range []string{"A", "B", "C"} {
		if result.NodeStatus[id] != orchestrate.NodeDone {
			t.Errorf("node %s status = %d, want NodeDone", id, result.NodeStatus[id])
		}
	}

	handler.mu.Lock()
	defer handler.mu.Unlock()
	if len(handler.starts) != 1 || handler.starts[0] != "C" {
		t.Errorf("expected only C to start, got %v", handler.starts)
	}
}

func TestIntegration_LargeDAG_ConcurrencyAndTimeline(t *testing.T) {
	const numFanOut = 20
	handler := &testEventHandler{}

	nodes := make([]orchestrate.Node, 0, numFanOut+2)
	nodes = append(nodes, orchestrate.Node{ID: "root", Runner: passthroughRunner()})
	for i := range numFanOut {
		nodes = append(nodes, orchestrate.Node{
			ID:     fmt.Sprintf("worker-%d", i),
			Runner: appendRunner(fmt.Sprintf("-%d", i)),
			Deps:   []string{"root"},
		})
	}
	deps := make([]string, numFanOut)
	for i := range numFanOut {
		deps[i] = fmt.Sprintf("worker-%d", i)
	}
	nodes = append(nodes, orchestrate.Node{ID: "sink", Runner: passthroughRunner(), Deps: deps})

	result, err := orchestrate.RunDAG(context.Background(), nodes, makeReq("start"),
		orchestrate.WithMaxConcurrency(5),
		orchestrate.WithEventHandler(handler),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, n := range nodes {
		if result.NodeStatus[n.ID] != orchestrate.NodeDone {
			t.Errorf("node %s status = %d, want NodeDone", n.ID, result.NodeStatus[n.ID])
		}
	}

	if len(result.Timeline) != numFanOut+2 {
		t.Errorf("expected %d timeline entries, got %d", numFanOut+2, len(result.Timeline))
	}

	handler.mu.Lock()
	defer handler.mu.Unlock()
	if len(handler.starts) != numFanOut+2 {
		t.Errorf("expected %d start events, got %d", numFanOut+2, len(handler.starts))
	}
}

func TestIntegration_CriticalPathWithEventHandler(t *testing.T) {
	handler := &testEventHandler{}

	nodes := []orchestrate.Node{
		{ID: "A", Runner: appendRunner("-A")},
		{ID: "B", Runner: appendRunner("-B"), Deps: []string{"A"}},
		{ID: "C", Runner: appendRunner("-C"), Deps: []string{"A"}},
		{ID: "D", Runner: appendRunner("-D"), Deps: []string{"B", "C"}},
	}

	result, err := orchestrate.RunDAG(context.Background(), nodes, makeReq("start"),
		orchestrate.WithPriorityScheduling(true),
		orchestrate.WithMaxConcurrency(2),
		orchestrate.WithEventHandler(handler),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, id := range []string{"A", "B", "C", "D"} {
		if result.NodeStatus[id] != orchestrate.NodeDone {
			t.Errorf("node %s status = %d, want NodeDone", id, result.NodeStatus[id])
		}
	}
}

func TestIntegration_EarlyExitWithTimeline(t *testing.T) {
	nodes := []orchestrate.Node{
		{ID: "A", Runner: appendRunner("-A")},
		{ID: "B", Runner: appendRunner("-B"), Deps: []string{"A"}},
		{ID: "C", Runner: appendRunner("-C"), Deps: []string{"B"}},
	}

	result, err := orchestrate.RunDAG(context.Background(), nodes, makeReq("start"),
		orchestrate.WithEarlyExit(func(nodeID string, _ *schema.RunResponse) bool {
			return nodeID == "A"
		}),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Timeline) == 0 {
		t.Error("expected at least one timeline entry")
	}
	if result.NodeStatus["A"] != orchestrate.NodeDone {
		t.Errorf("A status = %d, want NodeDone", result.NodeStatus["A"])
	}
}

func TestIntegration_SkipOptionalWithEventHandler(t *testing.T) {
	handler := &testEventHandler{}

	nodes := []orchestrate.Node{
		{ID: "root", Runner: passthroughRunner()},
		{ID: "opt", Runner: errorRunner(errors.New("fail")), Deps: []string{"root"}, Optional: true},
		{ID: "normal", Runner: appendRunner("-ok"), Deps: []string{"root"}},
	}

	result, err := orchestrate.RunDAG(context.Background(), nodes, makeReq("start"),
		orchestrate.WithErrorStrategy(orchestrate.Skip),
		orchestrate.WithEventHandler(handler),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.NodeStatus["opt"] != orchestrate.NodeSkipped {
		t.Errorf("opt status = %d, want NodeSkipped", result.NodeStatus["opt"])
	}
	if result.NodeStatus["normal"] != orchestrate.NodeDone {
		t.Errorf("normal status = %d, want NodeDone", result.NodeStatus["normal"])
	}

	handler.mu.Lock()
	defer handler.mu.Unlock()

	var foundFailed bool
	for _, c := range handler.completes {
		if c.nodeID == "opt" && c.status == orchestrate.NodeFailed {
			foundFailed = true
		}
	}
	if !foundFailed {
		t.Error("expected NodeFailed event for optional node")
	}
}

// =============================================================================
// BuildDAG (Edge-based DAG construction) integration tests
// =============================================================================

func TestBuildDAG_LinearChain(t *testing.T) {
	nodes := []orchestrate.Node{
		{ID: "A", Runner: appendRunner("-A")},
		{ID: "B", Runner: appendRunner("-B")},
		{ID: "C", Runner: appendRunner("-C")},
	}
	edges := []orchestrate.Edge{
		{From: "A", To: "B"},
		{From: "B", To: "C"},
	}

	built, err := orchestrate.BuildDAG(nodes, edges)
	if err != nil {
		t.Fatalf("BuildDAG error: %v", err)
	}

	result, err := orchestrate.RunDAG(context.Background(), built, makeReq("start"))
	if err != nil {
		t.Fatalf("RunDAG error: %v", err)
	}
	got := result.FinalOutput.Messages[0].Text()
	if got != "start-A-B-C" {
		t.Errorf("got %q, want %q", got, "start-A-B-C")
	}
}

func TestBuildDAG_Diamond(t *testing.T) {
	nodes := []orchestrate.Node{
		{ID: "A", Runner: appendRunner("-A")},
		{ID: "B", Runner: appendRunner("-B")},
		{ID: "C", Runner: appendRunner("-C")},
		{ID: "D", Runner: passthroughRunner()},
	}
	edges := []orchestrate.Edge{
		{From: "A", To: "B"},
		{From: "A", To: "C"},
		{From: "B", To: "D"},
		{From: "C", To: "D"},
	}

	built, err := orchestrate.BuildDAG(nodes, edges)
	if err != nil {
		t.Fatalf("BuildDAG error: %v", err)
	}

	result, err := orchestrate.RunDAG(context.Background(), built, makeReq("start"),
		orchestrate.WithMaxConcurrency(2),
	)
	if err != nil {
		t.Fatalf("RunDAG error: %v", err)
	}
	for _, id := range []string{"A", "B", "C", "D"} {
		if result.NodeStatus[id] != orchestrate.NodeDone {
			t.Errorf("node %s status = %d, want NodeDone", id, result.NodeStatus[id])
		}
	}
}

func TestBuildDAG_WithEventHandler(t *testing.T) {
	handler := &testEventHandler{}
	nodes := []orchestrate.Node{
		{ID: "A", Runner: appendRunner("-A")},
		{ID: "B", Runner: appendRunner("-B")},
		{ID: "C", Runner: appendRunner("-C")},
	}
	edges := []orchestrate.Edge{
		{From: "A", To: "B"},
		{From: "A", To: "C"},
	}

	built, err := orchestrate.BuildDAG(nodes, edges)
	if err != nil {
		t.Fatalf("BuildDAG error: %v", err)
	}

	result, err := orchestrate.RunDAG(context.Background(), built, makeReq("start"),
		orchestrate.WithEventHandler(handler),
	)
	if err != nil {
		t.Fatalf("RunDAG error: %v", err)
	}

	handler.mu.Lock()
	defer handler.mu.Unlock()
	if len(handler.starts) != 3 {
		t.Errorf("expected 3 start events, got %d: %v", len(handler.starts), handler.starts)
	}
	doneCount := 0
	for _, c := range handler.completes {
		if c.status == orchestrate.NodeDone {
			doneCount++
		}
	}
	if doneCount != 3 {
		t.Errorf("expected 3 NodeDone events, got %d", doneCount)
	}
	for _, id := range []string{"A", "B", "C"} {
		if result.NodeStatus[id] != orchestrate.NodeDone {
			t.Errorf("node %s status = %d, want NodeDone", id, result.NodeStatus[id])
		}
	}
}

func TestBuildDAG_WithCheckpointAndPriority(t *testing.T) {
	store := orchestrate.NewInMemoryCheckpointStore()
	nodes := []orchestrate.Node{
		{ID: "A", Runner: appendRunner("-A"), Priority: 10},
		{ID: "B", Runner: appendRunner("-B"), Priority: 1},
		{ID: "C", Runner: appendRunner("-C"), Priority: 5},
		{ID: "D", Runner: passthroughRunner()},
	}
	edges := []orchestrate.Edge{
		{From: "A", To: "B"},
		{From: "A", To: "C"},
		{From: "B", To: "D"},
		{From: "C", To: "D"},
	}

	built, err := orchestrate.BuildDAG(nodes, edges)
	if err != nil {
		t.Fatalf("BuildDAG error: %v", err)
	}

	result, err := orchestrate.RunDAG(context.Background(), built, makeReq("start"),
		orchestrate.WithCheckpointStore(store),
		orchestrate.WithPriorityScheduling(false),
		orchestrate.WithMaxConcurrency(1),
	)
	if err != nil {
		t.Fatalf("RunDAG error: %v", err)
	}

	for _, id := range []string{"A", "B", "C", "D"} {
		if result.NodeStatus[id] != orchestrate.NodeDone {
			t.Errorf("node %s status = %d, want NodeDone", id, result.NodeStatus[id])
		}
	}
	saved, _ := store.LoadAll(context.Background(), "test-session")
	if len(saved) != 4 {
		t.Errorf("expected 4 checkpoints, got %d", len(saved))
	}
}

func TestBuildDAG_WithCompensation(t *testing.T) {
	var compensated []string
	var mu sync.Mutex

	compRunner := &mockCompensatableRunner{
		runFn: func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
			return &schema.RunResponse{Messages: req.Messages}, nil
		},
		compensateFn: func(_ context.Context, _ *schema.RunResponse) error {
			mu.Lock()
			compensated = append(compensated, "A")
			mu.Unlock()
			return nil
		},
		idempotent: true,
	}

	nodes := []orchestrate.Node{
		{ID: "A", Runner: compRunner},
		{ID: "B", Runner: errorRunner(errors.New("fail"))},
	}
	edges := []orchestrate.Edge{
		{From: "A", To: "B"},
	}

	built, err := orchestrate.BuildDAG(nodes, edges)
	if err != nil {
		t.Fatalf("BuildDAG error: %v", err)
	}

	_, err = orchestrate.RunDAG(context.Background(), built, makeReq("start"),
		orchestrate.WithCompensation(&orchestrate.CompensateConfig{
			Strategy:   orchestrate.BackwardCompensate,
			MaxRetries: 1,
		}),
	)
	if err == nil {
		t.Fatal("expected error")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(compensated) != 1 || compensated[0] != "A" {
		t.Errorf("expected A compensated, got %v", compensated)
	}
}

func TestBuildDAG_WithConditionalSkip(t *testing.T) {
	nodes := []orchestrate.Node{
		{ID: "A", Runner: appendRunner("-A")},
		{
			ID: "B", Runner: appendRunner("-B"),
			Condition: func(_ map[string]*schema.RunResponse) bool { return false },
		},
		{ID: "C", Runner: appendRunner("-C")},
	}
	edges := []orchestrate.Edge{
		{From: "A", To: "B"},
		{From: "A", To: "C"},
	}

	built, err := orchestrate.BuildDAG(nodes, edges)
	if err != nil {
		t.Fatalf("BuildDAG error: %v", err)
	}

	result, err := orchestrate.RunDAG(context.Background(), built, makeReq("start"))
	if err != nil {
		t.Fatalf("RunDAG error: %v", err)
	}
	if result.NodeStatus["B"] != orchestrate.NodeSkipped {
		t.Errorf("B status = %d, want NodeSkipped", result.NodeStatus["B"])
	}
	if result.NodeStatus["C"] != orchestrate.NodeDone {
		t.Errorf("C status = %d, want NodeDone", result.NodeStatus["C"])
	}
}

func TestBuildDAG_WithResourceLimits(t *testing.T) {
	var maxConcurrentGPU atomic.Int32
	var currentGPU atomic.Int32

	gpuRunner := newMockRunner(func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		cur := currentGPU.Add(1)
		for {
			old := maxConcurrentGPU.Load()
			if cur <= old || maxConcurrentGPU.CompareAndSwap(old, cur) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		currentGPU.Add(-1)
		return &schema.RunResponse{Messages: req.Messages}, nil
	})

	nodes := []orchestrate.Node{
		{ID: "root", Runner: passthroughRunner()},
		{ID: "A", Runner: gpuRunner, ResourceTags: []string{"gpu"}},
		{ID: "B", Runner: gpuRunner, ResourceTags: []string{"gpu"}},
		{ID: "C", Runner: gpuRunner, ResourceTags: []string{"gpu"}},
	}
	edges := []orchestrate.Edge{
		{From: "root", To: "A"},
		{From: "root", To: "B"},
		{From: "root", To: "C"},
	}

	built, err := orchestrate.BuildDAG(nodes, edges)
	if err != nil {
		t.Fatalf("BuildDAG error: %v", err)
	}

	_, err = orchestrate.RunDAG(context.Background(), built, makeReq("start"),
		orchestrate.WithResourceLimits(map[string]int{"gpu": 1}),
	)
	if err != nil {
		t.Fatalf("RunDAG error: %v", err)
	}
	if maxConcurrentGPU.Load() > 1 {
		t.Errorf("max concurrent GPU = %d, want <= 1", maxConcurrentGPU.Load())
	}
}

func TestBuildDAG_LargeFanOutFanIn(t *testing.T) {
	const numWorkers = 15
	handler := &testEventHandler{}

	nodes := make([]orchestrate.Node, 0, numWorkers+2)
	nodes = append(nodes, orchestrate.Node{ID: "src", Runner: passthroughRunner()})
	for i := range numWorkers {
		nodes = append(nodes, orchestrate.Node{
			ID:     fmt.Sprintf("w%d", i),
			Runner: appendRunner(fmt.Sprintf("-%d", i)),
		})
	}
	nodes = append(nodes, orchestrate.Node{ID: "sink", Runner: passthroughRunner()})

	edges := make([]orchestrate.Edge, 0, numWorkers*2)
	for i := range numWorkers {
		edges = append(edges, orchestrate.Edge{From: "src", To: fmt.Sprintf("w%d", i)})
		edges = append(edges, orchestrate.Edge{From: fmt.Sprintf("w%d", i), To: "sink"})
	}

	built, err := orchestrate.BuildDAG(nodes, edges)
	if err != nil {
		t.Fatalf("BuildDAG error: %v", err)
	}

	result, err := orchestrate.RunDAG(context.Background(), built, makeReq("start"),
		orchestrate.WithMaxConcurrency(4),
		orchestrate.WithEventHandler(handler),
	)
	if err != nil {
		t.Fatalf("RunDAG error: %v", err)
	}
	for _, n := range built {
		if result.NodeStatus[n.ID] != orchestrate.NodeDone {
			t.Errorf("node %s status = %d, want NodeDone", n.ID, result.NodeStatus[n.ID])
		}
	}
	if len(result.Timeline) != numWorkers+2 {
		t.Errorf("expected %d timeline entries, got %d", numWorkers+2, len(result.Timeline))
	}
}

func TestBuildDAG_ValidationErrors(t *testing.T) {
	// Duplicate node ID.
	_, err := orchestrate.BuildDAG(
		[]orchestrate.Node{{ID: "A"}, {ID: "A"}},
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("expected duplicate error, got %v", err)
	}

	// Edge references unknown node (From).
	_, err = orchestrate.BuildDAG(
		[]orchestrate.Node{{ID: "A"}},
		[]orchestrate.Edge{{From: "X", To: "A"}},
	)
	if err == nil || !strings.Contains(err.Error(), "unknown node") {
		t.Errorf("expected unknown node error, got %v", err)
	}

	// Edge references unknown node (To).
	_, err = orchestrate.BuildDAG(
		[]orchestrate.Node{{ID: "A"}},
		[]orchestrate.Edge{{From: "A", To: "X"}},
	)
	if err == nil || !strings.Contains(err.Error(), "unknown node") {
		t.Errorf("expected unknown node error, got %v", err)
	}

	// Node has Deps set (cannot mix styles).
	_, err = orchestrate.BuildDAG(
		[]orchestrate.Node{{ID: "A"}, {ID: "B", Deps: []string{"A"}}},
		[]orchestrate.Edge{{From: "A", To: "B"}},
	)
	if err == nil || !strings.Contains(err.Error(), "cannot mix") {
		t.Errorf("expected 'cannot mix' error, got %v", err)
	}
}

func TestBuildDAG_CycleDetection(t *testing.T) {
	nodes := []orchestrate.Node{
		{ID: "A"},
		{ID: "B"},
		{ID: "C"},
	}
	edges := []orchestrate.Edge{
		{From: "A", To: "B"},
		{From: "B", To: "C"},
		{From: "C", To: "A"},
	}

	_, err := orchestrate.BuildDAG(nodes, edges)
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Errorf("expected cycle error, got %v", err)
	}
}
