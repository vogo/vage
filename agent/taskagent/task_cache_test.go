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
	"testing"

	"github.com/vogo/aimodel"
	"github.com/vogo/vage/agent"
	"github.com/vogo/vage/prompt"
	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/tool"
)

// TestMarkPromptCacheBreakpoints_SystemAndTools covers the happy path:
// the last system message and the last tool both get flagged; nothing
// else is touched.
func TestMarkPromptCacheBreakpoints_SystemAndTools(t *testing.T) {
	msgs := []aimodel.Message{
		{Role: aimodel.RoleSystem, Content: aimodel.NewTextContent("s1")},
		{Role: aimodel.RoleUser, Content: aimodel.NewTextContent("u1")},
	}
	tools := []aimodel.Tool{
		{Type: "function", Function: aimodel.FunctionDefinition{Name: "t1"}},
		{Type: "function", Function: aimodel.FunctionDefinition{Name: "t2"}},
	}
	markPromptCacheBreakpoints(msgs, tools)

	if !msgs[0].CacheBreakpoint {
		t.Errorf("system message not marked")
	}
	if msgs[1].CacheBreakpoint {
		t.Errorf("user message incorrectly marked")
	}
	if tools[0].CacheBreakpoint {
		t.Errorf("first tool incorrectly marked")
	}
	if !tools[1].CacheBreakpoint {
		t.Errorf("last tool not marked")
	}
}

// TestMarkPromptCacheBreakpoints_MultipleSystem verifies the helper marks
// only the LAST system message — matches Anthropic's "cache up to and
// including" semantics so one breakpoint at the tail covers all of them.
func TestMarkPromptCacheBreakpoints_MultipleSystem(t *testing.T) {
	msgs := []aimodel.Message{
		{Role: aimodel.RoleSystem, Content: aimodel.NewTextContent("s1")},
		{Role: aimodel.RoleSystem, Content: aimodel.NewTextContent("s2")},
		{Role: aimodel.RoleUser, Content: aimodel.NewTextContent("u1")},
	}
	markPromptCacheBreakpoints(msgs, nil)
	if msgs[0].CacheBreakpoint {
		t.Errorf("first system incorrectly marked")
	}
	if !msgs[1].CacheBreakpoint {
		t.Errorf("last system not marked")
	}
}

// TestMarkPromptCacheBreakpoints_NoSystem is a regression guard — with no
// system message at all the helper should still mark the tool cleanly.
func TestMarkPromptCacheBreakpoints_NoSystem(t *testing.T) {
	msgs := []aimodel.Message{
		{Role: aimodel.RoleUser, Content: aimodel.NewTextContent("u1")},
	}
	tools := []aimodel.Tool{
		{Type: "function", Function: aimodel.FunctionDefinition{Name: "t1"}},
	}
	markPromptCacheBreakpoints(msgs, tools)
	if msgs[0].CacheBreakpoint {
		t.Errorf("user message incorrectly marked")
	}
	if !tools[0].CacheBreakpoint {
		t.Errorf("tool not marked")
	}
}

// TestMarkPromptCacheBreakpoints_NoTools verifies the helper handles an
// empty tool slice without panicking and still marks the system msg.
func TestMarkPromptCacheBreakpoints_NoTools(t *testing.T) {
	msgs := []aimodel.Message{
		{Role: aimodel.RoleSystem, Content: aimodel.NewTextContent("s1")},
	}
	markPromptCacheBreakpoints(msgs, nil)
	if !msgs[0].CacheBreakpoint {
		t.Errorf("system not marked")
	}
}

// TestAgent_Run_PromptCachingDefault confirms that the default-on option
// plumbs the CacheBreakpoint flag through to the outbound ChatRequest.
func TestAgent_Run_PromptCachingDefault(t *testing.T) {
	mock := &mockChatCompleter{responses: []*aimodel.ChatResponse{stopResponse("ok")}}

	reg := tool.NewRegistry()
	_ = reg.Register(
		schema.ToolDef{Name: "t1"},
		func(_ context.Context, _, _ string) (schema.ToolResult, error) {
			return schema.TextResult("", ""), nil
		},
	)

	a := New(
		agent.Config{},
		WithChatCompleter(mock),
		WithToolRegistry(reg),
		WithSystemPrompt(prompt.StringPrompt("you are helpful")),
	)

	_, err := a.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("hi")},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(mock.requests) == 0 {
		t.Fatal("no LLM requests captured")
	}
	req := mock.requests[0]

	foundSystem := false
	for _, m := range req.Messages {
		if m.Role == aimodel.RoleSystem && m.CacheBreakpoint {
			foundSystem = true
		}
	}
	if !foundSystem {
		t.Errorf("expected a system message with CacheBreakpoint=true")
	}
	if len(req.Tools) == 0 {
		t.Fatal("expected at least one tool")
	}
	if !req.Tools[len(req.Tools)-1].CacheBreakpoint {
		t.Errorf("last tool CacheBreakpoint=false, want true")
	}
}

// TestAgent_Run_PromptCachingDisabled verifies that WithPromptCaching(false)
// sends neither the system-message nor the tool-array marker.
func TestAgent_Run_PromptCachingDisabled(t *testing.T) {
	mock := &mockChatCompleter{responses: []*aimodel.ChatResponse{stopResponse("ok")}}

	reg := tool.NewRegistry()
	_ = reg.Register(
		schema.ToolDef{Name: "t1"},
		func(_ context.Context, _, _ string) (schema.ToolResult, error) {
			return schema.TextResult("", ""), nil
		},
	)

	a := New(
		agent.Config{},
		WithChatCompleter(mock),
		WithToolRegistry(reg),
		WithSystemPrompt(prompt.StringPrompt("you are helpful")),
		WithPromptCaching(false),
	)

	_, err := a.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage("hi")},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	req := mock.requests[0]
	for i, m := range req.Messages {
		if m.CacheBreakpoint {
			t.Errorf("messages[%d] (role=%s) unexpectedly marked with CacheBreakpoint", i, m.Role)
		}
	}
	for i, tl := range req.Tools {
		if tl.CacheBreakpoint {
			t.Errorf("tools[%d] unexpectedly marked with CacheBreakpoint", i)
		}
	}
}

// TestNew_DefaultPromptCaching verifies the zero-arg constructor turns on
// prompt caching — operators opt out rather than opt in.
func TestNew_DefaultPromptCaching(t *testing.T) {
	a := New(agent.Config{})
	if !a.promptCaching {
		t.Errorf("promptCaching default = false, want true")
	}
}
