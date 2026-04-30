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
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vogo/vage/hook"
	"github.com/vogo/vage/schema"
)

// reentrantHookManager wires a sync hook that calls back into the store
// once. It exists to confirm dispatch runs outside the per-session lock.
type reentrantHookManager struct {
	t         *testing.T
	manager   *hook.Manager
	store     atomic.Pointer[FileTreeStore]
	callbacks atomic.Int64
}

func newReentrantHookManager(t *testing.T) *reentrantHookManager {
	t.Helper()
	r := &reentrantHookManager{t: t, manager: hook.NewManager()}
	r.manager.Register(hook.NewHookFunc(func(ctx context.Context, ev schema.Event) error {
		s := r.store.Load()
		if s == nil {
			return nil
		}
		// Only react to the first event to avoid recursive amplification.
		if r.callbacks.Add(1) > 1 {
			return nil
		}
		// Re-enter on the same session; this would deadlock if dispatch
		// were holding the per-session mutex.
		_ = s.SetCursor(ctx, ev.SessionID, "")
		return nil
	}, schema.EventSessionTreeUpdated))
	return r
}

func (r *reentrantHookManager) bind(s *FileTreeStore) { r.store.Store(s) }

// TestFileStoreConformance runs the shared scenario suite against
// FileTreeStore.
func TestFileStoreConformance(t *testing.T) {
	runStoreConformance(t, func(t *testing.T) SessionTreeStore {
		dir := t.TempDir()
		store, err := NewFileTreeStore(dir)
		if err != nil {
			t.Fatal(err)
		}
		return store
	})
}

// TestFileStoreLayout verifies the on-disk layout matches the documented
// `<root>/<sessionID>/tree/tree.json` shape.
func TestFileStoreLayout(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileTreeStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := store.CreateTree(ctx, "s", TreeNode{Title: "G"}); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "s", "tree", "tree.json")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("tree.json missing: %v", err)
	}
	if got := store.PathOf("s"); got != filepath.Join(dir, "s", "tree") {
		t.Errorf("PathOf=%q want %q", got, filepath.Join(dir, "s", "tree"))
	}
}

// TestFileStorePersistence: writes are visible through a fresh store
// instance, demonstrating that on-disk data is the source of truth.
func TestFileStorePersistence(t *testing.T) {
	dir := t.TempDir()
	storeA, _ := NewFileTreeStore(dir)
	ctx := context.Background()
	root, err := storeA.CreateTree(ctx, "s", TreeNode{Title: "G"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storeA.AddNode(ctx, "s", root.ID, TreeNode{Type: NodeSubtask, Title: "step1"}); err != nil {
		t.Fatal(err)
	}

	storeB, _ := NewFileTreeStore(dir)
	tr, err := storeB.GetTree(ctx, "s")
	if err != nil {
		t.Fatalf("storeB GetTree: %v", err)
	}
	if len(tr.Nodes) != 2 {
		t.Errorf("nodes=%d want 2", len(tr.Nodes))
	}
}

// TestFileStoreCorruptedJSON: a tampered tree.json surfaces as an error
// (not ErrTreeMissing) so callers can distinguish "absent" from "broken".
// The vctx.SessionTreeSource maps both into fail-open.
func TestFileStoreCorruptedJSON(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewFileTreeStore(dir)
	ctx := context.Background()
	if _, err := store.CreateTree(ctx, "s", TreeNode{Title: "G"}); err != nil {
		t.Fatal(err)
	}

	// Corrupt the file.
	path := filepath.Join(dir, "s", "tree", "tree.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := store.GetTree(ctx, "s")
	if err == nil {
		t.Fatal("expected error on corrupted JSON")
	}
	if errors.Is(err, ErrTreeMissing) {
		t.Errorf("got ErrTreeMissing, want a separate decode error")
	}
}

// TestFileStoreInvalidIDDelete: invalid session ids are silently treated
// as "no-op" so DeleteTree stays safe to call from cleanup loops.
func TestFileStoreInvalidIDDelete(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewFileTreeStore(dir)
	if err := store.DeleteTree(context.Background(), ".."); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

// TestFileStoreEmptyRoot: empty root rejects with a clear error.
func TestFileStoreEmptyRoot(t *testing.T) {
	if _, err := NewFileTreeStore(""); err == nil {
		t.Error("expected error on empty root")
	}
}

// TestFileStoreSyncHookReentrant guards the invariant that a synchronous
// hook callback may call back into the store on the same session without
// deadlocking on the per-session mutex. Hooks dispatch must run after the
// lock is released.
func TestFileStoreSyncHookReentrant(t *testing.T) {
	dir := t.TempDir()

	// Build the hook before the store so we can capture the store pointer
	// inside the hook closure once the store is constructed.
	mgr := newReentrantHookManager(t)
	store, err := NewFileTreeStore(dir, WithFileHookManager(mgr.manager))
	if err != nil {
		t.Fatal(err)
	}
	mgr.bind(store)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// CreateTree dispatches "create"; the hook will issue a follow-up
	// SetCursor on the same session. If dispatch happened under the lock,
	// SetCursor would deadlock and the timeout would fire.
	if _, err := store.CreateTree(ctx, "s", TreeNode{Title: "G"}); err != nil {
		t.Fatalf("CreateTree: %v", err)
	}

	if got := mgr.callbacks.Load(); got == 0 {
		t.Errorf("expected at least one re-entrant callback; got %d", got)
	}
}
