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

const (
	quickID     = "assistant"
	quickName   = "Assistant"
	quickModel  = "gpt-4o"
	quickPrompt = "You are helpful."
)

// TestQuick_MatchesExpandedNew pins Quick to the construction it claims to be
// shorthand for: same identity, same protocol, and the same model request down
// the basic Ask path.
func TestQuick_MatchesExpandedNew(t *testing.T) {
	quick, expanded := newMock(stopResponse("hello")), newMock(stopResponse("hello"))

	qa := Quick(quickID, quickName, quick, quickModel, quickPrompt)
	na := New(
		agent.Config{ID: quickID, Name: quickName},
		WithCaller(expanded),
		WithModel(quickModel),
		WithSystemPrompt(prompt.StringPrompt(quickPrompt)),
	)

	if qa.ID() != na.ID() || qa.Name() != na.Name() {
		t.Fatalf("identity = (%q, %q), want (%q, %q)", qa.ID(), qa.Name(), na.ID(), na.Name())
	}
	if qa.Protocol() != na.Protocol() {
		t.Fatalf("Protocol = %q, want %q", qa.Protocol(), na.Protocol())
	}
	if qa.Description() != "" {
		t.Errorf("Description = %q, want empty", qa.Description())
	}

	// agent.RunText is the framework's shortest Ask path; driving both agents
	// through it covers the entry-level question in one call.
	for _, tc := range []struct {
		name string
		a    *Agent
	}{{"quick", qa}, {"new", na}} {
		resp, err := agent.RunText(context.Background(), tc.a, "Hi")
		if err != nil {
			t.Fatalf("%s: RunText: %v", tc.name, err)
		}
		if got := resp.Messages[len(resp.Messages)-1].Text(); got != "hello" {
			t.Errorf("%s: answer = %q, want %q", tc.name, got, "hello")
		}
		if resp.StopReason != schema.StopReasonComplete {
			t.Errorf("%s: StopReason = %q, want %q", tc.name, resp.StopReason, schema.StopReasonComplete)
		}
	}

	qreq, nreq := quick.Requests(), expanded.Requests()
	if len(qreq) != 1 || len(nreq) != 1 {
		t.Fatalf("call counts = (%d, %d), want (1, 1)", len(qreq), len(nreq))
	}

	if qreq[0].Model != nreq[0].Model || qreq[0].Model != quickModel {
		t.Errorf("Model = %q / %q, want %q", qreq[0].Model, nreq[0].Model, quickModel)
	}
	if qreq[0].PromptCaching != nreq[0].PromptCaching {
		t.Errorf("PromptCaching = %v, want %v", qreq[0].PromptCaching, nreq[0].PromptCaching)
	}
	if len(qreq[0].Messages) != len(nreq[0].Messages) {
		t.Fatalf("len(Messages) = %d, want %d", len(qreq[0].Messages), len(nreq[0].Messages))
	}

	for i := range qreq[0].Messages {
		q, n := qreq[0].Messages[i], nreq[0].Messages[i]
		if q.Role() != n.Role() || q.Text() != n.Text() || q.Protocol() != n.Protocol() {
			t.Errorf("Messages[%d] = (%q, %q, %q), want (%q, %q, %q)",
				i, q.Role(), q.Text(), q.Protocol(), n.Role(), n.Text(), n.Protocol())
		}
	}

	sys := qreq[0].Messages[0]
	if sys.Role() != schema.RoleSystem || sys.Text() != quickPrompt {
		t.Errorf("system message = (%q, %q), want (%q, %q)",
			sys.Role(), sys.Text(), schema.RoleSystem, quickPrompt)
	}
	if user := qreq[0].Messages[len(qreq[0].Messages)-1]; user.Role() != schema.RoleUser || user.Text() != "Hi" {
		t.Errorf("user message = (%q, %q), want (%q, %q)", user.Role(), user.Text(), schema.RoleUser, "Hi")
	}
}

// TestQuick_RunMatchesExpandedNew covers the direct Run entry point, where the
// caller supplies the request instead of agent.RunText.
func TestQuick_RunMatchesExpandedNew(t *testing.T) {
	quick, expanded := newMock(stopResponse("hello")), newMock(stopResponse("hello"))

	agents := map[string]*Agent{
		"quick": Quick(quickID, quickName, quick, quickModel, quickPrompt),
		"new": New(
			agent.Config{ID: quickID, Name: quickName},
			WithCaller(expanded),
			WithModel(quickModel),
			WithSystemPrompt(prompt.StringPrompt(quickPrompt)),
		),
	}

	for name, a := range agents {
		resp, err := a.Run(context.Background(), &schema.RunRequest{
			Messages: []schema.Message{schema.NewUserMessage(a.Protocol(), "Hi")},
		})
		if err != nil {
			t.Fatalf("%s: Run: %v", name, err)
		}
		if got := resp.Messages[len(resp.Messages)-1].Text(); got != "hello" {
			t.Errorf("%s: answer = %q, want %q", name, got, "hello")
		}
		if resp.StopReason != schema.StopReasonComplete {
			t.Errorf("%s: StopReason = %q, want %q", name, resp.StopReason, schema.StopReasonComplete)
		}
	}

	if quick.Calls() != expanded.Calls() {
		t.Errorf("Calls = %d, want %d", quick.Calls(), expanded.Calls())
	}
}

