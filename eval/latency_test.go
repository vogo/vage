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

func TestLatencyEval_InvalidThreshold(t *testing.T) {
	_, err := NewLatencyEval(0)
	if err == nil {
		t.Error("expected error for zero threshold")
	}

	_, err = NewLatencyEval(-1)
	if err == nil {
		t.Error("expected error for negative threshold")
	}
}

func TestLatencyEval_NilActual(t *testing.T) {
	e, _ := NewLatencyEval(100)

	_, err := e.Evaluate(context.Background(), &EvalCase{ID: "nil"})
	if err == nil {
		t.Error("expected error for nil Actual")
	}
}

func TestLatencyEval_Scoring(t *testing.T) {
	e, _ := NewLatencyEval(200)

	tests := []struct {
		name       string
		durationMs int64
		wantScore  float64
		wantPassed bool
	}{
		{"zero", 0, 1.0, true},
		{"half", 100, 0.75, true},
		{"at_threshold", 200, 0.5, true},
		{"over", 300, 0.25, false},
		{"double", 400, 0.0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := e.Evaluate(context.Background(), &EvalCase{
				ID:     tt.name,
				Actual: makeResponseWithDuration("x", tt.durationMs),
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if !almostEqual(result.Score, tt.wantScore, 0.001) {
				t.Errorf("expected score %f, got %f", tt.wantScore, result.Score)
			}

			if result.Passed != tt.wantPassed {
				t.Errorf("expected Passed=%v, got %v", tt.wantPassed, result.Passed)
			}
		})
	}
}
