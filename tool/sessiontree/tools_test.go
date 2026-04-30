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

package sessiontree

import (
	"context"
	"strings"
	"testing"

	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/session/tree"
	"github.com/vogo/vage/tool"
)

// newTestStore returns a fresh in-memory MapTreeStore wired with a no-op
// Promoter so tree_promote works without a real LLM.
func newTestStore(t *testing.T) tree.SessionTreeStore {
	t.Helper()
	return tree.NewMapTreeStore(tree.WithMapPromoter(tree.NoopPromoter{}))
}

// newRegistryWithTools builds a registry, registers all five tree tools, and
// returns the registry. Fails the test on registration error.
func newRegistryWithTools(t *testing.T, store tree.SessionTreeStore) *tool.Registry {
	t.Helper()
	reg := tool.NewRegistry()
	if err := Register(reg, store); err != nil {
		t.Fatalf("Register: %v", err)
	}
	return reg
}

// callTool runs the named tool through the registry's Execute path so the
// path matches what TaskAgent invokes at runtime.
func callTool(t *testing.T, reg *tool.Registry, ctx context.Context, name, args string) schema.ToolResult {
	t.Helper()
	res, err := reg.Execute(ctx, name, args)
	if err != nil {
		t.Fatalf("Execute(%s): %v", name, err)
	}
	return res
}

// resultText extracts the first text content part. Tools always reply with a
// single text block so this keeps the assertions terse.
func resultText(r schema.ToolResult) string {
	if len(r.Content) == 0 {
		return ""
	}
	return r.Content[0].Text
}

// withSession seeds the session id schema.SessionIDFromContext expects.
func withSession(ctx context.Context, sid string) context.Context {
	return schema.WithSessionID(ctx, sid)
}

// TestRegister_NilStore makes sure a nil store yields a clean error rather
// than registering tools that segfault later.
func TestRegister_NilStore(t *testing.T) {
	reg := tool.NewRegistry()
	if err := Register(reg, nil); err == nil {
		t.Fatalf("Register with nil store returned nil error")
	}
}

// TestRegister_All verifies every tool name appears in the registry after
// Register; lets the LLM-side contract change in lockstep with the package.
func TestRegister_All(t *testing.T) {
	reg := newRegistryWithTools(t, newTestStore(t))
	for _, name := range []string{
		AddToolName, UpdateToolName, CursorToolName, PromoteToolName, ZoomInToolName,
	} {
		if _, ok := reg.Get(name); !ok {
			t.Errorf("tool %q not registered", name)
		}
	}
}

// TestAdd_CreatesRoot covers the empty-tree path: parent_id="" and no tree
// yet means we should bootstrap a goal root.
func TestAdd_CreatesRoot(t *testing.T) {
	store := newTestStore(t)
	reg := newRegistryWithTools(t, store)
	ctx := withSession(context.Background(), "sess-1")

	res := callTool(t, reg, ctx, AddToolName, `{"title":"build login"}`)
	if res.IsError {
		t.Fatalf("tree_add returned error: %s", resultText(res))
	}
	if !strings.Contains(resultText(res), "added node") {
		t.Errorf("missing acknowledgement: %s", resultText(res))
	}
	tr, err := store.GetTree(ctx, "sess-1")
	if err != nil {
		t.Fatalf("GetTree: %v", err)
	}
	root := tr.Nodes[tr.RootID]
	if root.Type != tree.NodeGoal {
		t.Errorf("root type = %q, want goal", root.Type)
	}
	if root.Status != tree.StatusActive {
		t.Errorf("root status = %q, want active", root.Status)
	}
}

// TestAdd_AttachToRoot verifies that with an existing tree, parent_id="" is
// shorthand for "attach under root".
func TestAdd_AttachToRoot(t *testing.T) {
	store := newTestStore(t)
	reg := newRegistryWithTools(t, store)
	ctx := withSession(context.Background(), "sess-2")

	res := callTool(t, reg, ctx, AddToolName, `{"title":"goal"}`)
	if res.IsError {
		t.Fatalf("create root: %s", resultText(res))
	}
	res = callTool(t, reg, ctx, AddToolName, `{"title":"step1"}`)
	if res.IsError {
		t.Fatalf("step1: %s", resultText(res))
	}

	tr, _ := store.GetTree(ctx, "sess-2")
	root := tr.Nodes[tr.RootID]
	if len(root.Children) != 1 {
		t.Fatalf("root.Children = %v, want 1 child", root.Children)
	}
	child := tr.Nodes[root.Children[0]]
	if child.Type != tree.NodeSubtask {
		t.Errorf("default type = %q, want subtask", child.Type)
	}
}

// TestAdd_NoSession exercises the missing-session-id branch — the agent
// runtime is supposed to inject one but this guard saves us from a silent
// store call without context.
func TestAdd_NoSession(t *testing.T) {
	reg := newRegistryWithTools(t, newTestStore(t))
	res := callTool(t, reg, context.Background(), AddToolName, `{"title":"x"}`)
	if !res.IsError {
		t.Fatalf("expected error when session id missing")
	}
}

// TestUpdate_ChangesStatus covers the common "mark step done" usage.
func TestUpdate_ChangesStatus(t *testing.T) {
	store := newTestStore(t)
	reg := newRegistryWithTools(t, store)
	ctx := withSession(context.Background(), "sess-3")

	callTool(t, reg, ctx, AddToolName, `{"title":"goal"}`)
	callTool(t, reg, ctx, AddToolName, `{"title":"step1"}`)
	tr, _ := store.GetTree(ctx, "sess-3")
	stepID := tr.Nodes[tr.RootID].Children[0]

	res := callTool(t, reg, ctx, UpdateToolName, `{"node_id":"`+stepID+`","title":"step1","status":"done"}`)
	if res.IsError {
		t.Fatalf("update: %s", resultText(res))
	}
	tr, _ = store.GetTree(ctx, "sess-3")
	if got := tr.Nodes[stepID].Status; got != tree.StatusDone {
		t.Errorf("status = %q, want done", got)
	}
}

