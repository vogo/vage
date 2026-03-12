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

func TestCostEval_NilConfig(t *testing.T) {
	_, err := NewCostEval(nil)
	if err == nil {
		t.Error("expected error for nil config")
	}
}

func TestCostEval_ZeroBudget(t *testing.T) {
	_, err := NewCostEval(&CostConfig{Budget: 0})
	if err == nil {
		t.Error("expected error for zero budget")
	}
}

func TestCostEval_NilActual(t *testing.T) {
	e, _ := NewCostEval(&CostConfig{Budget: 100})

	_, err := e.Evaluate(context.Background(), &EvalCase{ID: "nil"})
	if err == nil {
		t.Error("expected error for nil Actual")
	}
}

func TestCostEval_UnderBudget(t *testing.T) {
	e, _ := NewCostEval(&CostConfig{Budget: 1000})

	result, err := e.Evaluate(context.Background(), &EvalCase{
		ID:     "under",
		Actual: makeResponseWithUsage("x", 500),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !almostEqual(result.Score, 0.75, 0.001) {
		t.Errorf("expected score ~0.75, got %f", result.Score)
	}

	if !result.Passed {
		t.Error("expected Passed=true")
	}
}

func TestCostEval_AtBudget(t *testing.T) {
	e, _ := NewCostEval(&CostConfig{Budget: 1000})

	result, err := e.Evaluate(context.Background(), &EvalCase{
		ID:     "at",
		Actual: makeResponseWithUsage("x", 1000),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !almostEqual(result.Score, 0.5, 0.001) {
		t.Errorf("expected score ~0.5, got %f", result.Score)
	}

	if !result.Passed {
		t.Error("expected Passed=true at budget")
	}
}

func TestCostEval_OverBudget(t *testing.T) {
	e, _ := NewCostEval(&CostConfig{Budget: 1000})

	result, err := e.Evaluate(context.Background(), &EvalCase{
		ID:     "over",
		Actual: makeResponseWithUsage("x", 2000),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Score != 0.0 {
		t.Errorf("expected score 0.0, got %f", result.Score)
	}

	if result.Passed {
		t.Error("expected Passed=false over budget")
	}
}

func TestCostEval_NilUsage_Default(t *testing.T) {
	e, _ := NewCostEval(&CostConfig{Budget: 1000})

	result, err := e.Evaluate(context.Background(), &EvalCase{
		ID:     "nil-usage",
		Actual: makeResponse("x"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Score != 1.0 || !result.Passed {
		t.Errorf("expected score=1.0 passed=true for nil usage, got score=%f passed=%v", result.Score, result.Passed)
	}
}

func TestCostEval_NilUsage_FailOnMissing(t *testing.T) {
	e, _ := NewCostEval(&CostConfig{Budget: 1000, FailOnMissingUsage: true})

	result, err := e.Evaluate(context.Background(), &EvalCase{
		ID:     "nil-usage-strict",
		Actual: makeResponse("x"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Score != 0 || result.Passed {
		t.Errorf("expected score=0 passed=false, got score=%f passed=%v", result.Score, result.Passed)
	}
}
