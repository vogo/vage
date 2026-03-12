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

func TestContainsEval_NilConfig(t *testing.T) {
	_, err := NewContainsEval(nil)
	if err == nil {
		t.Error("expected error for nil config")
	}
}

func TestContainsEval_EmptyKeywords(t *testing.T) {
	e, err := NewContainsEval(&ContainsConfig{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	result, err := e.Evaluate(context.Background(), &EvalCase{ID: "empty", Actual: makeResponse("anything")})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Score != 1.0 || !result.Passed {
		t.Errorf("expected score=1.0 passed=true, got score=%f passed=%v", result.Score, result.Passed)
	}
}

func TestContainsEval_FullMatch(t *testing.T) {
	e, _ := NewContainsEval(&ContainsConfig{Keywords: []string{"hello", "world"}})

	result, err := e.Evaluate(context.Background(), &EvalCase{
		ID:     "full",
		Actual: makeResponse("hello world"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Score != 1.0 || !result.Passed {
		t.Errorf("expected score=1.0 passed=true, got score=%f passed=%v", result.Score, result.Passed)
	}

	if len(result.Details) != 2 {
		t.Errorf("expected 2 details, got %d", len(result.Details))
	}
}

func TestContainsEval_PartialMatch(t *testing.T) {
	e, _ := NewContainsEval(&ContainsConfig{Keywords: []string{"hello", "missing"}})

	result, err := e.Evaluate(context.Background(), &EvalCase{
		ID:     "partial",
		Actual: makeResponse("hello world"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Score != 0.5 {
		t.Errorf("expected score 0.5, got %f", result.Score)
	}

	if result.Passed {
		t.Error("expected Passed=false with default threshold 1.0")
	}
}

func TestContainsEval_CaseInsensitive(t *testing.T) {
	e, _ := NewContainsEval(&ContainsConfig{Keywords: []string{"HELLO"}})

	result, err := e.Evaluate(context.Background(), &EvalCase{
		ID:     "case",
		Actual: makeResponse("hello world"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Score != 1.0 || !result.Passed {
		t.Errorf("expected case-insensitive match: score=%f passed=%v", result.Score, result.Passed)
	}
}

func TestContainsEval_CustomThreshold(t *testing.T) {
	e, _ := NewContainsEval(&ContainsConfig{
		Keywords:      []string{"a", "b", "c", "d"},
		PassThreshold: 0.5,
	})

	// 2/4 = 0.5 >= 0.5 threshold -> pass.
	result, err := e.Evaluate(context.Background(), &EvalCase{
		ID:     "threshold",
		Actual: makeResponse("a b"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Score != 0.5 || !result.Passed {
		t.Errorf("expected score=0.5 passed=true, got score=%f passed=%v", result.Score, result.Passed)
	}
}

func TestContainsEval_NilActual(t *testing.T) {
	e, _ := NewContainsEval(&ContainsConfig{Keywords: []string{"x"}})

	_, err := e.Evaluate(context.Background(), &EvalCase{ID: "nil"})
	if err == nil {
		t.Error("expected error for nil Actual")
	}
}
