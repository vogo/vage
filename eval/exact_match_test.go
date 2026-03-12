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

func TestExactMatchEval_Pass(t *testing.T) {
	e, err := NewExactMatchEval()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	result, err := e.Evaluate(context.Background(), &EvalCase{
		ID:       "pass",
		Expected: makeResponse("hello"),
		Actual:   makeResponse("hello"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Score != 1.0 || !result.Passed {
		t.Errorf("expected score=1.0 passed=true, got score=%f passed=%v", result.Score, result.Passed)
	}

	if len(result.Details) != 1 || result.Details[0].Name != "exact_match" {
		t.Errorf("unexpected details: %+v", result.Details)
	}
}

func TestExactMatchEval_Fail(t *testing.T) {
	e, _ := NewExactMatchEval()

	result, err := e.Evaluate(context.Background(), &EvalCase{
		ID:       "fail",
		Expected: makeResponse("hello"),
		Actual:   makeResponse("world"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Score != 0.0 || result.Passed {
		t.Errorf("expected score=0.0 passed=false, got score=%f passed=%v", result.Score, result.Passed)
	}
}

func TestExactMatchEval_NilExpected(t *testing.T) {
	e, _ := NewExactMatchEval()

	_, err := e.Evaluate(context.Background(), &EvalCase{ID: "nil-expected", Actual: makeResponse("x")})
	if err == nil {
		t.Error("expected error for nil Expected")
	}
}

func TestExactMatchEval_NilActual(t *testing.T) {
	e, _ := NewExactMatchEval()

	_, err := e.Evaluate(context.Background(), &EvalCase{ID: "nil-actual", Expected: makeResponse("x")})
	if err == nil {
		t.Error("expected error for nil Actual")
	}
}
