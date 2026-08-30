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
	"sync/atomic"
	"testing"

	"github.com/vogo/vage/integrations/internal/subagent"
	"github.com/vogo/vage/orchestrate"
	"github.com/vogo/vage/schema"
)

// countingRunner records invocations so tests can prove downstream work never
// starts on a suspended upstream response.
func countingRunner(runs *atomic.Int32) orchestrate.Runner {
	return runnerFunc(func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		runs.Add(1)

		return &schema.RunResponse{Messages: req.Messages}, nil
	})
}

type runnerFunc func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error)

func (f runnerFunc) Run(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
	return f(ctx, req)
}

// TestDAG_SuspendedTaskAgentNode_IsVisibleFailure runs a real TaskAgent that
// freezes a tool batch as a DAG node. The engine has no interrupt id and no
// resume entry point, so the suspension has to become a node failure that
// keeps the half-written response out of results, checkpoints and aggregation.
func TestDAG_SuspendedTaskAgentNode_IsVisibleFailure(t *testing.T) {
	var downstreamRuns, handlerRuns atomic.Int32
	store := orchestrate.NewInMemoryCheckpointStore()

	nodes := []orchestrate.Node{
		{ID: "ask", Runner: subagent.Suspending("hitl-agent", &handlerRuns)},
		{ID: "summarize", Deps: []string{"ask"}, Runner: countingRunner(&downstreamRuns)},
	}

	result, err := orchestrate.ExecuteDAG(context.Background(),
		orchestrate.DAGConfig{ErrorStrategy: orchestrate.Abort, CheckpointStore: store},
		nodes, subagent.Request("sess-dag", "please ask"))

	if err == nil {
		t.Fatalf("expected an error, got result %+v", result)
	}
	if !errors.Is(err, orchestrate.ErrInterruptedRunner) {
		t.Fatalf("error %v does not wrap ErrInterruptedRunner", err)
	}

	if downstreamRuns.Load() != 0 {
		t.Errorf("downstream runner ran %d times, want 0", downstreamRuns.Load())
	}
	if handlerRuns.Load() != 0 {
		t.Errorf("flagged tool handler ran %d times, want 0", handlerRuns.Load())
	}
	if _, ok := result.NodeResults["ask"]; ok {
		t.Error("suspended response leaked into NodeResults")
	}
	if result.NodeStatus["ask"] != orchestrate.NodeFailed {
		t.Errorf("node status = %v, want NodeFailed", result.NodeStatus["ask"])
	}

	saved, loadErr := store.LoadAll(context.Background(), "sess-dag")
	if loadErr != nil {
		t.Fatalf("LoadAll: %v", loadErr)
	}
	if len(saved) != 0 {
		t.Errorf("checkpoint saved %d entries, want 0", len(saved))
	}
	if result.FinalOutput != nil {
		t.Errorf("FinalOutput = %+v, want nil (aggregation must not run)", result.FinalOutput)
	}
}

// TestDAG_SuspendedTaskAgentNode_NotRetried: a suspension is not transient.
// Re-running the node would start fresh work and can persist a second pending
// record, so the configured retries must not fire.
func TestDAG_SuspendedTaskAgentNode_NotRetried(t *testing.T) {
	var handlerRuns, nodeRuns atomic.Int32

	ag := subagent.Suspending("hitl-agent", &handlerRuns)
	nodes := []orchestrate.Node{{
		ID:      "ask",
		Retries: 2,
		Runner: runnerFunc(func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
			nodeRuns.Add(1)

			return ag.Run(ctx, req)
		}),
	}}

	_, err := orchestrate.ExecuteDAG(context.Background(), orchestrate.DAGConfig{},
		nodes, subagent.Request("sess-retry", "please ask"))
	if !errors.Is(err, orchestrate.ErrInterruptedRunner) {
		t.Fatalf("error %v does not wrap ErrInterruptedRunner", err)
	}

	if nodeRuns.Load() != 1 {
		t.Fatalf("node runner called %d times, want 1", nodeRuns.Load())
	}
}

// TestDynamicSpawn_SuspendedTaskAgentParent_DoesNotSpawn: the spawner would
// fan a half-written turn out into N children, multiplying the loss.
func TestDynamicSpawn_SuspendedTaskAgentParent_DoesNotSpawn(t *testing.T) {
	var spawnerRuns, handlerRuns atomic.Int32

	dsn := &orchestrate.DynamicSpawnNode{
		Node: orchestrate.Node{ID: "fanout", Runner: subagent.Suspending("hitl-agent", &handlerRuns)},
		Spawner: func(_ context.Context, _ *schema.RunResponse) ([]orchestrate.Node, error) {
			spawnerRuns.Add(1)

			return nil, nil
		},
	}

	resp, err := orchestrate.ExecuteDynamicSpawn(context.Background(), dsn,
		subagent.Request("sess-spawn", "please ask"))
	if !errors.Is(err, orchestrate.ErrInterruptedRunner) {
		t.Fatalf("error %v does not wrap ErrInterruptedRunner", err)
	}
	if resp != nil {
		t.Errorf("response = %+v, want nil", resp)
	}
	if spawnerRuns.Load() != 0 {
		t.Fatalf("Spawner ran %d times, want 0", spawnerRuns.Load())
	}
}

// TestDAG_CompletingTaskAgentNode_StillSucceeds is the control: an ordinary
// TaskAgent node keeps flowing to its downstream, checkpoint and aggregation.
func TestDAG_CompletingTaskAgentNode_StillSucceeds(t *testing.T) {
	var downstreamRuns atomic.Int32
	store := orchestrate.NewInMemoryCheckpointStore()

	nodes := []orchestrate.Node{
		{ID: "answer", Runner: subagent.Completing("plain-agent", "done")},
		{ID: "summarize", Deps: []string{"answer"}, Runner: countingRunner(&downstreamRuns)},
	}

	result, err := orchestrate.ExecuteDAG(context.Background(),
		orchestrate.DAGConfig{CheckpointStore: store}, nodes, subagent.Request("sess-ok", "hello"))
	if err != nil {
		t.Fatalf("ExecuteDAG: %v", err)
	}
	if downstreamRuns.Load() != 1 {
		t.Errorf("downstream runner ran %d times, want 1", downstreamRuns.Load())
	}
	if result.NodeStatus["answer"] != orchestrate.NodeDone {
		t.Errorf("node status = %v, want NodeDone", result.NodeStatus["answer"])
	}
}
