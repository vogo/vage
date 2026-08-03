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

package eval_tests

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/vogo/vage/eval"
	"github.com/vogo/vage/largemodel"
	"github.com/vogo/vage/schema"
)

// makeResponse creates a RunResponse with a single assistant message.
func makeResponse(text string) *schema.RunResponse {
	return &schema.RunResponse{
		Messages: []schema.Message{
			schema.NewTextMessage(schema.ProtocolOpenAIChat, schema.RoleAssistant, text),
		},
	}
}

// makeResponseWithDuration creates a RunResponse with a single assistant message and duration.
func makeResponseWithDuration(text string, durationMs int64) *schema.RunResponse {
	resp := makeResponse(text)
	resp.Duration = durationMs

	return resp
}

// makeResponseWithUsage creates a RunResponse with a single assistant message and usage.
func makeResponseWithUsage(text string, totalTokens int) *schema.RunResponse {
	resp := makeResponse(text)
	resp.Usage = &schema.Usage{TotalTokens: totalTokens}

	return resp
}

// makeResponseWithToolCalls creates a RunResponse with assistant tool call messages.
func makeResponseWithToolCalls(calls ...schema.ToolCall) *schema.RunResponse {
	return &schema.RunResponse{
		Messages: []schema.Message{
			schema.NewAssistantTurn(schema.ProtocolOpenAIChat, "", "", calls),
		},
	}
}

// almostEqual checks if two floats are within a tolerance.
func almostEqual(a, b, tolerance float64) bool {
	return math.Abs(a-b) < tolerance
}

// =============================================================================
// TC-1: ExactMatchEval
// =============================================================================

