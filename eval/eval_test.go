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
	"github.com/vogo/vagent/schema"
)

func TestClamp(t *testing.T) {
	tests := []struct {
		v, min, max, want float64
	}{
		{0.5, 0, 1, 0.5},
		{-1, 0, 1, 0},
		{2, 0, 1, 1},
		{0, 0, 1, 0},
		{1, 0, 1, 1},
	}

	for _, tt := range tests {
		got := clamp(tt.v, tt.min, tt.max)
		if got != tt.want {
			t.Errorf("clamp(%f, %f, %f) = %f, want %f", tt.v, tt.min, tt.max, got, tt.want)
		}
	}
}

func TestLastAssistantText(t *testing.T) {
	// nil response.
	if got := lastAssistantText(nil); got != "" {
		t.Errorf("expected empty string for nil response, got %q", got)
	}

	// No assistant message.
	resp := &schema.RunResponse{
		Messages: []schema.Message{
			{Message: aimodel.Message{Role: aimodel.RoleUser, Content: aimodel.NewTextContent("user")}},
		},
	}

	if got := lastAssistantText(resp); got != "" {
		t.Errorf("expected empty string for no assistant message, got %q", got)
	}

	// Multiple assistant messages - should return last.
	resp = &schema.RunResponse{
		Messages: []schema.Message{
			{Message: aimodel.Message{Role: aimodel.RoleAssistant, Content: aimodel.NewTextContent("first")}},
			{Message: aimodel.Message{Role: aimodel.RoleUser, Content: aimodel.NewTextContent("user")}},
			{Message: aimodel.Message{Role: aimodel.RoleAssistant, Content: aimodel.NewTextContent("last")}},
		},
	}

	if got := lastAssistantText(resp); got != "last" {
		t.Errorf("expected %q, got %q", "last", got)
	}
}

func TestEvalFunc(t *testing.T) {
	ctx := context.Background()

	f := EvalFunc(func(_ context.Context, c *EvalCase) (*EvalResult, error) {
		return &EvalResult{CaseID: c.ID, Score: 0.5, Passed: true}, nil
	})

	result, err := f.Evaluate(ctx, &EvalCase{ID: "test"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Score != 0.5 {
		t.Errorf("expected score 0.5, got %f", result.Score)
	}

	if result.CaseID != "test" {
		t.Errorf("expected CaseID %q, got %q", "test", result.CaseID)
	}
}
