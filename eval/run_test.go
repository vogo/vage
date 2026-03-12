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

func TestRunAndEvaluate(t *testing.T) {
	runFn := func(_ context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		text := ""
		if len(req.Messages) > 0 {
			text = req.Messages[0].Content.Text()
		}

		return makeResponse(text), nil
	}

	e, _ := NewExactMatchEval()

	cases := []*EvalCase{
		{
			ID:       "run-pass",
			Input:    &schema.RunRequest{Messages: []schema.Message{schema.NewUserMessage("hello")}},
			Expected: makeResponse("hello"),
		},
		{
			ID:       "run-fail",
			Input:    &schema.RunRequest{Messages: []schema.Message{schema.NewUserMessage("hello")}},
			Expected: makeResponse("world"),
		},
	}

	report, err := RunAndEvaluate(context.Background(), runFn, e, cases)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if report.PassedCases != 1 || report.FailedCases != 1 {
		t.Errorf("expected 1 pass 1 fail, got %d pass %d fail", report.PassedCases, report.FailedCases)
	}
}

func TestRunAndEvaluate_SkipsPrefilledActual(t *testing.T) {
	called := 0
	runFn := func(_ context.Context, _ *schema.RunRequest) (*schema.RunResponse, error) {
		called++

		return makeResponse("result"), nil
	}

	e, _ := NewExactMatchEval()

	cases := []*EvalCase{
		{
			ID:       "prefilled",
			Input:    &schema.RunRequest{Messages: []schema.Message{schema.NewUserMessage("test")}},
			Expected: makeResponse("existing"),
			Actual:   makeResponse("existing"), // Already filled.
		},
	}

	report, err := RunAndEvaluate(context.Background(), runFn, e, cases)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if called != 0 {
		t.Errorf("expected runFn not to be called for prefilled case, but called %d times", called)
	}

	if report.PassedCases != 1 {
		t.Errorf("expected 1 passed, got %d", report.PassedCases)
	}
}

func TestRunAndEvaluate_SkipsNilInput(t *testing.T) {
	called := 0
	runFn := func(_ context.Context, _ *schema.RunRequest) (*schema.RunResponse, error) {
		called++

		return makeResponse("result"), nil
	}

	e, _ := NewExactMatchEval()

	cases := []*EvalCase{
		{ID: "nil-input"}, // No Input, no Actual.
	}

	_, err := RunAndEvaluate(context.Background(), runFn, e, cases)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if called != 0 {
		t.Errorf("expected runFn not to be called for nil input, but called %d times", called)
	}
}

func TestRunAndEvaluate_AgentError(t *testing.T) {
	runFn := func(_ context.Context, _ *schema.RunRequest) (*schema.RunResponse, error) {
		return nil, errors.New("agent failure")
	}

	e, _ := NewExactMatchEval()

	cases := []*EvalCase{
		{
			ID:    "error",
			Input: &schema.RunRequest{Messages: []schema.Message{schema.NewUserMessage("test")}},
		},
	}

	_, err := RunAndEvaluate(context.Background(), runFn, e, cases)
	if err == nil {
		t.Error("expected error from agent failure")
	}
}

func TestRunAndEvaluate_ContextCancelled(t *testing.T) {
	runFn := func(_ context.Context, _ *schema.RunRequest) (*schema.RunResponse, error) {
		return makeResponse("result"), nil
	}

	e, _ := NewExactMatchEval()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	cases := []*EvalCase{
		{
			ID:    "cancel",
			Input: &schema.RunRequest{Messages: []schema.Message{schema.NewUserMessage("test")}},
		},
	}

	_, err := RunAndEvaluate(ctx, runFn, e, cases)
	if err == nil {
		t.Error("expected error from context cancellation")
	}
}
