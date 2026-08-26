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
	"slices"

	"github.com/vogo/vage/schema"
)

// EvalCase represents a single evaluation test case.
type EvalCase struct {
	// ID is the unique case identifier.
	ID string
	// Input is the request sent to the agent.
	Input *schema.RunRequest
	// Expected is the expected output (optional, used for comparison).
	Expected *schema.RunResponse
	// Actual is the actual agent output (populated by caller before evaluation).
	Actual *schema.RunResponse
	// Criteria contains evaluation criteria or dimension names (used by LLMJudgeEval).
	Criteria []string
	// Tags are used for grouping and filtering.
	Tags []string
}

// EvalResult represents the outcome of evaluating a single case.
type EvalResult struct {
	// CaseID is the corresponding EvalCase.ID.
	CaseID string
	// Score is the overall score in range [0, 1].
	Score float64
	// Passed indicates whether the evaluation passed.
	Passed bool
	// Details contains per-dimension scoring details.
	Details []EvalDetail
	// Duration is the evaluation duration in milliseconds.
	Duration int64
	// Usage is the token usage (from actual response or judge call).
	Usage *schema.Usage
	// Error is the error message if evaluation encountered an error.
	Error string
}

// EvalDetail represents per-dimension evaluation detail.
type EvalDetail struct {
	// Name is the dimension or criterion name.
	Name string
	// Score is the score for this dimension in [0, 1].
	Score float64
	// Passed indicates whether this dimension passed.
	Passed bool
	// Message is a human-readable explanation or reason.
	Message string
}

// EvalReport summarizes the results of a batch evaluation.
type EvalReport struct {
	// Results contains individual results for each case.
	Results []*EvalResult
	// TotalCases is the total number of cases evaluated.
	TotalCases int
	// PassedCases is the number of cases that passed.
	PassedCases int
	// FailedCases is the number of cases that failed.
	FailedCases int
	// ErrorCases is the number of cases that encountered errors.
	ErrorCases int
	// AvgScore is the average score across all non-error cases.
	AvgScore float64
	// TotalDuration is the total wall-clock evaluation duration in milliseconds.
	TotalDuration int64
}

// Evaluator defines the contract for evaluating agent outputs.
type Evaluator interface {
	// Evaluate scores a single evaluation case.
	// Returns an error for infrastructure failures, not for evaluation failures
	// (those are captured in EvalResult.Passed).
	Evaluate(ctx context.Context, c *EvalCase) (*EvalResult, error)
}

// EvalFunc is a function adapter for Evaluator, similar to http.HandlerFunc.
type EvalFunc func(ctx context.Context, c *EvalCase) (*EvalResult, error)

// Evaluate implements Evaluator by calling the function itself.
func (f EvalFunc) Evaluate(ctx context.Context, c *EvalCase) (*EvalResult, error) {
	return f(ctx, c)
}

// lastAssistantText returns the text content of the last assistant message
// in the given RunResponse. Returns empty string if no assistant message is found.
func lastAssistantText(resp *schema.RunResponse) string {
	if resp == nil {
		return ""
	}

	for _, v := range slices.Backward(resp.Messages) {
		if v.Role() == schema.RoleAssistant {
			return v.Text()
		}
	}

	return ""
}

// clamp restricts v to the range [min, max].
func clamp(v, min, max float64) float64 {
	if v < min {
		return min
	}

	if v > max {
		return max
	}

	return v
}
