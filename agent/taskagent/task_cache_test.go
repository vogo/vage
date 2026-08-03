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

	"github.com/vogo/vage/agent"
	"github.com/vogo/vage/largemodel"
	"github.com/vogo/vage/prompt"
	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/tool"
)

// Prompt caching is a vendor-specific wire concern, so the agent no longer
// marks individual messages and tools. It states the intent on the request
// and the Anthropic caller renders the cache_control breakpoints; OpenAI
// ignores the flag because it caches identical prefixes on its own. These
// tests cover the agent's half of that contract — that the intent reaches
// the request — while largemodel's tests cover the wire rendering.

// cachingTestAgent builds an agent with one tool and a system prompt, the two
// surfaces prompt caching applies to.
func cachingTestAgent(t *testing.T, mock *mockCaller, opts ...Option) *Agent {
	t.Helper()

	reg := tool.NewRegistry()
	if err := reg.Register(
		schema.ToolDef{Name: "t1"},
		func(_ context.Context, _, _ string) (schema.ToolResult, error) {
			return schema.TextResult("", ""), nil
		},
	); err != nil {
		t.Fatalf("register tool: %v", err)
	}

	base := []Option{
		WithCaller(mock),
		WithToolRegistry(reg),
		WithSystemPrompt(prompt.StringPrompt("you are helpful")),
	}

	return New(agent.Config{}, append(base, opts...)...)
}

// runCachingAgent runs one turn and returns the request the agent produced.
func runCachingAgent(t *testing.T, a *Agent, mock *mockCaller) *largemodel.Request {
	t.Helper()

	if _, err := a.Run(context.Background(), &schema.RunRequest{
		Messages: []schema.Message{schema.NewUserMessage(testProtocol, "hi")},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	reqs := mock.Requests()
	if len(reqs) == 0 {
		t.Fatal("no LLM requests captured")
	}

	return reqs[0]
}

// TestAgent_Run_PromptCachingDefault confirms caching is on by default and
// the agent asks for it on every outbound request.
func TestAgent_Run_PromptCachingDefault(t *testing.T) {
	mock := newMock(stopResponse("ok"))
	req := runCachingAgent(t, cachingTestAgent(t, mock), mock)

	if !req.PromptCaching {
		t.Error("PromptCaching = false, want true by default")
	}

	if len(req.Tools) == 0 {
		t.Error("expected at least one tool on the request")
	}
}

// TestAgent_Run_PromptCachingDisabled verifies WithPromptCaching(false)
// suppresses the request-level intent.
func TestAgent_Run_PromptCachingDisabled(t *testing.T) {
	mock := newMock(stopResponse("ok"))
	req := runCachingAgent(t, cachingTestAgent(t, mock, WithPromptCaching(false)), mock)

	if req.PromptCaching {
		t.Error("PromptCaching = true, want false when disabled")
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
