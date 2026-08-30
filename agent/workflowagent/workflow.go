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
	"fmt"
	"time"

	"github.com/vogo/vage/agent"
	"github.com/vogo/vage/orchestrate"
	"github.com/vogo/vage/schema"
)

type workflowMode int

const (
	modeSequence workflowMode = iota
	modeDAG
	modeLoop
)

// Agent executes workflows using the orchestrate engine.
type Agent struct {
	agent.Base
	mode     workflowMode
	steps    []agent.Agent         // sequence mode
	dagCfg   orchestrate.DAGConfig // DAG mode
	dagNodes []orchestrate.Node    // DAG mode
	loopNode orchestrate.LoopNode  // loop mode
}

var (
	_ agent.Agent       = (*Agent)(nil)
	_ agent.StreamAgent = (*Agent)(nil)
)

// New creates a workflow Agent that runs the given steps sequentially.
func New(cfg agent.Config, steps ...agent.Agent) *Agent {
	return &Agent{
		Base:  agent.NewBase(cfg),
		mode:  modeSequence,
		steps: steps,
	}
}

// NewDAG creates a DAG workflow with explicit node dependencies.
// It validates the DAG structure at construction time and returns an error
// if the nodes contain duplicate IDs, missing dependencies, cycles, or disconnected subgraphs.
func NewDAG(cfg agent.Config, dagCfg orchestrate.DAGConfig, nodes []orchestrate.Node) (*Agent, error) {
	if err := orchestrate.ValidateDAG(nodes); err != nil {
		return nil, err
	}
	return &Agent{
		Base:     agent.NewBase(cfg),
		mode:     modeDAG,
		dagCfg:   dagCfg,
		dagNodes: nodes,
	}, nil
}

// NewDAGWithEdges creates a DAG workflow using separate node and edge definitions.
func NewDAGWithEdges(cfg agent.Config, dagCfg orchestrate.DAGConfig, nodes []orchestrate.Node, edges []orchestrate.Edge) (*Agent, error) {
	dagNodes, err := orchestrate.BuildDAG(nodes, edges)
	if err != nil {
		return nil, err
	}
	return &Agent{
		Base:     agent.NewBase(cfg),
		mode:     modeDAG,
		dagCfg:   dagCfg,
		dagNodes: dagNodes,
	}, nil
}

// NewLoop creates a loop workflow.
func NewLoop(cfg agent.Config, body agent.Agent, condition func(*schema.RunResponse) bool, maxIters int) *Agent {
	return &Agent{
		Base: agent.NewBase(cfg),
		mode: modeLoop,
		loopNode: orchestrate.LoopNode{
			Body:      body,
			Condition: condition,
			MaxIters:  maxIters,
		},
	}
}

// Steps returns the sub-agents in this workflow (sequence mode only).
func (a *Agent) Steps() []agent.Agent { return a.steps }

// Run executes the workflow based on its mode.
func (a *Agent) Run(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
	start := time.Now()

	var resp *schema.RunResponse
	var err error

	switch a.mode {
	case modeSequence:
		resp, err = a.runSequence(ctx, req)
	case modeDAG:
		resp, err = a.runDAG(ctx, req)
	case modeLoop:
		resp, err = a.runLoop(ctx, req)
	default:
		return nil, fmt.Errorf("workflowagent: unknown workflow mode %d", a.mode)
	}

	if err != nil {
		return nil, err
	}

	resp.Duration = time.Since(start).Milliseconds()
	return resp, nil
}

func (a *Agent) runSequence(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
	if len(a.steps) == 0 {
		return &schema.RunResponse{
			Messages:  req.Messages,
			SessionID: req.SessionID,
		}, nil
	}

	var totalUsage schema.Usage
	hasUsage := false
	currentReq := req
	var lastResp *schema.RunResponse

	for i, step := range a.steps {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		resp, err := step.Run(ctx, currentReq)
		if err != nil {
			return nil, fmt.Errorf("workflowagent: workflow step %d (%s): %w", i+1, step.ID(), err)
		}

		if resp == nil {
			return nil, fmt.Errorf("workflowagent: workflow step %d (%s): nil response", i+1, step.ID())
		}

		// A suspended step has not produced an output: feeding its half-written
		// messages to the next step would build the rest of the workflow on a
		// turn a human never decided. Stop before accumulating its usage.
		if resp.IsInterrupted() {
			return nil, fmt.Errorf(
				"workflowagent: workflow step %d (%s) suspended for a human decision; "+
					"nested human-in-the-loop is not supported — run that agent directly at the top level to resume it",
				i+1, step.ID(),
			)
		}

		if resp.Usage != nil {
			hasUsage = true
			totalUsage.Add(resp.Usage)
		}

		lastResp = resp
		currentReq = &schema.RunRequest{
			Messages:  resp.Messages,
			SessionID: req.SessionID,
			Options:   req.Options,
			Metadata:  req.Metadata,
		}
	}

	result := &schema.RunResponse{
		Messages:  lastResp.Messages,
		Metadata:  lastResp.Metadata,
		SessionID: req.SessionID,
	}
	if hasUsage {
		result.Usage = &totalUsage
	}

	return result, nil
}

func (a *Agent) runDAG(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
	dagResult, err := orchestrate.ExecuteDAG(ctx, a.dagCfg, a.dagNodes, req)
	if err != nil {
		return nil, err
	}

	resp := dagResult.FinalOutput
	if resp == nil {
		resp = &schema.RunResponse{Messages: req.Messages, SessionID: req.SessionID}
	}
	resp.SessionID = req.SessionID
	if dagResult.Usage != nil {
		resp.Usage = dagResult.Usage
	}
	return resp, nil
}

func (a *Agent) runLoop(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
	resp, err := orchestrate.ExecuteLoop(ctx, a.loopNode, req)
	if err != nil {
		return nil, err
	}

	resp.SessionID = req.SessionID
	return resp, nil
}

// RunStream returns a RunStream that emits lifecycle events as the pipeline executes.
func (a *Agent) RunStream(ctx context.Context, req *schema.RunRequest) (*schema.RunStream, error) {
	return agent.RunToStream(ctx, a, req), nil
}
