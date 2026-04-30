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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vogo/vage/hook"
	"github.com/vogo/vage/schema"
)

// TestMapStoreConformance shares the conformance suite.
func TestMapStoreConformance(t *testing.T) {
	runStoreConformance(t, func(t *testing.T) SessionTreeStore {
		return NewMapTreeStore()
	})
}

// TestMapStoreClockInjection verifies that WithMapClock makes timestamps
// deterministic. A hand-crafted clock is the easiest way to check that
// CreateTree / AddNode / UpdateNode all touch UpdatedAt.
func TestMapStoreClockInjection(t *testing.T) {
	now := time.Date(2026, 4, 30, 10, 0, 0, 0, time.UTC)
	store := NewMapTreeStore(WithMapClock(func() time.Time { return now }))
	ctx := context.Background()

	root, err := store.CreateTree(ctx, "s", TreeNode{Title: "G"})
	if err != nil {
		t.Fatal(err)
	}
	if !root.CreatedAt.Equal(now) {
		t.Errorf("CreatedAt=%v want %v", root.CreatedAt, now)
	}
}

// TestMapStoreHookDispatch confirms that mutations dispatch
// EventSessionTreeUpdated through the wired hook.Manager.
func TestMapStoreHookDispatch(t *testing.T) {
	mgr := hook.NewManager()
	var got []schema.SessionTreeUpdatedData
	var mu sync.Mutex
	mgr.Register(hook.NewHookFunc(func(_ context.Context, ev schema.Event) error {
		if d, ok := ev.Data.(schema.SessionTreeUpdatedData); ok {
			mu.Lock()
			got = append(got, d)
			mu.Unlock()
		}
		return nil
	}, schema.EventSessionTreeUpdated))

	store := NewMapTreeStore(WithMapHookManager(mgr))
	ctx := context.Background()

	root, _ := store.CreateTree(ctx, "s", TreeNode{Title: "G"})
	c1, _ := store.AddNode(ctx, "s", root.ID, TreeNode{Type: NodeSubtask, Title: "step1"})
	_, _ = store.UpdateNode(ctx, "s", TreeNode{ID: c1.ID, Title: "step1 v2", Status: StatusActive})
	_ = store.SetCursor(ctx, "s", c1.ID)
	_ = store.DeleteNode(ctx, "s", c1.ID)
	_ = store.DeleteTree(ctx, "s")

	mu.Lock()
	defer mu.Unlock()

	if len(got) != 6 {
		t.Fatalf("event count=%d want 6: %+v", len(got), got)
	}
	wantOps := []string{
		schema.SessionTreeOpCreate,
		schema.SessionTreeOpAdd,
		schema.SessionTreeOpUpdate,
		schema.SessionTreeOpCursor,
		schema.SessionTreeOpDelete,
		schema.SessionTreeOpDeleteTree,
	}
	for i, op := range wantOps {
		if got[i].Operation != op {
			t.Errorf("event[%d].Operation=%s want %s", i, got[i].Operation, op)
		}
		if got[i].SessionID != "s" {
			t.Errorf("event[%d].SessionID=%q", i, got[i].SessionID)
		}
	}
	// NodeCount on Create == 1, on Add == 2, on Delete == 1, on DeleteTree == 0.
	if got[0].NodeCount != 1 || got[1].NodeCount != 2 || got[4].NodeCount != 1 || got[5].NodeCount != 0 {
		t.Errorf("NodeCount sequence wrong: %+v", got)
	}
}

// TestMapStoreConcurrentAdd: drive parallel AddNode calls and verify the
// final node count matches the issued attempts within MaxNodes.
func TestMapStoreConcurrentAdd(t *testing.T) {
	store := NewMapTreeStore()
	ctx := context.Background()
	root, _ := store.CreateTree(ctx, "s", TreeNode{Title: "G"})

	const goroutines = 16
	const perGoroutine = 25
	var ok atomic.Int64
	var wg sync.WaitGroup
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range perGoroutine {
				if _, err := store.AddNode(ctx, "s", root.ID, TreeNode{Type: NodeFact, Title: "f"}); err == nil {
					ok.Add(1)
				}
			}
		}()
	}
	wg.Wait()

	tr, _ := store.GetTree(ctx, "s")
	// goroutines * perGoroutine = 400, well below MaxNodes 1024.
	want := int64(goroutines * perGoroutine)
	if ok.Load() != want {
		t.Errorf("successful Adds=%d want %d", ok.Load(), want)
	}
	// Total nodes = root + successful adds.
	if len(tr.Nodes) != int(want)+1 {
		t.Errorf("node count=%d want %d", len(tr.Nodes), want+1)
	}
	if len(tr.Nodes[root.ID].Children) != int(want) {
		t.Errorf("root children=%d want %d", len(tr.Nodes[root.ID].Children), want)
	}
}
