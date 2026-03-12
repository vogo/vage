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

	"github.com/vogo/aimodel"
)

func TestToolCallEval_NilConfig(t *testing.T) {
	e, err := NewToolCallEval(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if e.strictArgs {
		t.Error("expected strictArgs=false for nil config")
	}
}

func TestToolCallEval_NilActual(t *testing.T) {
	e, _ := NewToolCallEval(nil)

	_, err := e.Evaluate(context.Background(), &EvalCase{ID: "nil"})
	if err == nil {
		t.Error("expected error for nil Actual")
	}
}

func TestToolCallEval_NoExpected(t *testing.T) {
	e, _ := NewToolCallEval(nil)

	result, err := e.Evaluate(context.Background(), &EvalCase{
		ID:     "no-expected",
		Actual: makeResponseWithToolCalls(aimodel.ToolCall{Function: aimodel.FunctionCall{Name: "search"}}),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Score != 1.0 || !result.Passed {
		t.Errorf("expected score=1.0 passed=true, got score=%f passed=%v", result.Score, result.Passed)
	}
}

func TestToolCallEval_FullMatch(t *testing.T) {
	e, _ := NewToolCallEval(nil)

	search := aimodel.ToolCall{Function: aimodel.FunctionCall{Name: "search"}}
	calc := aimodel.ToolCall{Function: aimodel.FunctionCall{Name: "calculate"}}

	result, err := e.Evaluate(context.Background(), &EvalCase{
		ID:       "full",
		Expected: makeResponseWithToolCalls(search, calc),
		Actual:   makeResponseWithToolCalls(search, calc),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Score != 1.0 || !result.Passed {
		t.Errorf("expected score=1.0 passed=true, got score=%f passed=%v", result.Score, result.Passed)
	}
}

func TestToolCallEval_PartialMatch(t *testing.T) {
	e, _ := NewToolCallEval(nil)

	search := aimodel.ToolCall{Function: aimodel.FunctionCall{Name: "search"}}
	calc := aimodel.ToolCall{Function: aimodel.FunctionCall{Name: "calculate"}}

	result, err := e.Evaluate(context.Background(), &EvalCase{
		ID:       "partial",
		Expected: makeResponseWithToolCalls(search, calc),
		Actual:   makeResponseWithToolCalls(search),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Score != 0.5 {
		t.Errorf("expected score 0.5, got %f", result.Score)
	}

	if result.Passed {
		t.Error("expected Passed=false")
	}
}

func TestToolCallEval_StrictArgs_Match(t *testing.T) {
	e, _ := NewToolCallEval(&ToolCallConfig{StrictArgs: true})

	call := aimodel.ToolCall{Function: aimodel.FunctionCall{Name: "search", Arguments: `{"q":"hello"}`}}

	result, err := e.Evaluate(context.Background(), &EvalCase{
		ID:       "strict-match",
		Expected: makeResponseWithToolCalls(call),
		Actual:   makeResponseWithToolCalls(call),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Score != 1.0 || !result.Passed {
		t.Errorf("expected score=1.0 passed=true, got score=%f passed=%v", result.Score, result.Passed)
	}
}

func TestToolCallEval_StrictArgs_Mismatch(t *testing.T) {
	e, _ := NewToolCallEval(&ToolCallConfig{StrictArgs: true})

	expected := aimodel.ToolCall{Function: aimodel.FunctionCall{Name: "search", Arguments: `{"q":"hello"}`}}
	actual := aimodel.ToolCall{Function: aimodel.FunctionCall{Name: "search", Arguments: `{"q":"world"}`}}

	result, err := e.Evaluate(context.Background(), &EvalCase{
		ID:       "strict-mismatch",
		Expected: makeResponseWithToolCalls(expected),
		Actual:   makeResponseWithToolCalls(actual),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Score != 0.0 || result.Passed {
		t.Errorf("expected score=0.0 passed=false, got score=%f passed=%v", result.Score, result.Passed)
	}
}

func TestExtractToolCalls_Nil(t *testing.T) {
	calls := extractToolCalls(nil)
	if calls != nil {
		t.Errorf("expected nil for nil response, got %v", calls)
	}
}

func TestExtractToolCalls_NoToolCalls(t *testing.T) {
	resp := makeResponse("no tools")
	calls := extractToolCalls(resp)

	if len(calls) != 0 {
		t.Errorf("expected 0 tool calls, got %d", len(calls))
	}
}
