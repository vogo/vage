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

func TestBatchEval_Sequential(t *testing.T) {
	e, _ := NewExactMatchEval()

	cases := []*EvalCase{
		{ID: "b1", Expected: makeResponse("a"), Actual: makeResponse("a")},
		{ID: "b2", Expected: makeResponse("a"), Actual: makeResponse("b")},
	}

	report, err := BatchEval(context.Background(), e, cases)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if report.TotalCases != 2 {
		t.Errorf("expected TotalCases=2, got %d", report.TotalCases)
	}

	if report.PassedCases != 1 || report.FailedCases != 1 {
		t.Errorf("expected 1 pass 1 fail, got %d pass %d fail", report.PassedCases, report.FailedCases)
	}

	if !almostEqual(report.AvgScore, 0.5, 0.01) {
		t.Errorf("expected AvgScore ~0.5, got %f", report.AvgScore)
	}
}

func TestBatchEval_Concurrent(t *testing.T) {
	e, _ := NewExactMatchEval()

	cases := make([]*EvalCase, 10)
	for i := range cases {
		cases[i] = &EvalCase{
			ID:       "c" + string(rune('0'+i)),
			Expected: makeResponse("same"),
			Actual:   makeResponse("same"),
		}
	}

	report, err := BatchEval(context.Background(), e, cases, WithConcurrency(4))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if report.TotalCases != 10 || report.PassedCases != 10 {
		t.Errorf("expected 10 total 10 passed, got %d total %d passed", report.TotalCases, report.PassedCases)
	}
}

func TestBatchEval_ContextCancelled(t *testing.T) {
	e, _ := NewExactMatchEval()

	cases := make([]*EvalCase, 5)
	for i := range cases {
		cases[i] = &EvalCase{
			ID:       "cancel" + string(rune('0'+i)),
			Expected: makeResponse("x"),
			Actual:   makeResponse("x"),
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	report, err := BatchEval(ctx, e, cases)
	if err == nil {
		t.Error("expected context cancellation error")
	}

	if len(report.Results) >= len(cases) {
		t.Errorf("expected partial results, got %d", len(report.Results))
	}
}

func TestBatchEval_WithErrors(t *testing.T) {
	e, _ := NewExactMatchEval()

	cases := []*EvalCase{
		{ID: "pass", Expected: makeResponse("a"), Actual: makeResponse("a")},
		{ID: "error", Actual: makeResponse("a")}, // no Expected -> error.
	}

	report, err := BatchEval(context.Background(), e, cases)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if report.ErrorCases != 1 {
		t.Errorf("expected 1 error case, got %d", report.ErrorCases)
	}

	if report.PassedCases != 1 {
		t.Errorf("expected 1 passed case, got %d", report.PassedCases)
	}
}

func TestWithConcurrency(t *testing.T) {
	cfg := &batchConfig{concurrency: 1}
	WithConcurrency(4)(cfg)

	if cfg.concurrency != 4 {
		t.Errorf("expected concurrency 4, got %d", cfg.concurrency)
	}

	// Zero or negative should not change.
	WithConcurrency(0)(cfg)

	if cfg.concurrency != 4 {
		t.Errorf("expected concurrency to remain 4, got %d", cfg.concurrency)
	}
}

func TestComputeAvgScore(t *testing.T) {
	report := &EvalReport{
		Results: []*EvalResult{
			{Score: 1.0},
			{Score: 0.5},
			{Score: 0, Error: "some error"}, // Should be excluded.
		},
	}

	computeAvgScore(report)

	if !almostEqual(report.AvgScore, 0.75, 0.01) {
		t.Errorf("expected AvgScore ~0.75, got %f", report.AvgScore)
	}

	// Empty report.
	emptyReport := &EvalReport{}
	computeAvgScore(emptyReport)

	if emptyReport.AvgScore != 0 {
		t.Errorf("expected AvgScore 0 for empty report, got %f", emptyReport.AvgScore)
	}
}
