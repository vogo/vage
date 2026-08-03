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

package orchestrate

import (
	"context"
	"strings"
	"testing"

	"github.com/vogo/vage/schema"
)

func TestConditionalNode_Validate_ExhaustiveNoDefault(t *testing.T) {
	cn := &ConditionalNode{
		Node:       Node{ID: "cond1"},
		Exhaustive: true,
		Default:    "",
	}
	err := ValidateConditionalNode(cn)
	if err == nil {
		t.Fatal("expected error for Exhaustive=true without Default")
	}
	if !strings.Contains(err.Error(), "Exhaustive") {
		t.Errorf("error should mention Exhaustive: %v", err)
	}
}

func TestConditionalNode_Validate_ExhaustiveWithDefault(t *testing.T) {
	cn := &ConditionalNode{
		Node:       Node{ID: "cond1"},
		Exhaustive: true,
		Default:    "fallback",
		Branches: []Branch{
			{Condition: func(_ map[string]*schema.RunResponse) bool { return true }, TargetID: "target1"},
		},
	}
	err := ValidateConditionalNode(cn)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestConditionalNode_Validate_NilCondition(t *testing.T) {
	cn := &ConditionalNode{
		Node: Node{ID: "cond1"},
		Branches: []Branch{
			{Condition: nil, TargetID: "target1"},
		},
	}
	err := ValidateConditionalNode(cn)
	if err == nil {
		t.Fatal("expected error for nil Condition")
	}
	if !strings.Contains(err.Error(), "nil Condition") {
		t.Errorf("error should mention nil Condition: %v", err)
	}
}

func TestConditionalNode_Validate_EmptyTargetID(t *testing.T) {
	cn := &ConditionalNode{
		Node: Node{ID: "cond1"},
		Branches: []Branch{
			{Condition: func(_ map[string]*schema.RunResponse) bool { return true }, TargetID: ""},
		},
	}
	err := ValidateConditionalNode(cn)
	if err == nil {
		t.Fatal("expected error for empty TargetID")
	}
}

func TestConditionalNode_EvaluateBranches_FirstMatch(t *testing.T) {
	cn := &ConditionalNode{
		Node: Node{ID: "cond1"},
		Branches: []Branch{
			{Condition: func(_ map[string]*schema.RunResponse) bool { return false }, TargetID: "A"},
			{Condition: func(_ map[string]*schema.RunResponse) bool { return true }, TargetID: "B"},
			{Condition: func(_ map[string]*schema.RunResponse) bool { return true }, TargetID: "C"},
		},
		Default: "D",
	}
	target := cn.EvaluateBranches(nil)
	if target != "B" {
		t.Errorf("expected target B, got %q", target)
	}
}

func TestConditionalNode_EvaluateBranches_Default(t *testing.T) {
	cn := &ConditionalNode{
		Node: Node{ID: "cond1"},
		Branches: []Branch{
			{Condition: func(_ map[string]*schema.RunResponse) bool { return false }, TargetID: "A"},
		},
		Default: "D",
	}
	target := cn.EvaluateBranches(nil)
	if target != "D" {
		t.Errorf("expected target D, got %q", target)
	}
}

func TestConditionalNode_EvaluateBranches_NoMatchNoDefault(t *testing.T) {
	cn := &ConditionalNode{
		Node: Node{ID: "cond1"},
		Branches: []Branch{
			{Condition: func(_ map[string]*schema.RunResponse) bool { return false }, TargetID: "A"},
		},
	}
	target := cn.EvaluateBranches(nil)
	if target != "" {
		t.Errorf("expected empty target, got %q", target)
	}
}

func TestExecuteConditional_WithRunner(t *testing.T) {
	cn := &ConditionalNode{
		Node: Node{
			ID:     "cond1",
			Runner: appendRunner("-cond"),
		},
		Branches: []Branch{
			{
				Condition: func(up map[string]*schema.RunResponse) bool {
					return true
				},
				TargetID: "target-A",
			},
		},
	}

	resp, targetID, err := ExecuteConditional(context.Background(), cn, makeReq("start"), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	got := resp.Messages[0].Text()
	if got != "start-cond" {
		t.Errorf("got %q, want %q", got, "start-cond")
	}
	if targetID != "target-A" {
		t.Errorf("got target %q, want %q", targetID, "target-A")
	}
}

func TestExecuteConditional_NilRunner(t *testing.T) {
	cn := &ConditionalNode{
		Node: Node{ID: "cond1"},
		Branches: []Branch{
			{
				Condition: func(_ map[string]*schema.RunResponse) bool { return true },
				TargetID:  "target-A",
			},
		},
	}

	resp, targetID, err := ExecuteConditional(context.Background(), cn, makeReq("start"), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != nil {
		t.Error("expected nil response for nil runner")
	}
	if targetID != "target-A" {
		t.Errorf("got target %q, want %q", targetID, "target-A")
	}
}

func TestExecuteConditional_BranchBasedOnUpstream(t *testing.T) {
	cn := &ConditionalNode{
		Node: Node{ID: "cond1"},
		Branches: []Branch{
			{
				Condition: func(up map[string]*schema.RunResponse) bool {
					if up["A"] == nil {
						return false
					}
					return strings.Contains(up["A"].Messages[0].Text(), "yes")
				},
				TargetID: "happy-path",
			},
		},
		Default: "sad-path",
	}

	upstream := map[string]*schema.RunResponse{
		"A": {Messages: []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "yes please")}},
	}

	_, targetID, err := ExecuteConditional(context.Background(), cn, makeReq(""), upstream)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if targetID != "happy-path" {
		t.Errorf("got target %q, want %q", targetID, "happy-path")
	}

	// Test with "no" - should go to default.
	upstream["A"] = &schema.RunResponse{Messages: []schema.Message{schema.NewUserMessage(schema.ProtocolOpenAIChat, "no thanks")}}
	_, targetID, err = ExecuteConditional(context.Background(), cn, makeReq(""), upstream)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if targetID != "sad-path" {
		t.Errorf("got target %q, want %q", targetID, "sad-path")
	}
}
