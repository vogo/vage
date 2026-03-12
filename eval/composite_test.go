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
	"testing"
)

func TestCompositeEvaluator_Empty(t *testing.T) {
	e, _ := NewCompositeEvaluator(nil)

	result, err := e.Evaluate(context.Background(), &EvalCase{ID: "empty"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Score != 1.0 || !result.Passed {
		t.Errorf("expected score=1.0 passed=true for empty composite, got score=%f passed=%v", result.Score, result.Passed)
	}
}

func TestCompositeEvaluator_WeightedAverage(t *testing.T) {
	exactEval, _ := NewExactMatchEval()
	containsEval, _ := NewContainsEval(&ContainsConfig{Keywords: []string{"hello", "missing"}})

	e, _ := NewCompositeEvaluator(nil,
		WeightedEvaluator{Evaluator: exactEval, Weight: 1.0},
		WeightedEvaluator{Evaluator: containsEval, Weight: 1.0},
	)

	result, err := e.Evaluate(context.Background(), &EvalCase{
		ID:       "weighted",
		Expected: makeResponse("Hello world"),
		Actual:   makeResponse("Hello world"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// ExactMatch=1.0, Contains=0.5 (1/2 keywords), equal weight -> (1.0+0.5)/2 = 0.75.
	if !almostEqual(result.Score, 0.75, 0.01) {
		t.Errorf("expected score ~0.75, got %f", result.Score)
	}

	if result.Passed {
		t.Error("expected Passed=false because contains evaluator did not pass")
	}
}

func TestCompositeEvaluator_ZeroWeights(t *testing.T) {
	exactEval, _ := NewExactMatchEval()

	e, _ := NewCompositeEvaluator(nil,
		WeightedEvaluator{Evaluator: exactEval, Weight: 0},
		WeightedEvaluator{Evaluator: exactEval, Weight: 0},
	)

	result, err := e.Evaluate(context.Background(), &EvalCase{
		ID:       "zero-weights",
		Expected: makeResponse("test"),
		Actual:   makeResponse("test"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Score != 1.0 {
		t.Errorf("expected score 1.0 with equal weighting, got %f", result.Score)
	}
}

func TestCompositeEvaluator_FailFast(t *testing.T) {
	errorEval := EvalFunc(func(_ context.Context, _ *EvalCase) (*EvalResult, error) {
		return nil, errAlwaysFail
	})

	exactEval, _ := NewExactMatchEval()

	e, _ := NewCompositeEvaluator(&CompositeConfig{FailFast: true},
		WeightedEvaluator{Evaluator: errorEval, Weight: 1.0},
		WeightedEvaluator{Evaluator: exactEval, Weight: 1.0},
	)

	_, err := e.Evaluate(context.Background(), &EvalCase{
		ID:       "failfast",
		Expected: makeResponse("x"),
		Actual:   makeResponse("x"),
	})
	if err == nil {
		t.Error("expected error in fail-fast mode")
	}
}

func TestCompositeEvaluator_NonFailFast(t *testing.T) {
	errorEval := EvalFunc(func(_ context.Context, _ *EvalCase) (*EvalResult, error) {
		return nil, errAlwaysFail
	})

	exactEval, _ := NewExactMatchEval()

	e, _ := NewCompositeEvaluator(nil,
		WeightedEvaluator{Evaluator: errorEval, Weight: 1.0},
		WeightedEvaluator{Evaluator: exactEval, Weight: 1.0},
	)

	result, err := e.Evaluate(context.Background(), &EvalCase{
		ID:       "nonfailfast",
		Expected: makeResponse("x"),
		Actual:   makeResponse("x"),
	})
	if err != nil {
		t.Fatalf("non-fail-fast should not return error, got: %v", err)
	}

	if result.Passed {
		t.Error("expected Passed=false due to error evaluator")
	}

	if result.Error == "" {
		t.Error("expected error message in result")
	}
}
