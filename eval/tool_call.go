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
	"errors"
	"fmt"
	"time"

	"github.com/vogo/vage/schema"
)

var _ Evaluator = (*ToolCallEval)(nil)

// ToolCallConfig configures the ToolCallEval evaluator.
type ToolCallConfig struct {
	// StrictArgs controls whether tool call arguments are compared.
	// If false, only tool names are compared.
	StrictArgs bool
}

// ToolCallEval verifies that the agent made the expected tool calls in order.
type ToolCallEval struct {
	strictArgs bool
}

// NewToolCallEval creates a new ToolCallEval.
// If cfg is nil, default configuration (name-only matching) is used.
func NewToolCallEval(cfg *ToolCallConfig) (*ToolCallEval, error) {
	strictArgs := false
	if cfg != nil {
		strictArgs = cfg.StrictArgs
	}

	return &ToolCallEval{strictArgs: strictArgs}, nil
}

// Evaluate implements Evaluator.
func (e *ToolCallEval) Evaluate(_ context.Context, c *EvalCase) (*EvalResult, error) {
	start := time.Now()

	if c.Actual == nil {
		return nil, errors.New("tool call eval requires a non-nil Actual response")
	}

	expectedCalls := extractToolCalls(c.Expected)

	if len(expectedCalls) == 0 {
		return &EvalResult{
			CaseID: c.ID,
			Score:  1.0,
			Passed: true,
			Details: []EvalDetail{
				{
					Name:    "tool_calls",
					Score:   1.0,
					Passed:  true,
					Message: "no expected tool calls",
				},
			},
			Duration: time.Since(start).Milliseconds(),
			Usage:    c.Actual.Usage,
		}, nil
	}

	actualCalls := extractToolCalls(c.Actual)

	matched := 0
	details := make([]EvalDetail, 0, len(expectedCalls))
	actualIdx := 0

	for _, expected := range expectedCalls {
		found := false

		for actualIdx < len(actualCalls) {
			actual := actualCalls[actualIdx]
			actualIdx++

			if actual.Function.Name == expected.Function.Name {
				if e.strictArgs && actual.Function.Arguments != expected.Function.Arguments {
					details = append(details, EvalDetail{
						Name:    expected.Function.Name,
						Score:   0,
						Passed:  false,
						Message: fmt.Sprintf("tool %q found but arguments differ: expected %q, got %q", expected.Function.Name, expected.Function.Arguments, actual.Function.Arguments),
					})

					found = true

					break
				}

				matched++

				details = append(details, EvalDetail{
					Name:    expected.Function.Name,
					Score:   1.0,
					Passed:  true,
					Message: fmt.Sprintf("tool %q matched", expected.Function.Name),
				})

				found = true

				break
			}
		}

		if !found {
			details = append(details, EvalDetail{
				Name:    expected.Function.Name,
				Score:   0,
				Passed:  false,
				Message: fmt.Sprintf("tool %q not found in actual calls", expected.Function.Name),
			})
		}
	}

	score := float64(matched) / float64(len(expectedCalls))
	passed := matched == len(expectedCalls)

	return &EvalResult{
		CaseID:   c.ID,
		Score:    score,
		Passed:   passed,
		Details:  details,
		Duration: time.Since(start).Milliseconds(),
		Usage:    c.Actual.Usage,
	}, nil
}

// extractToolCalls collects all tool calls from assistant messages in a RunResponse.
func extractToolCalls(resp *schema.RunResponse) []schema.ToolCall {
	if resp == nil {
		return nil
	}

	var calls []schema.ToolCall

	for _, msg := range resp.Messages {
		if msg.Role() == schema.RoleAssistant && len(msg.ToolCalls) > 0 {
			calls = append(calls, msg.ToolCalls...)
		}
	}

	return calls
}
