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

package eval

import (
	"context"
	"fmt"

	"github.com/vogo/largemodel/schema"
)

// AgentRunFunc is the function signature for running an agent.
// It matches agent.Agent.Run, allowing eval to work without importing the agent package.
type AgentRunFunc func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error)

// RunAndEvaluate runs the agent for each case, fills in Actual, then evaluates.
// It combines agent execution and evaluation into a single workflow.
// Use BatchOption to control concurrency.
func RunAndEvaluate(ctx context.Context, runFn AgentRunFunc, evaluator Evaluator, cases []*EvalCase, opts ...BatchOption) (*EvalReport, error) {
	for _, c := range cases {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("context cancelled before running case %s: %w", c.ID, err)
		}

		if c.Actual != nil {
			continue
		}

		if c.Input == nil {
			continue
		}

		resp, err := runFn(ctx, c.Input)
		if err != nil {
			return nil, fmt.Errorf("agent run failed for case %s: %w", c.ID, err)
		}

		c.Actual = resp
	}

	return BatchEval(ctx, evaluator, cases, opts...)
}