func TestIntegration_ExactMatch_PassAndFail(t *testing.T) {
	evaluator, err := eval.NewExactMatchEval()
	if err != nil {
		t.Fatalf("unexpected error creating ExactMatchEval: %v", err)
	}

	ctx := context.Background()

	// Matching case.
	c1 := &eval.EvalCase{
		ID:       "match-1",
		Expected: makeResponse("Hello world"),
		Actual:   makeResponse("Hello world"),
	}

	result, err := evaluator.Evaluate(ctx, c1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Score != 1.0 {
		t.Errorf("expected score 1.0, got %f", result.Score)
	}

	if !result.Passed {
		t.Error("expected Passed=true")
	}

	// Non-matching case.
	c2 := &eval.EvalCase{
		ID:       "match-2",
		Expected: makeResponse("Hello world"),
		Actual:   makeResponse("Goodbye world"),
	}

	result, err = evaluator.Evaluate(ctx, c2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Score != 0.0 {
		t.Errorf("expected score 0.0, got %f", result.Score)
	}

	if result.Passed {
		t.Error("expected Passed=false")
	}

	// Nil Expected case.
	c3 := &eval.EvalCase{
		ID:     "match-3",
		Actual: makeResponse("Hello"),
	}

	_, err = evaluator.Evaluate(ctx, c3)
	if err == nil {
		t.Error("expected error for nil Expected")
	}

	// Nil Actual case.
	c4 := &eval.EvalCase{
		ID:       "match-4",
		Expected: makeResponse("Hello"),
	}

	_, err = evaluator.Evaluate(ctx, c4)
	if err == nil {
		t.Error("expected error for nil Actual")
	}
}

// =============================================================================
// TC-2: ContainsEval - Full and Partial
// =============================================================================

func TestIntegration_Contains_PartialAndFull(t *testing.T) {
	evaluator, err := eval.NewContainsEval(&eval.ContainsConfig{
		Keywords: []string{"weather", "sunny", "today"},
	})
	if err != nil {
		t.Fatalf("unexpected error creating ContainsEval: %v", err)
	}

	ctx := context.Background()

	// Full match.
	c1 := &eval.EvalCase{
		ID:     "contains-1",
		Actual: makeResponse("The weather is sunny today"),
	}

	result, err := evaluator.Evaluate(ctx, c1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Score != 1.0 {
		t.Errorf("expected score 1.0, got %f", result.Score)
	}

	if !result.Passed {
		t.Error("expected Passed=true")
	}

	if len(result.Details) != 3 {
		t.Errorf("expected 3 details, got %d", len(result.Details))
	}

	// Partial match.
	c2 := &eval.EvalCase{
		ID:     "contains-2",
		Actual: makeResponse("The weather is cloudy"),
	}

	result, err = evaluator.Evaluate(ctx, c2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !almostEqual(result.Score, 1.0/3.0, 0.01) {
		t.Errorf("expected score ~0.333, got %f", result.Score)
	}

	if result.Passed {
		t.Error("expected Passed=false")
	}

	// Empty keywords.
	emptyEval, err := eval.NewContainsEval(&eval.ContainsConfig{})
	if err != nil {
		t.Fatalf("unexpected error creating empty ContainsEval: %v", err)
	}

	c3 := &eval.EvalCase{
		ID:     "contains-3",
		Actual: makeResponse("anything"),
	}

	result, err = emptyEval.Evaluate(ctx, c3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Score != 1.0 {
		t.Errorf("expected score 1.0, got %f", result.Score)
	}

	if !result.Passed {
		t.Error("expected Passed=true for empty keywords")
	}

	// Nil config should error.
	_, err = eval.NewContainsEval(nil)
	if err == nil {
		t.Error("expected error for nil config")
	}
}

// =============================================================================
// TC-3: ContainsEval - Custom Threshold
// =============================================================================

func TestIntegration_Contains_CustomThreshold(t *testing.T) {
	evaluator, err := eval.NewContainsEval(&eval.ContainsConfig{
		Keywords:      []string{"a", "b", "c", "d"},
		PassThreshold: 0.5,
	})
	if err != nil {
		t.Fatalf("unexpected error creating ContainsEval: %v", err)
	}

	ctx := context.Background()

	// 2 out of 4 keywords match => score=0.5, passes with threshold 0.5.
	c1 := &eval.EvalCase{
		ID:     "threshold-1",
		Actual: makeResponse("a b"),
	}

	result, err := evaluator.Evaluate(ctx, c1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Score != 0.5 {
		t.Errorf("expected score 0.5, got %f", result.Score)
	}

	if !result.Passed {
		t.Error("expected Passed=true with threshold 0.5 and score 0.5")
	}

	// 1 out of 4 keywords match => score=0.25, fails with threshold 0.5.
	c2 := &eval.EvalCase{
		ID:     "threshold-2",
		Actual: makeResponse("a"),
	}

	result, err = evaluator.Evaluate(ctx, c2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Score != 0.25 {
		t.Errorf("expected score 0.25, got %f", result.Score)
	}

	if result.Passed {
		t.Error("expected Passed=false with threshold 0.5 and score 0.25")
	}
}

// =============================================================================
// TC-4: ToolCallEval - Sequence Match
// =============================================================================

func TestIntegration_ToolCall_SequenceMatch(t *testing.T) {
	evaluator, err := eval.NewToolCallEval(nil)
	if err != nil {
		t.Fatalf("unexpected error creating ToolCallEval: %v", err)
	}

	ctx := context.Background()

	searchCall := schema.ToolCall{Name: "search"}
	calcCall := schema.ToolCall{Name: "calculate"}

	// Full match.
	c1 := &eval.EvalCase{
		ID:       "tc-1",
		Expected: makeResponseWithToolCalls(searchCall, calcCall),
		Actual:   makeResponseWithToolCalls(searchCall, calcCall),
	}

	result, err := evaluator.Evaluate(ctx, c1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Score != 1.0 {
		t.Errorf("expected score 1.0, got %f", result.Score)
	}

	if !result.Passed {
		t.Error("expected Passed=true")
	}

	// Partial match (actual only has search).
	c2 := &eval.EvalCase{
		ID:       "tc-2",
		Expected: makeResponseWithToolCalls(searchCall, calcCall),
		Actual:   makeResponseWithToolCalls(searchCall),
	}

	result, err = evaluator.Evaluate(ctx, c2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Score != 0.5 {
		t.Errorf("expected score 0.5, got %f", result.Score)
	}

	if result.Passed {
		t.Error("expected Passed=false")
	}

	// No expected tool calls.
	c3 := &eval.EvalCase{
		ID:     "tc-3",
		Actual: makeResponseWithToolCalls(searchCall),
	}

	result, err = evaluator.Evaluate(ctx, c3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Score != 1.0 {
		t.Errorf("expected score 1.0, got %f", result.Score)
	}

	if !result.Passed {
		t.Error("expected Passed=true")
	}
}

// =============================================================================
// TC-5: ToolCallEval - Strict Args
// =============================================================================

func TestIntegration_ToolCall_StrictArgs(t *testing.T) {
	evaluator, err := eval.NewToolCallEval(&eval.ToolCallConfig{StrictArgs: true})
	if err != nil {
		t.Fatalf("unexpected error creating ToolCallEval: %v", err)
	}

	ctx := context.Background()

	expectedCall := schema.ToolCall{
		Name:      "search",
		Arguments: `{"q":"hello"}`,
	}
	matchingCall := schema.ToolCall{
		Name:      "search",
		Arguments: `{"q":"hello"}`,
	}
	differentArgsCall := schema.ToolCall{
		Name:      "search",
		Arguments: `{"q":"world"}`,
	}

	// Same name, different arguments in strict mode.
	c1 := &eval.EvalCase{
		ID:       "strict-1",
		Expected: makeResponseWithToolCalls(expectedCall),
		Actual:   makeResponseWithToolCalls(differentArgsCall),
	}

	result, err := evaluator.Evaluate(ctx, c1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Score != 0.0 {
		t.Errorf("expected score 0.0, got %f", result.Score)
	}

	if result.Passed {
		t.Error("expected Passed=false for different args in strict mode")
	}

	// Same name, same arguments in strict mode.
	c2 := &eval.EvalCase{
		ID:       "strict-2",
		Expected: makeResponseWithToolCalls(expectedCall),
		Actual:   makeResponseWithToolCalls(matchingCall),
	}

	result, err = evaluator.Evaluate(ctx, c2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Score != 1.0 {
		t.Errorf("expected score 1.0, got %f", result.Score)
	}

	if !result.Passed {
		t.Error("expected Passed=true for matching args in strict mode")
	}
}

// =============================================================================
// TC-6: LatencyEval - Threshold Behavior
// =============================================================================

func TestIntegration_Latency_ThresholdBehavior(t *testing.T) {
	evaluator, err := eval.NewLatencyEval(200)
	if err != nil {
		t.Fatalf("unexpected error creating LatencyEval: %v", err)
	}

	ctx := context.Background()

	tests := []struct {
		name       string
		durationMs int64
		wantScore  float64
		wantPassed bool
	}{
		{"under threshold", 100, 0.75, true},
		{"at threshold", 200, 0.5, true},
		{"over threshold 2x", 400, 0.0, false},
		{"zero duration", 0, 1.0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &eval.EvalCase{
				ID:     "latency-" + tt.name,
				Actual: makeResponseWithDuration("response", tt.durationMs),
			}

			result, evalErr := evaluator.Evaluate(ctx, c)
			if evalErr != nil {
				t.Fatalf("unexpected error: %v", evalErr)
			}

			if !almostEqual(result.Score, tt.wantScore, 0.001) {
				t.Errorf("expected score %f, got %f", tt.wantScore, result.Score)
			}

			if result.Passed != tt.wantPassed {
				t.Errorf("expected Passed=%v, got %v", tt.wantPassed, result.Passed)
			}
		})
	}

	// Zero threshold should error.
	_, err = eval.NewLatencyEval(0)
	if err == nil {
		t.Error("expected error for zero threshold")
	}
}

// =============================================================================
// TC-7: CostEval - Budget Behavior
// =============================================================================

func TestIntegration_Cost_BudgetBehavior(t *testing.T) {
	evaluator, err := eval.NewCostEval(&eval.CostConfig{Budget: 1000})
	if err != nil {
		t.Fatalf("unexpected error creating CostEval: %v", err)
	}

	ctx := context.Background()

	tests := []struct {
		name       string
		tokens     int
		wantScore  float64
		wantPassed bool
	}{
		{"under budget", 500, 0.75, true},
		{"at budget", 1000, 0.5, true},
		{"over budget 2x", 2000, 0.0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &eval.EvalCase{
				ID:     "cost-" + tt.name,
				Actual: makeResponseWithUsage("response", tt.tokens),
			}

			result, evalErr := evaluator.Evaluate(ctx, c)
			if evalErr != nil {
				t.Fatalf("unexpected error: %v", evalErr)
			}

			if !almostEqual(result.Score, tt.wantScore, 0.001) {
				t.Errorf("expected score %f, got %f", tt.wantScore, result.Score)
			}

			if result.Passed != tt.wantPassed {
				t.Errorf("expected Passed=%v, got %v", tt.wantPassed, result.Passed)
			}
		})
	}

	// Nil usage (default: pass).
	c := &eval.EvalCase{
		ID:     "cost-nil-usage",
		Actual: makeResponse("no usage"),
	}

	result, err := evaluator.Evaluate(ctx, c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Score != 1.0 {
		t.Errorf("expected score 1.0 for nil usage, got %f", result.Score)
	}

	if !result.Passed {
		t.Error("expected Passed=true for nil usage")
	}

	// Nil usage with FailOnMissingUsage.
	strictEval, err := eval.NewCostEval(&eval.CostConfig{Budget: 1000, FailOnMissingUsage: true})
	if err != nil {
		t.Fatalf("unexpected error creating strict CostEval: %v", err)
	}

	result, err = strictEval.Evaluate(ctx, c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Passed {
		t.Error("expected Passed=false for nil usage with FailOnMissingUsage")
	}

	if result.Score != 0 {
		t.Errorf("expected score 0 for nil usage with FailOnMissingUsage, got %f", result.Score)
	}

	// Nil config should error.
	_, err = eval.NewCostEval(nil)
	if err == nil {
		t.Error("expected error for nil config")
	}

	// Zero budget should error.
	_, err = eval.NewCostEval(&eval.CostConfig{Budget: 0})
	if err == nil {
		t.Error("expected error for zero budget")
	}
}

// =============================================================================
// TC-8: CompositeEvaluator - Weighted Average
// =============================================================================

func TestIntegration_Composite_WeightedAverage(t *testing.T) {
	ctx := context.Background()

	// ExactMatch gives 1.0 (match), Contains gives 0.5 (1 of 2 keywords).
	c := &eval.EvalCase{
		ID:       "composite-1",
		Expected: makeResponse("Hello world"),
		Actual:   makeResponse("Hello world"),
	}

	exactEval, err := eval.NewExactMatchEval()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	containsEval, err := eval.NewContainsEval(&eval.ContainsConfig{
		Keywords: []string{"hello", "missing"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	composite, err := eval.NewCompositeEvaluator(
		nil,
		eval.WeightedEvaluator{Evaluator: exactEval, Weight: 1.0},
		eval.WeightedEvaluator{Evaluator: containsEval, Weight: 1.0},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	result, err := composite.Evaluate(ctx, c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !almostEqual(result.Score, 0.75, 0.01) {
		t.Errorf("expected score ~0.75, got %f", result.Score)
	}

	// Contains fails (Passed=false), so overall should be false.
	if result.Passed {
		t.Error("expected Passed=false because contains evaluator did not pass")
	}

	// All pass case.
	c2 := &eval.EvalCase{
		ID:       "composite-2",
		Expected: makeResponse("Hello world"),
		Actual:   makeResponse("Hello world"),
	}

	containsEval2, err := eval.NewContainsEval(&eval.ContainsConfig{
		Keywords: []string{"hello", "world"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	composite2, err := eval.NewCompositeEvaluator(
		nil,
		eval.WeightedEvaluator{Evaluator: exactEval, Weight: 1.0},
		eval.WeightedEvaluator{Evaluator: containsEval2, Weight: 1.0},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	result, err = composite2.Evaluate(ctx, c2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Score != 1.0 {
		t.Errorf("expected score 1.0, got %f", result.Score)
	}

	if !result.Passed {
		t.Error("expected Passed=true when all evaluators pass")
	}
}

// =============================================================================
// TC-9: CompositeEvaluator - Non-FailFast collects errors
// =============================================================================

func TestIntegration_Composite_NonFailFast(t *testing.T) {
	ctx := context.Background()

	// An evaluator that always errors.
	errorEval := eval.EvalFunc(func(_ context.Context, c *eval.EvalCase) (*eval.EvalResult, error) {
		return nil, errAlwaysFail
	})

	exactEval, err := eval.NewExactMatchEval()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Non-fail-fast: should continue past errors.
	composite, err := eval.NewCompositeEvaluator(
		nil,
		eval.WeightedEvaluator{Evaluator: errorEval, Weight: 1.0},
		eval.WeightedEvaluator{Evaluator: exactEval, Weight: 1.0},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	c := &eval.EvalCase{
		ID:       "composite-nff",
		Expected: makeResponse("test"),
		Actual:   makeResponse("test"),
	}

	result, err := composite.Evaluate(ctx, c)
	if err != nil {
		t.Fatalf("non-fail-fast should not return error, got: %v", err)
	}

	if result.Passed {
		t.Error("expected Passed=false due to error evaluator")
	}

	if result.Error == "" {
		t.Error("expected error message in result")
	}

	// Fail-fast: should return error immediately.
	compositeFf, err := eval.NewCompositeEvaluator(
		&eval.CompositeConfig{FailFast: true},
		eval.WeightedEvaluator{Evaluator: errorEval, Weight: 1.0},
		eval.WeightedEvaluator{Evaluator: exactEval, Weight: 1.0},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err = compositeFf.Evaluate(ctx, c)
	if err == nil {
		t.Error("fail-fast should return error")
	}
}

var errAlwaysFail = errorf("always fail")

type constantError string

func errorf(msg string) constantError { return constantError(msg) }
func (e constantError) Error() string { return string(e) }

// =============================================================================
// TC-10: BatchEval - Mixed Results
// =============================================================================

func TestIntegration_BatchEval_MixedResults(t *testing.T) {
	evaluator, err := eval.NewExactMatchEval()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ctx := context.Background()

	cases := []*eval.EvalCase{
		{
			ID:       "batch-pass",
			Expected: makeResponse("Hello"),
			Actual:   makeResponse("Hello"),
		},
		{
			ID:       "batch-fail",
			Expected: makeResponse("Hello"),
			Actual:   makeResponse("Goodbye"),
		},
		{
			ID:     "batch-error",
			Actual: makeResponse("Hello"),
			// Expected is nil, will error.
		},
	}

	report, err := eval.BatchEval(ctx, evaluator, cases)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if report.TotalCases != 3 {
		t.Errorf("expected TotalCases=3, got %d", report.TotalCases)
	}

	if report.PassedCases != 1 {
		t.Errorf("expected PassedCases=1, got %d", report.PassedCases)
	}

	if report.FailedCases != 1 {
		t.Errorf("expected FailedCases=1, got %d", report.FailedCases)
	}

	if report.ErrorCases != 1 {
		t.Errorf("expected ErrorCases=1, got %d", report.ErrorCases)
	}

	// AvgScore over non-error cases: (1.0 + 0.0) / 2 = 0.5.
	if !almostEqual(report.AvgScore, 0.5, 0.01) {
		t.Errorf("expected AvgScore ~0.5, got %f", report.AvgScore)
	}

	// Verify invariant.
	if report.PassedCases+report.FailedCases+report.ErrorCases != report.TotalCases {
		t.Error("invariant violated: passed + failed + error != total")
	}
}

// =============================================================================
// TC-11: BatchEval - Context Cancellation
// =============================================================================

func TestIntegration_BatchEval_ContextCancellation(t *testing.T) {
	evaluator, err := eval.NewExactMatchEval()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	cases := make([]*eval.EvalCase, 5)
	for i := range cases {
		cases[i] = &eval.EvalCase{
			ID:       "cancel-" + string(rune('0'+i)),
			Expected: makeResponse("test"),
			Actual:   makeResponse("test"),
		}
	}

	// Cancel before calling to ensure partial report.
	cancel()

	report, err := eval.BatchEval(ctx, evaluator, cases)
	if err == nil {
		t.Error("expected context cancellation error")
	}

	if len(report.Results) >= len(cases) {
		t.Errorf("expected partial results, got %d of %d", len(report.Results), len(cases))
	}
}

// =============================================================================
// TC-12: BatchEval - All Pass
// =============================================================================

func TestIntegration_BatchEval_AllPass(t *testing.T) {
	evaluator, err := eval.NewExactMatchEval()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ctx := context.Background()

	cases := make([]*eval.EvalCase, 5)
	for i := range cases {
		cases[i] = &eval.EvalCase{
			ID:       "allpass-" + string(rune('0'+i)),
			Expected: makeResponse("same"),
			Actual:   makeResponse("same"),
		}
	}

	report, err := eval.BatchEval(ctx, evaluator, cases)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if report.TotalCases != 5 {
		t.Errorf("expected TotalCases=5, got %d", report.TotalCases)
	}

	if report.PassedCases != 5 {
		t.Errorf("expected PassedCases=5, got %d", report.PassedCases)
	}

	if report.FailedCases != 0 {
		t.Errorf("expected FailedCases=0, got %d", report.FailedCases)
	}

	if report.ErrorCases != 0 {
		t.Errorf("expected ErrorCases=0, got %d", report.ErrorCases)
	}

	if report.AvgScore != 1.0 {
		t.Errorf("expected AvgScore=1.0, got %f", report.AvgScore)
	}
}

// =============================================================================
// TC-13: BatchEval - Concurrent
// =============================================================================

func TestIntegration_BatchEval_Concurrent(t *testing.T) {
	evaluator, err := eval.NewExactMatchEval()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ctx := context.Background()

	cases := make([]*eval.EvalCase, 10)
	for i := range cases {
		cases[i] = &eval.EvalCase{
			ID:       "concurrent-" + string(rune('0'+i)),
			Expected: makeResponse("same"),
			Actual:   makeResponse("same"),
		}
	}

	report, err := eval.BatchEval(ctx, evaluator, cases, eval.WithConcurrency(4))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if report.TotalCases != 10 {
		t.Errorf("expected TotalCases=10, got %d", report.TotalCases)
	}

	if report.PassedCases != 10 {
		t.Errorf("expected PassedCases=10, got %d", report.PassedCases)
	}

	if report.AvgScore != 1.0 {
		t.Errorf("expected AvgScore=1.0, got %f", report.AvgScore)
	}
}

// =============================================================================
// TC-14: EvalFunc adapter
// =============================================================================

func TestIntegration_EvalFunc(t *testing.T) {
	ctx := context.Background()

	customEval := eval.EvalFunc(func(_ context.Context, c *eval.EvalCase) (*eval.EvalResult, error) {
		return &eval.EvalResult{
			CaseID: c.ID,
			Score:  0.42,
			Passed: true,
			Details: []eval.EvalDetail{
				{Name: "custom", Score: 0.42, Passed: true, Message: "custom check"},
			},
		}, nil
	})

	c := &eval.EvalCase{
		ID:     "func-1",
		Actual: makeResponse("test"),
	}

	result, err := customEval.Evaluate(ctx, c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Score != 0.42 {
		t.Errorf("expected score 0.42, got %f", result.Score)
	}

	if !result.Passed {
		t.Error("expected Passed=true")
	}
}

// =============================================================================
// TC-15: RunAndEvaluate
// =============================================================================

func TestIntegration_RunAndEvaluate(t *testing.T) {
	ctx := context.Background()

	// Mock agent that echoes input.
	runFn := func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		text := ""
		if len(req.Messages) > 0 {
			text = req.Messages[0].Text()
		}

		return makeResponse(text), nil
	}

	evaluator, err := eval.NewExactMatchEval()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cases := []*eval.EvalCase{
		{
			ID:       "run-pass",
			Input:    &schema.RunRequest{Messages: []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "hello")}},
			Expected: makeResponse("hello"),
		},
		{
			ID:       "run-fail",
			Input:    &schema.RunRequest{Messages: []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "hello")}},
			Expected: makeResponse("world"),
		},
	}

	report, err := eval.RunAndEvaluate(ctx, runFn, evaluator, cases)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if report.TotalCases != 2 {
		t.Errorf("expected TotalCases=2, got %d", report.TotalCases)
	}

	if report.PassedCases != 1 {
		t.Errorf("expected PassedCases=1, got %d", report.PassedCases)
	}

	if report.FailedCases != 1 {
		t.Errorf("expected FailedCases=1, got %d", report.FailedCases)
	}
}

// =============================================================================
// TC-16: LLM Judge - Robust Parsing
// =============================================================================

func TestIntegration_LLMJudge_RobustParsing(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name      string
		response  string
		wantScore float64
		wantPass  bool
		wantErr   bool
	}{
		{
			name:      "standard format",
			response:  "SCORE: 0.8\nPASSED: true\nREASONING: good output",
			wantScore: 0.8,
			wantPass:  true,
		},
		{
			name:      "markdown bold",
			response:  "**SCORE:** 0.9\n**PASSED:** true\n**REASONING:** great work",
			wantScore: 0.9,
			wantPass:  true,
		},
		{
			name:      "lowercase labels",
			response:  "score: 0.7\npassed: false\nreasoning: needs work",
			wantScore: 0.7,
			wantPass:  false,
		},
		{
			name:      "mixed case",
			response:  "Score: 0.5\nPassed: true\nReasoning: acceptable",
			wantScore: 0.5,
			wantPass:  true,
		},
		{
			name: "multi-line reasoning",
			response: "SCORE: 0.6\nPASSED: true\nREASONING: first line\n" +
				"second line of reasoning\nthird line of reasoning",
			wantScore: 0.6,
			wantPass:  true,
		},
		{
			name:     "missing score",
			response: "PASSED: true\nREASONING: no score",
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			completer := &mockCompleter{response: tt.response}

			judge, err := eval.NewLLMJudgeEval(completer, "test-model")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			c := &eval.EvalCase{
				ID:     "judge-" + tt.name,
				Input:  &schema.RunRequest{Messages: []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "test")}},
				Actual: makeResponse("test output"),
			}

			result, err := judge.Evaluate(ctx, c)
			if err != nil {
				t.Fatalf("unexpected infrastructure error: %v", err)
			}

			if tt.wantErr {
				if result.Passed {
					t.Error("expected Passed=false for parse error")
				}

				return
			}

			if !almostEqual(result.Score, tt.wantScore, 0.01) {
				t.Errorf("expected score %f, got %f", tt.wantScore, result.Score)
			}

			if result.Passed != tt.wantPass {
				t.Errorf("expected Passed=%v, got %v", tt.wantPass, result.Passed)
			}
		})
	}
}

// mockCompleter is a scripted largemodel.Caller for testing.
type mockCompleter struct {
	response string
}

func (m *mockCompleter) Protocol() schema.Protocol { return schema.ProtocolOpenAIChat }

func (m *mockCompleter) Call(_ context.Context, _ *largemodel.Request) (*largemodel.Response, error) {
	return largemodel.FakeStopResponse(schema.ProtocolOpenAIChat, m.response, schema.Usage{}), nil
}

func (m *mockCompleter) CallStream(_ context.Context, _ *largemodel.Request) (*largemodel.Stream, error) {
	return nil, errors.New("not implemented")
}
