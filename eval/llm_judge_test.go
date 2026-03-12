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
	"testing"

	"github.com/vogo/vagent/schema"
)

func TestLLMJudgeEval_NilCompleter(t *testing.T) {
	_, err := NewLLMJudgeEval(nil, "model")
	if err == nil {
		t.Error("expected error for nil completer")
	}
}

func TestLLMJudgeEval_EmptyModel(t *testing.T) {
	_, err := NewLLMJudgeEval(&mockCompleter{}, "")
	if err == nil {
		t.Error("expected error for empty model")
	}
}

func TestLLMJudgeEval_NilActual(t *testing.T) {
	e, _ := NewLLMJudgeEval(&mockCompleter{}, "model")

	_, err := e.Evaluate(context.Background(), &EvalCase{ID: "nil"})
	if err == nil {
		t.Error("expected error for nil Actual")
	}
}

func TestLLMJudgeEval_StandardFormat(t *testing.T) {
	e, _ := NewLLMJudgeEval(&mockCompleter{
		response: "SCORE: 0.8\nPASSED: true\nREASONING: good output",
	}, "test-model")

	result, err := e.Evaluate(context.Background(), &EvalCase{
		ID:     "standard",
		Input:  &schema.RunRequest{Messages: []schema.Message{schema.NewUserMessage("test")}},
		Actual: makeResponse("output"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !almostEqual(result.Score, 0.8, 0.01) {
		t.Errorf("expected score 0.8, got %f", result.Score)
	}

	if !result.Passed {
		t.Error("expected Passed=true")
	}
}

func TestLLMJudgeEval_MarkdownFormat(t *testing.T) {
	e, _ := NewLLMJudgeEval(&mockCompleter{
		response: "**SCORE:** 0.9\n**PASSED:** true\n**REASONING:** great work",
	}, "test-model")

	result, err := e.Evaluate(context.Background(), &EvalCase{
		ID:     "markdown",
		Actual: makeResponse("output"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !almostEqual(result.Score, 0.9, 0.01) {
		t.Errorf("expected score 0.9, got %f", result.Score)
	}
}

func TestLLMJudgeEval_MissingScore(t *testing.T) {
	e, _ := NewLLMJudgeEval(&mockCompleter{
		response: "PASSED: true\nREASONING: no score",
	}, "test-model")

	result, err := e.Evaluate(context.Background(), &EvalCase{
		ID:     "missing-score",
		Actual: makeResponse("output"),
	})
	if err != nil {
		t.Fatalf("unexpected infrastructure error: %v", err)
	}

	if result.Passed {
		t.Error("expected Passed=false for parse error")
	}

	if result.Score != 0 {
		t.Errorf("expected score 0, got %f", result.Score)
	}
}

func TestLLMJudgeEval_CompleterError(t *testing.T) {
	e, _ := NewLLMJudgeEval(&mockCompleter{
		err: errors.New("api error"),
	}, "test-model")

	_, err := e.Evaluate(context.Background(), &EvalCase{
		ID:     "api-error",
		Actual: makeResponse("output"),
	})
	if err == nil {
		t.Error("expected error from completer failure")
	}
}

func TestLLMJudgeEval_WithExpectedAndCriteria(t *testing.T) {
	e, _ := NewLLMJudgeEval(&mockCompleter{
		response: "SCORE: 0.7\nPASSED: false\nREASONING: partial match",
	}, "test-model")

	result, err := e.Evaluate(context.Background(), &EvalCase{
		ID:       "with-expected",
		Input:    &schema.RunRequest{Messages: []schema.Message{schema.NewUserMessage("question")}},
		Expected: makeResponse("expected answer"),
		Actual:   makeResponse("actual answer"),
		Criteria: []string{"accuracy", "completeness"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !almostEqual(result.Score, 0.7, 0.01) {
		t.Errorf("expected score 0.7, got %f", result.Score)
	}
}

func TestLLMJudgeEval_MultiLineReasoning(t *testing.T) {
	e, _ := NewLLMJudgeEval(&mockCompleter{
		response: "SCORE: 0.6\nPASSED: true\nREASONING: first line\nsecond line\nthird line",
	}, "test-model")

	result, err := e.Evaluate(context.Background(), &EvalCase{
		ID:     "multiline",
		Actual: makeResponse("output"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !almostEqual(result.Score, 0.6, 0.01) {
		t.Errorf("expected score 0.6, got %f", result.Score)
	}

	if !result.Passed {
		t.Error("expected Passed=true")
	}
}

func TestParseJudgeResponse_MissingPassed(t *testing.T) {
	_, _, _, err := parseJudgeResponse("SCORE: 0.5\nREASONING: no passed")
	if err == nil {
		t.Error("expected error for missing PASSED")
	}
}

func TestParseJudgeResponse_InvalidScore(t *testing.T) {
	_, _, _, err := parseJudgeResponse("SCORE: abc\nPASSED: true\nREASONING: bad")
	if err == nil {
		t.Error("expected error for invalid score")
	}
}

func TestParseJudgeResponse_InvalidPassed(t *testing.T) {
	_, _, _, err := parseJudgeResponse("SCORE: 0.5\nPASSED: maybe\nREASONING: bad")
	if err == nil {
		t.Error("expected error for invalid passed value")
	}
}

func TestParseJudgeResponse_ScoreClamped(t *testing.T) {
	score, _, _, err := parseJudgeResponse("SCORE: 1.5\nPASSED: true\nREASONING: high")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if score != 1.0 {
		t.Errorf("expected clamped score 1.0, got %f", score)
	}
}

func TestNormalizeLabel(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"**SCORE:**", "SCORE:"},
		{"*SCORE:*", "SCORE:"},
		{"  SCORE:  ", "SCORE:"},
	}

	for _, tt := range tests {
		got := normalizeLabel(tt.input)
		if got != tt.want {
			t.Errorf("normalizeLabel(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestCutLabel(t *testing.T) {
	val, ok := cutLabel("SCORE: 0.8", "SCORE")
	if !ok || val != "0.8" {
		t.Errorf("cutLabel(SCORE: 0.8, SCORE) = %q, %v", val, ok)
	}

	val, ok = cutLabel("**Score:** 0.9", "SCORE")
	if !ok || val != "0.9" {
		t.Errorf("cutLabel(**Score:** 0.9, SCORE) = %q, %v", val, ok)
	}

	_, ok = cutLabel("OTHER: value", "SCORE")
	if ok {
		t.Error("expected ok=false for non-matching label")
	}
}
