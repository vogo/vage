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

	"github.com/vogo/aimodel"
	"github.com/vogo/vagent/agent"
	"github.com/vogo/vagent/schema"
)

// Agent executes a sequence of sub-agents in order.
type Agent struct {
	agent.Base
	steps []agent.Agent
}

var (
	_ agent.Agent       = (*Agent)(nil)
	_ agent.StreamAgent = (*Agent)(nil)
)

// New creates a workflow Agent that runs the given steps sequentially.
func New(cfg agent.Config, steps ...agent.Agent) *Agent {
	return &Agent{
		Base:  agent.NewBase(cfg),
		steps: steps,
	}
}

// Steps returns the sub-agents in this workflow.
func (a *Agent) Steps() []agent.Agent { return a.steps }

// Run executes each step sequentially, passing the output of each step as input to the next.
func (a *Agent) Run(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
	start := time.Now()

	if len(a.steps) == 0 {
		return &schema.RunResponse{
			Messages:  req.Messages,
			SessionID: req.SessionID,
			Duration:  time.Since(start).Milliseconds(),
		}, nil
	}

	var totalUsage aimodel.Usage
	hasUsage := false
	currentReq := req
	var lastResp *schema.RunResponse

	for i, step := range a.steps {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		resp, err := step.Run(ctx, currentReq)
		if err != nil {
			return nil, fmt.Errorf("vagent: workflow step %d (%s): %w", i+1, step.ID(), err)
		}

		if resp == nil {
			return nil, fmt.Errorf("vagent: workflow step %d (%s): nil response", i+1, step.ID())
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
		Duration:  time.Since(start).Milliseconds(),
	}
	if hasUsage {
		result.Usage = &totalUsage
	}

	return result, nil
}

// RunStream returns a RunStream that emits lifecycle events as the pipeline executes.
func (a *Agent) RunStream(ctx context.Context, req *schema.RunRequest) (*schema.RunStream, error) {
	return agent.RunToStream(ctx, a, req), nil
}
