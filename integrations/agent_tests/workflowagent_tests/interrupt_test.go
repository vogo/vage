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
	"strings"
	"sync/atomic"
	"testing"

	"github.com/vogo/vage/agent"
	"github.com/vogo/vage/agent/workflowagent"
	"github.com/vogo/vage/integrations/internal/subagent"
	"github.com/vogo/vage/orchestrate"
	"github.com/vogo/vage/schema"
)

// countingAgent records how many times it ran, so tests can prove the steps
// after a suspension never execute.
func countingAgent(id string, runs *atomic.Int32) agent.Agent {
	return agent.NewCustomAgent(
		agent.Config{ID: id},
		func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
			runs.Add(1)

			return &schema.RunResponse{
				Messages: append(req.Messages,
					schema.NewTextMessage(schema.ProtocolOpenAIChat, schema.RoleAssistant, id+" ran")),
				Usage: &schema.Usage{TotalTokens: 100},
			}, nil
		},
	)
}

// TestWorkflowSequence_SuspendedStep_StopsWithError: a suspended middle step
// has produced no output, so the workflow must fail rather than build the
// remaining steps on a turn no human ever decided.
func TestWorkflowSequence_SuspendedStep_StopsWithError(t *testing.T) {
	var firstRuns, lastRuns, handlerRuns atomic.Int32

	wf := workflowagent.New(
		agent.Config{ID: "wf"},
		countingAgent("step-1", &firstRuns),
		subagent.Suspending("hitl-agent", &handlerRuns),
		countingAgent("step-3", &lastRuns),
	)

	resp, err := wf.Run(context.Background(), subagent.Request("sess-wf", "go"))
	if err == nil {
		t.Fatalf("expected an error, got response %+v", resp)
	}
	if resp != nil {
		t.Errorf("response = %+v, want nil", resp)
	}

	for _, want := range []string{"step 2", "hitl-agent", "suspended", "nested human-in-the-loop is not supported"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}

	if firstRuns.Load() != 1 {
		t.Errorf("step 1 ran %d times, want 1", firstRuns.Load())
	}
	if lastRuns.Load() != 0 {
		t.Errorf("step 3 ran %d times, want 0", lastRuns.Load())
	}
	if handlerRuns.Load() != 0 {
		t.Errorf("flagged tool handler ran %d times, want 0", handlerRuns.Load())
	}
}

// TestWorkflowDAG_SuspendedNode_StopsWithError covers the DAG mode, whose
// protection comes from the orchestrate node boundary.
func TestWorkflowDAG_SuspendedNode_StopsWithError(t *testing.T) {
	var downstreamRuns, handlerRuns atomic.Int32

	wf, err := workflowagent.NewDAG(agent.Config{ID: "wf-dag"}, orchestrate.DAGConfig{},
		[]orchestrate.Node{
			{ID: "A", Runner: subagent.Suspending("hitl-agent", &handlerRuns)},
			{ID: "B", Deps: []string{"A"}, Runner: countingAgent("B", &downstreamRuns)},
		})
	if err != nil {
		t.Fatalf("NewDAG: %v", err)
	}

	resp, runErr := wf.Run(context.Background(), subagent.Request("sess-dag", "go"))
	if runErr == nil {
		t.Fatalf("expected an error, got response %+v", resp)
	}
	if !strings.Contains(runErr.Error(), "nested human-in-the-loop is not supported") {
		t.Errorf("error %q does not explain the unsupported nesting", runErr)
	}
	if downstreamRuns.Load() != 0 {
		t.Errorf("downstream node ran %d times, want 0", downstreamRuns.Load())
	}
}

// TestWorkflowLoop_SuspendedBody_StopsWithError covers the loop mode, whose
// protection comes from the orchestrate loop boundary.
func TestWorkflowLoop_SuspendedBody_StopsWithError(t *testing.T) {
	var handlerRuns atomic.Int32

	wf := workflowagent.NewLoop(agent.Config{ID: "wf-loop"},
		subagent.Suspending("hitl-agent", &handlerRuns),
		func(_ *schema.RunResponse) bool { return true }, 3)

	resp, err := wf.Run(context.Background(), subagent.Request("sess-loop", "go"))
	if err == nil {
		t.Fatalf("expected an error, got response %+v", resp)
	}
	if !strings.Contains(err.Error(), "nested human-in-the-loop is not supported") {
		t.Errorf("error %q does not explain the unsupported nesting", err)
	}
}

// TestWorkflowSequence_CompletingSteps_StillSucceed is the control: ordinary
// sequential workflows, including usage accumulation, are unchanged.
func TestWorkflowSequence_CompletingSteps_StillSucceed(t *testing.T) {
	var firstRuns, lastRuns atomic.Int32

	wf := workflowagent.New(agent.Config{ID: "wf"},
		countingAgent("step-1", &firstRuns), countingAgent("step-2", &lastRuns))

	resp, err := wf.Run(context.Background(), subagent.Request("sess-wf", "go"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if firstRuns.Load() != 1 || lastRuns.Load() != 1 {
		t.Fatalf("step runs = (%d, %d), want (1, 1)", firstRuns.Load(), lastRuns.Load())
	}
	if resp.Usage == nil || resp.Usage.TotalTokens != 200 {
		t.Errorf("Usage = %+v, want TotalTokens 200", resp.Usage)
	}
}
