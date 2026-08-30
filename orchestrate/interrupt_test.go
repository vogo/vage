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
	"sync/atomic"
	"testing"

	"github.com/vogo/vage/schema"
)

// suspendedResponse builds the half-written turn a TaskAgent returns when a
// tool batch is frozen for a human decision: assistant text exists, but the
// run has not terminated.
func suspendedResponse() *schema.RunResponse {
	return &schema.RunResponse{
		Messages:   []schema.Message{schema.NewTextMessage(schema.ProtocolOpenAIChat, schema.RoleAssistant, "half")},
		StopReason: schema.StopReasonInterrupted,
		Interrupt:  &schema.InterruptDescriptor{InterruptID: "int-1"},
	}
}

// interruptedRunner returns a suspended response and counts its invocations,
// so tests can prove no retry path calls it twice.
func interruptedRunner(calls *atomic.Int32, resp *schema.RunResponse) *mockRunner {
	return newMockRunner(func(_ context.Context, _ *schema.RunRequest) (*schema.RunResponse, error) {
		calls.Add(1)
		return resp, nil
	})
}

func assertInterruptedErr(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !errors.Is(err, ErrInterruptedRunner) {
		t.Fatalf("error %v does not wrap ErrInterruptedRunner", err)
	}
}

// =============================================================================
// Defensive dual-signal detection
// =============================================================================

func TestRejectInterrupted_EitherSignalAlone(t *testing.T) {
	cases := []struct {
		name string
		resp *schema.RunResponse
		want bool
	}{
		{"both signals", suspendedResponse(), true},
		{"stop reason only", &schema.RunResponse{StopReason: schema.StopReasonInterrupted}, true},
		{"interrupt field only", &schema.RunResponse{Interrupt: &schema.InterruptDescriptor{InterruptID: "x"}}, true},
		{"complete", &schema.RunResponse{StopReason: schema.StopReasonComplete}, false},
		{"max iterations", &schema.RunResponse{StopReason: schema.StopReasonMaxIterations}, false},
		{"budget exhausted", &schema.RunResponse{StopReason: schema.StopReasonBudgetExhausted}, false},
		{"empty", &schema.RunResponse{}, false},
		{"nil", nil, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := rejectInterrupted("node \"A\"", tc.resp)
			if got := err != nil; got != tc.want {
				t.Fatalf("rejected = %v, want %v (err=%v)", got, tc.want, err)
			}
			if err != nil {
				assertInterruptedErr(t, err)
			}
		})
	}
}

// =============================================================================
// DAG node boundary
// =============================================================================

// TestDAG_InterruptedNode_FailsAndDoesNotFeedDownstream is the core DAG
// guarantee: the suspended response never lands in NodeResults, never reaches
// a checkpoint, and downstream nodes are not run on it.
func TestDAG_InterruptedNode_FailsAndDoesNotFeedDownstream(t *testing.T) {
	var upCalls, downCalls atomic.Int32
	store := NewInMemoryCheckpointStore()

	nodes := []Node{
		{ID: "A", Runner: interruptedRunner(&upCalls, suspendedResponse())},
		{ID: "B", Deps: []string{"A"}, Runner: newMockRunner(
			func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
				downCalls.Add(1)
				return &schema.RunResponse{Messages: req.Messages}, nil
			},
		)},
	}

	result, err := ExecuteDAG(context.Background(),
		DAGConfig{ErrorStrategy: Abort, CheckpointStore: store}, nodes, makeReq("go"))
	assertInterruptedErr(t, err)

	if downCalls.Load() != 0 {
		t.Errorf("downstream node ran %d times, want 0", downCalls.Load())
	}
	if _, ok := result.NodeResults["A"]; ok {
		t.Error("suspended response leaked into NodeResults")
	}
	if result.NodeStatus["A"] != NodeFailed {
		t.Errorf("node A status = %v, want NodeFailed", result.NodeStatus["A"])
	}

	saved, loadErr := store.LoadAll(context.Background(), "test-session")
	if loadErr != nil {
		t.Fatalf("LoadAll: %v", loadErr)
	}
	if len(saved) != 0 {
		t.Errorf("checkpoint saved %d entries, want 0", len(saved))
	}
}

// TestDAG_InterruptedNode_NotRetried proves a suspension is not a transient
// failure: re-running would start fresh work, not resume the frozen batch.
func TestDAG_InterruptedNode_NotRetried(t *testing.T) {
	var calls atomic.Int32
	nodes := []Node{{ID: "A", Runner: interruptedRunner(&calls, suspendedResponse()), Retries: 3}}

	_, err := ExecuteDAG(context.Background(), DAGConfig{}, nodes, makeReq("go"))
	assertInterruptedErr(t, err)

	if calls.Load() != 1 {
		t.Fatalf("runner called %d times, want 1", calls.Load())
	}
}

// TestDAG_InterruptedNode_NoEarlyExitOrAggregation checks the response is kept
// away from every consumer, not just from downstream nodes.
func TestDAG_InterruptedNode_NoEarlyExitOrAggregation(t *testing.T) {
	var calls atomic.Int32
	var earlyExitSeen atomic.Int32
	nodes := []Node{{ID: "A", Runner: interruptedRunner(&calls, suspendedResponse())}}

	cfg := DAGConfig{EarlyExitFunc: func(_ string, _ *schema.RunResponse) bool {
		earlyExitSeen.Add(1)
		return false
	}}

	result, err := ExecuteDAG(context.Background(), cfg, nodes, makeReq("go"))
	assertInterruptedErr(t, err)

	if earlyExitSeen.Load() != 0 {
		t.Errorf("EarlyExitFunc saw the suspended response %d times, want 0", earlyExitSeen.Load())
	}
	if result.FinalOutput != nil {
		t.Errorf("FinalOutput = %+v, want nil (aggregation must not run)", result.FinalOutput)
	}
}

