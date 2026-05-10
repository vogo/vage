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

package agenttool

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/vogo/aimodel"
	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/session"
	"github.com/vogo/vage/sessionview"
	"github.com/vogo/vage/tool"
)

// makeEchoAgent returns an agent that captures the runtime ctx of the
// most recent invocation, so tests can assert what session id and
// SessionView the dispatcher attached.
func makeEchoAgent(t *testing.T, gotCtx *context.Context) *mockAgent {
	t.Helper()
	return newMockAgent("sub-1", "subagent", "echoes input",
		func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
			*gotCtx = ctx
			input := req.Messages[0].Content.Text()
			return &schema.RunResponse{
				Messages: []schema.Message{{
					Message: aimodel.Message{
						Role:    aimodel.RoleAssistant,
						Content: aimodel.NewTextContent("Echo: " + input),
					},
				}},
			}, nil
		})
}

// TestRegister_NoSessionContext_BackwardCompat preserves the legacy
// shape: without WithSessionContext, the subagent inherits the parent
// ctx unchanged and no [child_session=...] annotation is added.
func TestRegister_NoSessionContext_BackwardCompat(t *testing.T) {
	var capturedCtx context.Context
	ag := makeEchoAgent(t, &capturedCtx)

	reg := tool.NewRegistry()
	if err := Register(reg, ag); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, ok := reg.Get(ag.Name()); !ok {
		t.Fatalf("tool not registered")
	}
	handler := func(ctx context.Context, _, args string) (schema.ToolResult, error) {
		return reg.Execute(ctx, ag.Name(), args)
	}

	parent := schema.WithSessionID(context.Background(), "parent-sid")
	res, err := handler(parent, "", `{"input":"hello"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if res.IsError {
		t.Fatalf("res = %+v", res)
	}
	body := resultText(res)
	if strings.Contains(body, "[child_session=") {
		t.Errorf("legacy mode must not annotate; got %q", body)
	}
	// Sub-agent saw the parent's session id.
	if got := schema.SessionIDFromContext(capturedCtx); got != "parent-sid" {
		t.Errorf("subagent ctx sid = %q, want parent-sid", got)
	}
}

// TestRegister_WithSessionContext_CreatesChild verifies the canonical
// dispatch path: a child Session is persisted with parent_id set,
// AgentID populated, and the subagent runs under the child's session
// id with a SessionView attached.
func TestRegister_WithSessionContext_CreatesChild(t *testing.T) {
	var capturedCtx context.Context
	ag := makeEchoAgent(t, &capturedCtx)
	store := session.NewMapSessionStore()

	// Seed the parent.
	parent := session.New("parent-sid")
	if err := store.Create(context.Background(), parent); err != nil {
		t.Fatalf("seed parent: %v", err)
	}

	reg := tool.NewRegistry()
	var minted atomic.Int32
	if err := Register(reg, ag,
		WithSessionContext(store),
		WithChildIDFunc(func() string {
			minted.Add(1)
			return "child-deterministic"
		}),
	); err != nil {
		t.Fatalf("Register: %v", err)
	}
	handler := func(ctx context.Context, _, args string) (schema.ToolResult, error) {
		return reg.Execute(ctx, ag.Name(), args)
	}

	parentCtx := schema.WithSessionID(context.Background(), "parent-sid")
	res, err := handler(parentCtx, "", `{"input":"do thing"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if res.IsError {
		t.Fatalf("res = %+v", res)
	}

	// Child session must exist with parent_id linkage.
	child, err := store.Get(context.Background(), "child-deterministic")
	if err != nil {
		t.Fatalf("Get child: %v", err)
	}
	if child.ParentID != "parent-sid" {
		t.Errorf("ParentID = %q, want parent-sid", child.ParentID)
	}
	if child.AgentID != "sub-1" {
		t.Errorf("AgentID = %q, want sub-1", child.AgentID)
	}

	// Subagent ran under child sid.
	if got := schema.SessionIDFromContext(capturedCtx); got != "child-deterministic" {
		t.Errorf("subagent ctx sid = %q, want child-deterministic", got)
	}

	// SessionView attached to ctx with the right shape.
	view, ok := sessionview.FromContext(capturedCtx)
	if !ok {
		t.Fatalf("SessionView not in subagent ctx")
	}
	if view.ParentSessionID != "parent-sid" || view.ChildSessionID != "child-deterministic" {
		t.Errorf("view = %+v", view)
	}
	if view.Subgoal != "do thing" {
		t.Errorf("Subgoal = %q, want 'do thing'", view.Subgoal)
	}
	if view.ScratchSlot != "child-deterministic" {
		t.Errorf("ScratchSlot = %q, want child-deterministic", view.ScratchSlot)
	}

	// Annotation present in tool result.
	body := resultText(res)
	if !strings.Contains(body, "[child_session=child-deterministic]") {
		t.Errorf("missing annotation in body %q", body)
	}
	if !strings.Contains(body, "Echo: do thing") {
		t.Errorf("missing echo in body %q", body)
	}
}

// TestRegister_WithSessionContext_ParentIDMissing covers a CLI-style
// invocation where the parent has no session: the child is still
// created (as a top-level standalone session) with ParentID = "".
func TestRegister_WithSessionContext_ParentIDMissing(t *testing.T) {
	var capturedCtx context.Context
	ag := makeEchoAgent(t, &capturedCtx)
	store := session.NewMapSessionStore()

	reg := tool.NewRegistry()
	if err := Register(reg, ag,
		WithSessionContext(store),
		WithChildIDFunc(func() string { return "standalone-child" }),
	); err != nil {
		t.Fatalf("Register: %v", err)
	}
	handler := func(ctx context.Context, _, args string) (schema.ToolResult, error) {
		return reg.Execute(ctx, ag.Name(), args)
	}

	res, err := handler(context.Background(), "", `{"input":"x"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if res.IsError {
		t.Fatalf("res = %+v", res)
	}
	child, err := store.Get(context.Background(), "standalone-child")
	if err != nil {
		t.Fatalf("Get child: %v", err)
	}
	if child.ParentID != "" {
		t.Errorf("ParentID = %q, want empty", child.ParentID)
	}
}

// TestRegister_WithViewBuilder lets the dispatcher attach plan/notes
// snapshots. We supply a builder that injects a custom slot id and
// plan body and assert the subagent sees both.
func TestRegister_WithViewBuilder(t *testing.T) {
	var capturedCtx context.Context
	ag := makeEchoAgent(t, &capturedCtx)
	store := session.NewMapSessionStore()
	if err := store.Create(context.Background(), session.New("p")); err != nil {
		t.Fatalf("seed: %v", err)
	}

	reg := tool.NewRegistry()
	if err := Register(reg, ag,
		WithSessionContext(store),
		WithChildIDFunc(func() string { return "c" }),
		WithViewBuilder(func(parentSID, childSID, subgoal string) *sessionview.SessionView {
			return &sessionview.SessionView{
				ParentSessionID: parentSID,
				ChildSessionID:  childSID,
				Subgoal:         subgoal,
				ScratchSlot:     "custom-slot",
				PlanSnapshot:    "## Plan\n- step",
				NotesIndex:      []string{"alpha"},
			}
		}),
	); err != nil {
		t.Fatalf("Register: %v", err)
	}
	handler := func(ctx context.Context, _, args string) (schema.ToolResult, error) {
		return reg.Execute(ctx, ag.Name(), args)
	}

	parentCtx := schema.WithSessionID(context.Background(), "p")
	if _, err := handler(parentCtx, "", `{"input":"go"}`); err != nil {
		t.Fatalf("handler: %v", err)
	}

	view, ok := sessionview.FromContext(capturedCtx)
	if !ok {
		t.Fatalf("no SessionView")
	}
	if view.ScratchSlot != "custom-slot" {
		t.Errorf("ScratchSlot = %q", view.ScratchSlot)
	}
	if view.PlanSnapshot == "" {
		t.Errorf("PlanSnapshot empty")
	}
	if len(view.NotesIndex) != 1 || view.NotesIndex[0] != "alpha" {
		t.Errorf("NotesIndex = %v", view.NotesIndex)
	}
}

// TestRegister_WithSessionContext_DuplicateChildIDFails: when the
// minter returns a colliding id (rare but possible if the override is
// buggy), the handler surfaces a clean error rather than silently
// reusing the prior child's record.
func TestRegister_WithSessionContext_DuplicateChildIDFails(t *testing.T) {
	var capturedCtx context.Context
	ag := makeEchoAgent(t, &capturedCtx)
	store := session.NewMapSessionStore()

	reg := tool.NewRegistry()
	if err := Register(reg, ag,
		WithSessionContext(store),
		WithChildIDFunc(func() string { return "stuck" }),
	); err != nil {
		t.Fatalf("Register: %v", err)
	}
	handler := func(ctx context.Context, _, args string) (schema.ToolResult, error) {
		return reg.Execute(ctx, ag.Name(), args)
	}

	if _, err := handler(context.Background(), "", `{"input":"first"}`); err != nil {
		t.Fatalf("first call: %v", err)
	}
	res, err := handler(context.Background(), "", `{"input":"second"}`)
	if err != nil {
		t.Fatalf("second call (Go err): %v", err)
	}
	if !res.IsError {
		t.Errorf("expected IsError on duplicate child id, got %+v", res)
	}
	if !strings.Contains(resultText(res), "session setup failed") {
		t.Errorf("unexpected message: %q", resultText(res))
	}
}

func TestDefaultScratchSlot_Truncation(t *testing.T) {
	long := strings.Repeat("a", 80)
	got := defaultScratchSlot(long)
	if len(got) != 64 {
		t.Errorf("len = %d, want 64", len(got))
	}
}

func TestDefaultScratchSlot_PassThrough(t *testing.T) {
	got := defaultScratchSlot("short-id")
	if got != "short-id" {
		t.Errorf("got = %q", got)
	}
}

func TestSubgoalFromArgs(t *testing.T) {
	if got := SubgoalFromArgs(map[string]any{"input": "  hello  "}); got != "hello" {
		t.Errorf("got = %q", got)
	}
	if got := SubgoalFromArgs(map[string]any{}); got != "" {
		t.Errorf("got = %q, want empty", got)
	}
	if got := SubgoalFromArgs(map[string]any{"input": 5}); got != "" {
		t.Errorf("non-string: got = %q", got)
	}
}

// resultText pulls the assistant text out of a tool result for tests.
func resultText(r schema.ToolResult) string {
	for _, p := range r.Content {
		if p.Text != "" {
			return p.Text
		}
	}
	return ""
}
