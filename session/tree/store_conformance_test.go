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

package tree

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// runStoreConformance runs a black-box scenario suite against the supplied
// store factory. MapStore and FileStore both invoke this so behaviour stays
// in lock-step.
//
//nolint:funlen // Conformance suites exercise full surface area; splitting
// would obscure the linear narrative for the reader.
func runStoreConformance(t *testing.T, makeStore func(t *testing.T) SessionTreeStore) {
	t.Run("CreateTree happy path", func(t *testing.T) {
		store := makeStore(t)
		ctx := context.Background()
		root, err := store.CreateTree(ctx, "sess-a", TreeNode{Title: "build OAuth"})
		if err != nil {
			t.Fatalf("CreateTree: %v", err)
		}
		if root.Type != NodeGoal {
			t.Errorf("default Type=%s want %s", root.Type, NodeGoal)
		}
		if root.Status != StatusActive {
			t.Errorf("default Status=%s want %s", root.Status, StatusActive)
		}
		if root.ID == "" {
			t.Error("ID not assigned")
		}
		if root.Depth != 0 {
			t.Errorf("root Depth=%d want 0", root.Depth)
		}
		// Cursor points at root after CreateTree.
		tr, err := store.GetTree(ctx, "sess-a")
		if err != nil {
			t.Fatalf("GetTree: %v", err)
		}
		if tr.Cursor != root.ID {
			t.Errorf("cursor=%q want %q", tr.Cursor, root.ID)
		}
	})

	t.Run("CreateTree duplicate", func(t *testing.T) {
		store := makeStore(t)
		ctx := context.Background()
		if _, err := store.CreateTree(ctx, "sess-a", TreeNode{Title: "T"}); err != nil {
			t.Fatalf("first: %v", err)
		}
		_, err := store.CreateTree(ctx, "sess-a", TreeNode{Title: "T2"})
		if !errors.Is(err, ErrAlreadyExists) {
			t.Errorf("got %v want ErrAlreadyExists", err)
		}
	})

	t.Run("CreateTree invalid sessionID", func(t *testing.T) {
		store := makeStore(t)
		_, err := store.CreateTree(context.Background(), "", TreeNode{Title: "T"})
		if !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("got %v want ErrInvalidArgument", err)
		}
	})

	t.Run("GetTree missing", func(t *testing.T) {
		store := makeStore(t)
		_, err := store.GetTree(context.Background(), "sess-missing")
		if !errors.Is(err, ErrTreeMissing) {
			t.Errorf("got %v want ErrTreeMissing", err)
		}
	})

	t.Run("AddNode happy path", func(t *testing.T) {
		store := makeStore(t)
		ctx := context.Background()
		root, _ := store.CreateTree(ctx, "s", TreeNode{Title: "Goal"})
		child, err := store.AddNode(ctx, "s", root.ID, TreeNode{Type: NodeSubtask, Title: "step1"})
		if err != nil {
			t.Fatalf("AddNode: %v", err)
		}
		if child.Parent != root.ID {
			t.Errorf("Parent=%q want %q", child.Parent, root.ID)
		}
		if child.Depth != 1 {
			t.Errorf("Depth=%d want 1", child.Depth)
		}
		if child.Status != StatusPending {
			t.Errorf("Status=%s want pending", child.Status)
		}
		tr, _ := store.GetTree(ctx, "s")
		parent := tr.Nodes[root.ID]
		if len(parent.Children) != 1 || parent.Children[0] != child.ID {
			t.Errorf("parent children mismatch: %+v", parent.Children)
		}
	})

	t.Run("AddNode parent missing", func(t *testing.T) {
		store := makeStore(t)
		ctx := context.Background()
		_, _ = store.CreateTree(ctx, "s", TreeNode{Title: "Goal"})
		_, err := store.AddNode(ctx, "s", "tn-bogus", TreeNode{Type: NodeFact, Title: "f"})
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("got %v want ErrNotFound", err)
		}
	})

	t.Run("UpdateNode mutable fields", func(t *testing.T) {
		store := makeStore(t)
		ctx := context.Background()
		root, _ := store.CreateTree(ctx, "s", TreeNode{Title: "Goal"})
		updated, err := store.UpdateNode(ctx, "s", TreeNode{
			ID: root.ID, Title: "Goal v2", Summary: "added detail", Status: StatusDone, Pinned: true,
		})
		if err != nil {
			t.Fatalf("UpdateNode: %v", err)
		}
		if updated.Title != "Goal v2" || updated.Summary != "added detail" {
			t.Errorf("update did not stick: %+v", updated)
		}
		if updated.Status != StatusDone {
			t.Errorf("Status=%s want done", updated.Status)
		}
		if !updated.Pinned {
			t.Error("Pinned not propagated")
		}
		// UpdatedAt should advance (or at least not regress).
		if updated.UpdatedAt.Before(root.UpdatedAt) {
			t.Errorf("UpdatedAt regressed: %v < %v", updated.UpdatedAt, root.UpdatedAt)
		}
	})

	t.Run("UpdateNode immutable Type", func(t *testing.T) {
		store := makeStore(t)
		ctx := context.Background()
		root, _ := store.CreateTree(ctx, "s", TreeNode{Title: "Goal"})
		_, err := store.UpdateNode(ctx, "s", TreeNode{
			ID: root.ID, Type: NodeSubtask, Title: "still",
		})
		if !errors.Is(err, ErrImmutableField) {
			t.Errorf("got %v want ErrImmutableField", err)
		}
	})

	t.Run("DeleteNode leaf only", func(t *testing.T) {
		store := makeStore(t)
		ctx := context.Background()
		root, _ := store.CreateTree(ctx, "s", TreeNode{Title: "Goal"})
		c1, _ := store.AddNode(ctx, "s", root.ID, TreeNode{Type: NodeSubtask, Title: "step1"})
		_, _ = store.AddNode(ctx, "s", c1.ID, TreeNode{Type: NodeSubtask, Title: "step1.1"})

		// Cannot delete c1 (has children).
		err := store.DeleteNode(ctx, "s", c1.ID)
		if !errors.Is(err, ErrHasChildren) {
			t.Errorf("got %v want ErrHasChildren", err)
		}
		// Cannot delete root.
		err = store.DeleteNode(ctx, "s", root.ID)
		if !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("got %v want ErrInvalidArgument", err)
		}
	})

	t.Run("DeleteNode happy path", func(t *testing.T) {
		store := makeStore(t)
		ctx := context.Background()
		root, _ := store.CreateTree(ctx, "s", TreeNode{Title: "Goal"})
		c1, _ := store.AddNode(ctx, "s", root.ID, TreeNode{Type: NodeFact, Title: "fact1"})
		_ = store.SetCursor(ctx, "s", c1.ID)
		if err := store.DeleteNode(ctx, "s", c1.ID); err != nil {
			t.Fatalf("DeleteNode: %v", err)
		}
		tr, _ := store.GetTree(ctx, "s")
		if _, ok := tr.Nodes[c1.ID]; ok {
			t.Error("node still present")
		}
		if len(tr.Nodes[root.ID].Children) != 0 {
			t.Errorf("parent children not cleared: %+v", tr.Nodes[root.ID].Children)
		}
		// Deleting the cursor should fall back to root.
		if tr.Cursor != root.ID {
			t.Errorf("cursor=%q want %q after delete", tr.Cursor, root.ID)
		}
	})

	t.Run("SetCursor", func(t *testing.T) {
		store := makeStore(t)
		ctx := context.Background()
		root, _ := store.CreateTree(ctx, "s", TreeNode{Title: "Goal"})
		c1, _ := store.AddNode(ctx, "s", root.ID, TreeNode{Type: NodeSubtask, Title: "step1"})
		if err := store.SetCursor(ctx, "s", c1.ID); err != nil {
			t.Fatalf("SetCursor: %v", err)
		}
		tr, _ := store.GetTree(ctx, "s")
		if tr.Cursor != c1.ID {
			t.Errorf("cursor=%q want %q", tr.Cursor, c1.ID)
		}
		// SetCursor to "" clears.
		if err := store.SetCursor(ctx, "s", ""); err != nil {
			t.Fatalf("SetCursor empty: %v", err)
		}
		tr, _ = store.GetTree(ctx, "s")
		if tr.Cursor != "" {
			t.Errorf("cursor not cleared: %q", tr.Cursor)
		}
		// Bogus cursor.
		err := store.SetCursor(ctx, "s", "tn-bogus")
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("got %v want ErrNotFound", err)
		}
	})

	t.Run("DeleteTree idempotent", func(t *testing.T) {
		store := makeStore(t)
		ctx := context.Background()
		_, _ = store.CreateTree(ctx, "s", TreeNode{Title: "Goal"})
		if err := store.DeleteTree(ctx, "s"); err != nil {
			t.Fatalf("DeleteTree: %v", err)
		}
		// Second delete is a no-op.
		if err := store.DeleteTree(ctx, "s"); err != nil {
			t.Fatalf("DeleteTree (second): %v", err)
		}
		_, err := store.GetTree(ctx, "s")
		if !errors.Is(err, ErrTreeMissing) {
			t.Errorf("got %v want ErrTreeMissing", err)
		}
	})

	t.Run("MaxNodes cap", func(t *testing.T) {
		// Use a smaller cap by saturating with shallow nodes; MaxNodes is
		// 1024 which is fast enough at unit-test scale.
		store := makeStore(t)
		ctx := context.Background()
		root, _ := store.CreateTree(ctx, "s", TreeNode{Title: "Goal"})
		for i := range MaxNodes - 1 {
			_, err := store.AddNode(ctx, "s", root.ID, TreeNode{Type: NodeFact, Title: "f"})
			if err != nil {
				t.Fatalf("AddNode #%d: %v", i, err)
			}
		}
		_, err := store.AddNode(ctx, "s", root.ID, TreeNode{Type: NodeFact, Title: "f"})
		if !errors.Is(err, ErrTreeFull) {
			t.Errorf("got %v want ErrTreeFull", err)
		}
	})

	t.Run("invalid id rejection", func(t *testing.T) {
		store := makeStore(t)
		ctx := context.Background()
		_, _ = store.CreateTree(ctx, "s", TreeNode{Title: "Goal"})
		_, err := store.AddNode(ctx, "s", "../bad", TreeNode{Type: NodeFact, Title: "f"})
		if !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("got %v want ErrInvalidArgument", err)
		}
	})

	t.Run("title length cap", func(t *testing.T) {
		store := makeStore(t)
		ctx := context.Background()
		_, err := store.CreateTree(context.Background(), "s",
			TreeNode{Title: strings.Repeat("a", TitleMaxBytes+1)})
		if !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("got %v want ErrInvalidArgument", err)
		}
		_ = ctx
	})
}