// TestDAG_InterruptedOptionalNode_SkipStrategy: even when the caller opts into
// Skip, the suspended response is not consumable — the node is skipped, its
// output discarded, and downstream runs on the original request instead.
func TestDAG_InterruptedOptionalNode_SkipStrategy(t *testing.T) {
	var calls atomic.Int32
	nodes := []Node{
		{ID: "A", Runner: interruptedRunner(&calls, suspendedResponse()), Optional: true},
		{ID: "B", Deps: []string{"A"}, Runner: appendRunner("-b")},
	}

	result, err := ExecuteDAG(context.Background(), DAGConfig{ErrorStrategy: Skip}, nodes, makeReq("go"))
	if err != nil {
		t.Fatalf("Skip strategy should absorb the failure: %v", err)
	}
	if result.NodeStatus["A"] != NodeSkipped {
		t.Errorf("node A status = %v, want NodeSkipped", result.NodeStatus["A"])
	}
	if _, ok := result.NodeResults["A"]; ok {
		t.Error("suspended response leaked into NodeResults")
	}
}

// TestDAG_NormalNode_StillSucceeds guards against over-rejection: responses
// with any other stop reason keep flowing as before.
func TestDAG_NormalNode_StillSucceeds(t *testing.T) {
	nodes := []Node{{ID: "A", Runner: newMockRunner(
		func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
			return &schema.RunResponse{Messages: req.Messages, StopReason: schema.StopReasonMaxIterations}, nil
		},
	)}}

	result, err := ExecuteDAG(context.Background(), DAGConfig{}, nodes, makeReq("go"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.NodeStatus["A"] != NodeDone {
		t.Errorf("node A status = %v, want NodeDone", result.NodeStatus["A"])
	}
}

// =============================================================================
// Loop, conditional, spawn and forward-recovery boundaries
// =============================================================================

func TestLoop_InterruptedBody_StopsImmediately(t *testing.T) {
	var calls atomic.Int32
	loop := LoopNode{
		Body:      interruptedRunner(&calls, suspendedResponse()),
		Condition: func(_ *schema.RunResponse) bool { return true },
		MaxIters:  5,
	}

	resp, err := ExecuteLoop(context.Background(), loop, makeReq("go"))
	assertInterruptedErr(t, err)

	if resp != nil {
		t.Errorf("response = %+v, want nil", resp)
	}
	if calls.Load() != 1 {
		t.Fatalf("body ran %d times, want 1", calls.Load())
	}
}

func TestConditional_InterruptedRunner_SelectsNoBranch(t *testing.T) {
	var calls, branchCalls atomic.Int32
	cn := &ConditionalNode{
		Node: Node{ID: "C", Runner: interruptedRunner(&calls, suspendedResponse())},
		Branches: []Branch{{
			Condition: func(_ map[string]*schema.RunResponse) bool {
				branchCalls.Add(1)
				return true
			},
			TargetID: "next",
		}},
	}

	resp, target, err := ExecuteConditional(context.Background(), cn, makeReq("go"), nil)
	assertInterruptedErr(t, err)

	if resp != nil || target != "" {
		t.Errorf("got (%+v, %q), want (nil, \"\")", resp, target)
	}
	if branchCalls.Load() != 0 {
		t.Errorf("branch condition evaluated %d times, want 0", branchCalls.Load())
	}
}

func TestDynamicSpawn_InterruptedParent_DoesNotSpawn(t *testing.T) {
	var calls, spawnerCalls atomic.Int32
	dsn := &DynamicSpawnNode{
		Node: Node{ID: "S", Runner: interruptedRunner(&calls, suspendedResponse())},
		Spawner: func(_ context.Context, _ *schema.RunResponse) ([]Node, error) {
			spawnerCalls.Add(1)
			return nil, nil
		},
	}

	resp, err := ExecuteDynamicSpawn(context.Background(), dsn, makeReq("go"))
	assertInterruptedErr(t, err)

	if resp != nil {
		t.Errorf("response = %+v, want nil", resp)
	}
	if spawnerCalls.Load() != 0 {
		t.Fatalf("Spawner called %d times, want 0", spawnerCalls.Load())
	}
}

func TestDynamicSpawn_InterruptedChild_FailsAggregation(t *testing.T) {
	var childCalls atomic.Int32
	dsn := &DynamicSpawnNode{
		Node: Node{ID: "S", Runner: passthroughRunner()},
		Spawner: func(_ context.Context, _ *schema.RunResponse) ([]Node, error) {
			return []Node{{ID: "child-1", Runner: interruptedRunner(&childCalls, suspendedResponse())}}, nil
		},
	}

	resp, err := ExecuteDynamicSpawn(context.Background(), dsn, makeReq("go"))
	assertInterruptedErr(t, err)

	if resp != nil {
		t.Errorf("response = %+v, want nil", resp)
	}
}

func TestForwardRecovery_InterruptedRunner_NotRetried(t *testing.T) {
	var calls atomic.Int32
	node := &Node{ID: "A", Runner: interruptedRunner(&calls, suspendedResponse())}

	resp, err := executeForwardRecovery(context.Background(),
		&CompensateConfig{Strategy: ForwardRecovery, MaxRetries: 3}, node, makeReq("go"))
	assertInterruptedErr(t, err)

	if resp != nil {
		t.Errorf("response = %+v, want nil", resp)
	}
	if calls.Load() != 1 {
		t.Fatalf("runner called %d times, want 1", calls.Load())
	}
}