// TestCursor_SetClear walks the cursor onto a node then clears it.
func TestCursor_SetClear(t *testing.T) {
	store := newTestStore(t)
	reg := newRegistryWithTools(t, store)
	ctx := withSession(context.Background(), "sess-4")
	callTool(t, reg, ctx, AddToolName, `{"title":"goal"}`)
	callTool(t, reg, ctx, AddToolName, `{"title":"step"}`)
	tr, _ := store.GetTree(ctx, "sess-4")
	stepID := tr.Nodes[tr.RootID].Children[0]

	res := callTool(t, reg, ctx, CursorToolName, `{"node_id":"`+stepID+`"}`)
	if res.IsError {
		t.Fatalf("cursor set: %s", resultText(res))
	}
	tr, _ = store.GetTree(ctx, "sess-4")
	if tr.Cursor != stepID {
		t.Errorf("cursor = %q, want %q", tr.Cursor, stepID)
	}

	res = callTool(t, reg, ctx, CursorToolName, `{"node_id":""}`)
	if res.IsError {
		t.Fatalf("cursor clear: %s", resultText(res))
	}
	tr, _ = store.GetTree(ctx, "sess-4")
	if tr.Cursor != "" {
		t.Errorf("cursor not cleared: %q", tr.Cursor)
	}
}

// TestPromote_NoopFolds children when noop promoter is wired (default in tests).
// The Noop case still flips Promoted=true on eligible children, which is
// exactly what the LLM needs to verify the fold happened.
func TestPromote_NoopFolds(t *testing.T) {
	store := newTestStore(t)
	reg := newRegistryWithTools(t, store)
	ctx := withSession(context.Background(), "sess-5")

	callTool(t, reg, ctx, AddToolName, `{"title":"goal"}`)
	for range 3 {
		callTool(t, reg, ctx, AddToolName, `{"title":"sub"}`)
	}
	tr, _ := store.GetTree(ctx, "sess-5")
	root := tr.Nodes[tr.RootID]

	res := callTool(t, reg, ctx, PromoteToolName, `{"node_id":"`+root.ID+`"}`)
	if res.IsError {
		t.Fatalf("promote: %s", resultText(res))
	}
	tr, _ = store.GetTree(ctx, "sess-5")
	for _, cid := range tr.Nodes[tr.RootID].Children {
		if !tr.Nodes[cid].Promoted {
			t.Errorf("child %s not promoted after fold", cid)
		}
	}
}

// TestZoomIn_RendersFolded ensures the zoom output includes promoted children
// with the [folded] marker — the whole point of the tool.
func TestZoomIn_RendersFolded(t *testing.T) {
	store := newTestStore(t)
	reg := newRegistryWithTools(t, store)
	ctx := withSession(context.Background(), "sess-6")

	callTool(t, reg, ctx, AddToolName, `{"title":"goal"}`)
	callTool(t, reg, ctx, AddToolName, `{"title":"folded child","summary":"work done"}`)
	tr, _ := store.GetTree(ctx, "sess-6")
	root := tr.Nodes[tr.RootID]
	callTool(t, reg, ctx, PromoteToolName, `{"node_id":"`+root.ID+`"}`)

	res := callTool(t, reg, ctx, ZoomInToolName, `{"node_id":"`+root.ID+`"}`)
	if res.IsError {
		t.Fatalf("zoom: %s", resultText(res))
	}
	if !strings.Contains(resultText(res), "[folded]") {
		t.Errorf("missing folded marker in zoom output:\n%s", resultText(res))
	}
	if !strings.Contains(resultText(res), "folded child") {
		t.Errorf("missing folded child title:\n%s", resultText(res))
	}
}

// TestZoomIn_DefaultsToCursor verifies node_id="" falls back to the cursor.
func TestZoomIn_DefaultsToCursor(t *testing.T) {
	store := newTestStore(t)
	reg := newRegistryWithTools(t, store)
	ctx := withSession(context.Background(), "sess-7")

	callTool(t, reg, ctx, AddToolName, `{"title":"goal"}`)
	res := callTool(t, reg, ctx, ZoomInToolName, `{}`)
	if res.IsError {
		t.Fatalf("zoom default: %s", resultText(res))
	}
	if !strings.Contains(resultText(res), "Subtree at") {
		t.Errorf("rendered output missing header: %s", resultText(res))
	}
}

// TestErrResult_KnownSentinels confirms each known store error maps to a
// distinct, helpful message — the LLM relies on the prefix to react.
func TestErrResult_KnownSentinels(t *testing.T) {
	cases := map[string]error{
		"invalid argument":    tree.ErrInvalidArgument,
		"not found":           tree.ErrNotFound,
		"tree does not exist": tree.ErrTreeMissing,
		"already exists":      tree.ErrAlreadyExists,
		"is full":             tree.ErrTreeFull,
		"has children":        tree.ErrHasChildren,
		"is immutable":        tree.ErrImmutableField,
	}
	for substr, err := range cases {
		got := errResult("tree_x", err)
		if !strings.Contains(resultText(got), substr) {
			t.Errorf("%v -> %q; want substring %q", err, resultText(got), substr)
		}
		if !got.IsError {
			t.Errorf("%v -> IsError=false; want true", err)
		}
	}
}
