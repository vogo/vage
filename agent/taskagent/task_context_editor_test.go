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

package taskagent

import (
	"context"
	"strings"
	"testing"

	"github.com/vogo/aimodel"
	"github.com/vogo/vage/agent"
	"github.com/vogo/vage/largemodel"
	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/tool"
)

// TestAgent_WithContextEditor_FoldsOldToolResults exercises a four-turn
// ReAct loop: three tool calls then a stop. With keepLast=1 the second
// LLM request should already see the first tool_result placeholdered;
// the third request sees both first and second placeholdered.
func TestAgent_WithContextEditor_FoldsOldToolResults(t *testing.T) {
	mock := &mockChatCompleter{
		responses: []*aimodel.ChatResponse{
			toolCallResponse("tc-1", "echo", `{"v":"a"}`),
			toolCallResponse("tc-2", "echo", `{"v":"b"}`),
			toolCallResponse("tc-3", "echo", `{"v":"c"}`),
			stopResponse("done"),
		},
	}

	reg := tool.NewRegistry()
	_ = reg.Register(
		schema.ToolDef{Name: "echo"},
		func(_ context.Context, _, args string) (schema.ToolResult, error) {
			// Return content that is easy to spot in placeholdered form.
			return schema.TextResult("", "echo-result-"+args), nil
		},
	)

	editor := largemodel.NewContextEditorMiddleware(largemodel.WithKeepLastTools(1))

	a := New(
		agent.Config{},
		WithChatCompleter(mock),
		WithToolRegistry(reg),
		WithContextEditor(editor),
	)

	if _, err := a.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("hi")},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(mock.requests) != 4 {
		t.Fatalf("expected 4 LLM requests, got %d", len(mock.requests))
	}

	// Iteration 0 -> mock.requests[0]: no tool_results yet, nothing to elide.
	for _, m := range mock.requests[0].Messages {
		if m.Role == aimodel.RoleTool {
			t.Errorf("iter0 should not contain tool_result, found one: %+v", m)
		}
	}

	// Iteration 1 -> mock.requests[1]: one tool_result, kept verbatim.
	if got := countToolResults(mock.requests[1].Messages); got != 1 {
		t.Fatalf("iter1 expected 1 tool_result, got %d", got)
	}
	if elided := countElided(mock.requests[1].Messages); elided != 0 {
		t.Errorf("iter1 expected 0 elided, got %d", elided)
	}

	// Iteration 2 -> mock.requests[2]: two tool_results; the older one
	// elided, the newer one verbatim.
	if got := countToolResults(mock.requests[2].Messages); got != 2 {
		t.Fatalf("iter2 expected 2 tool_results, got %d", got)
	}
	if elided := countElided(mock.requests[2].Messages); elided != 1 {
		t.Errorf("iter2 expected 1 elided, got %d", elided)
	}

	// Iteration 3 -> mock.requests[3]: three tool_results; two older
	// elided, the latest verbatim.
	if got := countToolResults(mock.requests[3].Messages); got != 3 {
		t.Fatalf("iter3 expected 3 tool_results, got %d", got)
	}
	if elided := countElided(mock.requests[3].Messages); elided != 2 {
		t.Errorf("iter3 expected 2 elided, got %d", elided)
	}

	// All elided messages must keep their tool_call_id.
	for _, req := range mock.requests {
		for _, m := range req.Messages {
			if m.Role == aimodel.RoleTool && m.ToolCallID == "" {
				t.Errorf("tool_result lost tool_call_id: %+v", m)
			}
		}
	}
}

// TestAgent_WithoutContextEditor_NoChange is a zero-overhead regression
// guard: the same scenario without the option should keep every
// tool_result verbatim across iterations.
func TestAgent_WithoutContextEditor_NoChange(t *testing.T) {
	mock := &mockChatCompleter{
		responses: []*aimodel.ChatResponse{
			toolCallResponse("tc-1", "echo", `{"v":"a"}`),
			toolCallResponse("tc-2", "echo", `{"v":"b"}`),
			stopResponse("done"),
		},
	}

	reg := tool.NewRegistry()
	_ = reg.Register(
		schema.ToolDef{Name: "echo"},
		func(_ context.Context, _, args string) (schema.ToolResult, error) {
			return schema.TextResult("", "echo-result-"+args), nil
		},
	)

	a := New(
		agent.Config{},
		WithChatCompleter(mock),
		WithToolRegistry(reg),
	)

	if _, err := a.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("hi")},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Last request has 2 tool_results; none should be elided.
	last := mock.requests[len(mock.requests)-1]
	if elided := countElided(last.Messages); elided != 0 {
		t.Errorf("expected 0 elided without WithContextEditor, got %d", elided)
	}
}

// TestAgent_WithContextEditor_NilOption keeps the chain untouched —
// guards against accidentally wrapping ChatCompleter with a nil mw.
func TestAgent_WithContextEditor_NilOption(t *testing.T) {
	mock := &mockChatCompleter{responses: []*aimodel.ChatResponse{stopResponse("ok")}}

	a := New(
		agent.Config{},
		WithChatCompleter(mock),
		WithContextEditor(nil),
	)

	if _, err := a.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("hi")},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func countToolResults(msgs []aimodel.Message) int {
	n := 0
	for _, m := range msgs {
		if m.Role == aimodel.RoleTool {
			n++
		}
	}
	return n
}

func countElided(msgs []aimodel.Message) int {
	n := 0
	for _, m := range msgs {
		if m.Role == aimodel.RoleTool && strings.Contains(m.Content.Text(), "context_edited") {
			n++
		}
	}
	return n
}