// TestQuick_AdoptsCallerProtocol proves protocol derivation still happens in
// New: Quick takes no protocol parameter, so a non-default caller must still
// pull the agent and its messages onto that wire form.
func TestQuick_AdoptsCallerProtocol(t *testing.T) {
	mock := &mockCaller{FakeCaller: &largemodel.FakeCaller{
		Proto: schema.ProtocolAnthropicMessages,
		Responses: []*largemodel.Response{
			largemodel.FakeStopResponse(schema.ProtocolAnthropicMessages, "ok", schema.Usage{}),
		},
	}}

	a := Quick(quickID, quickName, mock, "claude-sonnet-4-5", quickPrompt)
	if a.Protocol() != schema.ProtocolAnthropicMessages {
		t.Fatalf("Protocol = %q, want %q", a.Protocol(), schema.ProtocolAnthropicMessages)
	}

	if _, err := agent.RunText(context.Background(), a, "Hi"); err != nil {
		t.Fatalf("RunText: %v", err)
	}

	sys := mock.Requests()[0].Messages[0]
	if err := sys.RequireProtocol(schema.ProtocolAnthropicMessages); err != nil {
		t.Fatalf("system message RequireProtocol: %v", err)
	}
}

// TestQuick_ExtraOptionsApply checks that options beyond the three presets are
// not swallowed.
func TestQuick_ExtraOptionsApply(t *testing.T) {
	reg := tool.NewRegistry()
	_ = reg.Register(
		schema.ToolDef{Name: "get_weather", Description: "Get weather"},
		func(_ context.Context, _, _ string) (schema.ToolResult, error) {
			return schema.TextResult("", "sunny, 22°C"), nil
		},
	)

	a := Quick(
		quickID, quickName, newMock(), quickModel, quickPrompt,
		WithToolRegistry(reg),
		WithMaxIterations(3),
	)

	if len(a.Tools()) != 1 {
		t.Errorf("len(Tools) = %d, want 1", len(a.Tools()))
	}
	if a.maxIterations != 3 {
		t.Errorf("maxIterations = %d, want 3", a.maxIterations)
	}
}

// TestQuick_ExtraOptionsOverridePresets pins the ordering contract: the preset
// caller/model/prompt are applied first, so a trailing option wins.
func TestQuick_ExtraOptionsOverridePresets(t *testing.T) {
	preset, override := newMock(), newMock(stopResponse("overridden"))

	a := Quick(
		quickID, quickName, preset, quickModel, quickPrompt,
		WithCaller(override),
		WithModel("gpt-4o-mini"),
		WithSystemPrompt(prompt.StringPrompt("You are terse.")),
	)

	if _, err := agent.RunText(context.Background(), a, "Hi"); err != nil {
		t.Fatalf("RunText: %v", err)
	}

	if preset.Calls() != 0 {
		t.Errorf("preset caller Calls = %d, want 0", preset.Calls())
	}

	req := override.Requests()[0]
	if req.Model != "gpt-4o-mini" {
		t.Errorf("Model = %q, want %q", req.Model, "gpt-4o-mini")
	}
	if req.Messages[0].Text() != "You are terse." {
		t.Errorf("system content = %q, want %q", req.Messages[0].Text(), "You are terse.")
	}
}

// TestQuick_NilCallerFailsLikeNew keeps Quick from growing validation New does
// not have: the failure must stay at first Run, not at construction.
func TestQuick_NilCallerFailsLikeNew(t *testing.T) {
	a := Quick(quickID, quickName, nil, quickModel, quickPrompt)
	if a == nil {
		t.Fatal("Quick returned nil for a nil caller, want an agent that fails at Run")
	}

	_, quickErr := agent.RunText(context.Background(), a, "Hi")

	n := New(agent.Config{ID: quickID, Name: quickName},
		WithCaller(nil), WithModel(quickModel), WithSystemPrompt(prompt.StringPrompt(quickPrompt)))
	_, newErr := agent.RunText(context.Background(), n, "Hi")

	if (quickErr == nil) != (newErr == nil) {
		t.Fatalf("Quick err = %v, New err = %v: failure timing diverged", quickErr, newErr)
	}
	if quickErr != nil && newErr != nil && quickErr.Error() != newErr.Error() {
		t.Errorf("Quick err = %q, want %q", quickErr, newErr)
	}
}
