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

// TestMapStorePromoteNode_HappyPath drives the synchronous PromoteNode
// path: parent gets a new summary, every eligible child flips Promoted,
// and the dispatched events appear in order.
func TestMapStorePromoteNode_HappyPath(t *testing.T) {
	mgr := hook.NewManager()
	var events []string
	var emu sync.Mutex
	mgr.Register(hook.NewHookFunc(func(_ context.Context, ev schema.Event) error {
		emu.Lock()
		events = append(events, ev.Type)
		emu.Unlock()
		return nil
	},
		schema.EventSessionTreePromotionStarted,
		schema.EventSessionTreePromotionCompleted,
		schema.EventSessionTreePromotionFailed))

	store := NewMapTreeStore(
		WithMapHookManager(mgr),
		WithMapPromoter(PromoteFunc(func(_ context.Context, _ *TreeNode, _ []*TreeNode) (string, error) {
			return "rolled-up", nil
		})),
	)
	ctx := context.Background()

	root, _ := store.CreateTree(ctx, "s", TreeNode{Title: "Goal"})
	c1, _ := store.AddNode(ctx, "s", root.ID, TreeNode{Type: NodeFact, Title: "f1"})
	_, _ = store.AddNode(ctx, "s", root.ID, TreeNode{Type: NodeFact, Title: "f2"})

	updated, err := store.PromoteNode(ctx, "s", root.ID)
	if err != nil {
		t.Fatalf("PromoteNode: %v", err)
	}
	if updated.Summary != "rolled-up" {
		t.Errorf("parent.Summary=%q want rolled-up", updated.Summary)
	}
	if updated.Metadata[SummarySourceMetaKey] != SummarySourcePromotion {
		t.Errorf("parent missing summary_source meta: %+v", updated.Metadata)
	}

	tr, _ := store.GetTree(ctx, "s")
	child := tr.Nodes[c1.ID]
	if !child.Promoted {
		t.Error("child still not promoted")
	}
	if child.PromotedAt.IsZero() {
		t.Error("child PromotedAt not set")
	}
	if child.Metadata[SummarySourceMetaKey] != SummarySourcePromotion {
		t.Errorf("child meta wrong: %+v", child.Metadata)
	}

	emu.Lock()
	defer emu.Unlock()
	if len(events) != 2 || events[0] != schema.EventSessionTreePromotionStarted ||
		events[1] != schema.EventSessionTreePromotionCompleted {
		t.Errorf("events=%v want [started, completed]", events)
	}
}

// TestMapStorePromoteNode_SkipsPinnedAndPromoted ensures the eligibility
// filter excludes Pinned and already-Promoted children. The lone eligible
// child triggers the work, but the others remain untouched.
func TestMapStorePromoteNode_SkipsPinnedAndPromoted(t *testing.T) {
	store := NewMapTreeStore(
		WithMapPromoter(PromoteFunc(func(_ context.Context, _ *TreeNode, c []*TreeNode) (string, error) {
			if len(c) != 1 || c[0].Title != "live" {
				t.Errorf("eligible children passed in = %+v", c)
			}
			return "rolled", nil
		})),
	)
	ctx := context.Background()
	root, _ := store.CreateTree(ctx, "s", TreeNode{Title: "Goal"})
	pinned, _ := store.AddNode(ctx, "s", root.ID, TreeNode{Type: NodeFact, Title: "pinned-c"})
	_, _ = store.UpdateNode(ctx, "s", TreeNode{ID: pinned.ID, Title: pinned.Title, Pinned: true})
	prePromoted, _ := store.AddNode(ctx, "s", root.ID, TreeNode{Type: NodeFact, Title: "old"})
	_, _ = store.UpdateNode(ctx, "s", TreeNode{ID: prePromoted.ID, Title: prePromoted.Title, Promoted: true})
	_, _ = store.AddNode(ctx, "s", root.ID, TreeNode{Type: NodeFact, Title: "live"})

	if _, err := store.PromoteNode(ctx, "s", root.ID); err != nil {
		t.Fatalf("PromoteNode: %v", err)
	}
	tr, _ := store.GetTree(ctx, "s")
	if !tr.Nodes[pinned.ID].Pinned {
		t.Error("pinned changed")
	}
	if tr.Nodes[pinned.ID].Promoted {
		t.Error("pinned was folded")
	}
	if !tr.Nodes[prePromoted.ID].Promoted {
		t.Error("pre-promoted lost its flag")
	}
}

// TestMapStorePromoteNode_NoEligible verifies the no-op short-circuit:
// no Promoter call, no event dispatch, parent unchanged.
func TestMapStorePromoteNode_NoEligible(t *testing.T) {
	calls := 0
	mgr := hook.NewManager()
	var events int
	mgr.Register(hook.NewHookFunc(func(_ context.Context, _ schema.Event) error {
		events++
		return nil
	},
		schema.EventSessionTreePromotionStarted,
		schema.EventSessionTreePromotionCompleted,
		schema.EventSessionTreePromotionFailed))

	store := NewMapTreeStore(
		WithMapHookManager(mgr),
		WithMapPromoter(PromoteFunc(func(_ context.Context, _ *TreeNode, _ []*TreeNode) (string, error) {
			calls++
			return "should not run", nil
		})),
	)
	ctx := context.Background()
	root, _ := store.CreateTree(ctx, "s", TreeNode{Title: "Goal"})

	updated, err := store.PromoteNode(ctx, "s", root.ID)
	if err != nil {
		t.Fatalf("PromoteNode: %v", err)
	}
	if updated.Summary != "" {
		t.Errorf("Summary changed: %q", updated.Summary)
	}
	if calls != 0 {
		t.Errorf("Promoter called %d times want 0", calls)
	}
	if events != 0 {
		t.Errorf("events=%d want 0", events)
	}
}

// TestMapStorePromoteNode_PromoterError surfaces the error and keeps the
// store untouched; a Failed event is dispatched.
func TestMapStorePromoteNode_PromoterError(t *testing.T) {
	mgr := hook.NewManager()
	var got []string
	var mu sync.Mutex
	mgr.Register(hook.NewHookFunc(func(_ context.Context, ev schema.Event) error {
		mu.Lock()
		got = append(got, ev.Type)
		mu.Unlock()
		return nil
	},
		schema.EventSessionTreePromotionStarted,
		schema.EventSessionTreePromotionCompleted,
		schema.EventSessionTreePromotionFailed))

	wantErr := errors.New("llm down")
	store := NewMapTreeStore(
		WithMapHookManager(mgr),
		WithMapPromoter(PromoteFunc(func(_ context.Context, _ *TreeNode, _ []*TreeNode) (string, error) {
			return "", wantErr
		})),
	)
	ctx := context.Background()
	root, _ := store.CreateTree(ctx, "s", TreeNode{Title: "Goal"})
	c1, _ := store.AddNode(ctx, "s", root.ID, TreeNode{Type: NodeFact, Title: "c1"})

	if _, err := store.PromoteNode(ctx, "s", root.ID); !errors.Is(err, wantErr) {
		t.Fatalf("got %v want chain", err)
	}

	tr, _ := store.GetTree(ctx, "s")
	if tr.Nodes[c1.ID].Promoted {
		t.Error("child promoted despite Promoter error")
	}
	if tr.Nodes[root.ID].Summary != "" {
		t.Error("parent summary changed despite Promoter error")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 || got[0] != schema.EventSessionTreePromotionStarted ||
		got[1] != schema.EventSessionTreePromotionFailed {
		t.Errorf("events=%v want [started, failed]", got)
	}
}

// TestMapStorePromoteNode_RaceDrainsEligible exercises the second-phase race
// path: the Promoter is invoked with N children, but by the time we re-
// acquire the write lock another writer has already promoted them all.
// applyPromotion must leave the parent untouched in that case (no Summary
// rewrite, no Metadata stamp), and Completed still fires with FoldedCount=0
// to keep the Started/Completed pair invariant.
func TestMapStorePromoteNode_RaceDrainsEligible(t *testing.T) {
	mgr := hook.NewManager()
	var events []schema.Event
	var emu sync.Mutex
	mgr.Register(hook.NewHookFunc(func(_ context.Context, ev schema.Event) error {
		emu.Lock()
		events = append(events, ev)
		emu.Unlock()
		return nil
	},
		schema.EventSessionTreePromotionStarted,
		schema.EventSessionTreePromotionCompleted,
		schema.EventSessionTreePromotionFailed))

	// The Promoter call is the seam where another writer races in: we use
	// the call as a signal to flip every child to Promoted=true via direct
	// UpdateNode before the second-phase commit re-runs.
	var store *MapTreeStore
	var rootID, c1ID, c2ID string
	racer := PromoteFunc(func(_ context.Context, _ *TreeNode, _ []*TreeNode) (string, error) {
		_, _ = store.UpdateNode(context.Background(), "s",
			TreeNode{ID: c1ID, Title: "c1", Promoted: true})
		_, _ = store.UpdateNode(context.Background(), "s",
			TreeNode{ID: c2ID, Title: "c2", Promoted: true})
		return "rolled-up", nil
	})

	store = NewMapTreeStore(WithMapHookManager(mgr), WithMapPromoter(racer))
	ctx := context.Background()
	root, _ := store.CreateTree(ctx, "s", TreeNode{Title: "Goal"})
	rootID = root.ID
	c1, _ := store.AddNode(ctx, "s", rootID, TreeNode{Type: NodeFact, Title: "c1"})
	c2, _ := store.AddNode(ctx, "s", rootID, TreeNode{Type: NodeFact, Title: "c2"})
	c1ID = c1.ID
	c2ID = c2.ID

	updated, err := store.PromoteNode(ctx, "s", rootID)
	if err != nil {
		t.Fatalf("PromoteNode: %v", err)
	}
	// Parent must be unchanged: empty Summary, no summary_source.
	if updated.Summary != "" {
		t.Errorf("parent.Summary=%q; expected unchanged ''", updated.Summary)
	}
	if v, ok := updated.Metadata[SummarySourceMetaKey]; ok {
		t.Errorf("parent stamped summary_source=%v despite folded==0", v)
	}

	emu.Lock()
	defer emu.Unlock()
	if len(events) != 2 {
		t.Fatalf("events=%d want 2 (started + completed)", len(events))
	}
	if events[0].Type != schema.EventSessionTreePromotionStarted ||
		events[1].Type != schema.EventSessionTreePromotionCompleted {
		t.Errorf("event types=%v want [started, completed]",
			[]string{events[0].Type, events[1].Type})
	}
	completed, ok := events[1].Data.(schema.SessionTreePromotionCompletedData)
	if !ok {
		t.Fatalf("Completed payload type=%T", events[1].Data)
	}
	if completed.FoldedCount != 0 {
		t.Errorf("Completed.FoldedCount=%d want 0 (race drained the set)", completed.FoldedCount)
	}
}

// TestMapStoreAutoTrigger drives the AddNode → decider → PromoteNode chain
// with a synchronous async runner so the test does not have to wait on
// goroutine scheduling.
func TestMapStoreAutoTrigger(t *testing.T) {
	var promoterCalls atomic.Int32
	store := NewMapTreeStore(
		WithMapPromoter(PromoteFunc(func(_ context.Context, _ *TreeNode, _ []*TreeNode) (string, error) {
			promoterCalls.Add(1)
			return "auto", nil
		})),
		WithMapPromotionDecider(ChildrenCountDecider{Min: 2}),
		WithMapPromotionAsync(func(fn func()) { fn() }), // synchronous for determinism
	)
	ctx := context.Background()
	root, _ := store.CreateTree(ctx, "s", TreeNode{Title: "Goal"})
	_, _ = store.AddNode(ctx, "s", root.ID, TreeNode{Type: NodeFact, Title: "c1"})
	if got := promoterCalls.Load(); got != 0 {
		t.Errorf("after first add: promoterCalls=%d want 0", got)
	}
	_, _ = store.AddNode(ctx, "s", root.ID, TreeNode{Type: NodeFact, Title: "c2"})
	if got := promoterCalls.Load(); got != 1 {
		t.Errorf("after second add: promoterCalls=%d want 1", got)
	}
}

// TestMapStoreAutoTrigger_SingleflightDrops verifies that overlapping
// triggers for the same parent collapse to a single PromoteNode run.
// We use a manual async runner to control the ordering: the first AddNode
// launches a "long-running" promoter and we observe that subsequent
// triggers are dropped entirely.
func TestMapStoreAutoTrigger_SingleflightDrops(t *testing.T) {
	var promoterCalls atomic.Int32
	release := make(chan struct{})

	// queuedRunner stores fn instead of running it, so we can release
	// the runner on demand and observe overlapping triggers.
	var queue []func()
	var qmu sync.Mutex

	store := NewMapTreeStore(
		WithMapPromoter(PromoteFunc(func(_ context.Context, _ *TreeNode, _ []*TreeNode) (string, error) {
			promoterCalls.Add(1)
			<-release
			return "rolled", nil
		})),
		WithMapPromotionDecider(ChildrenCountDecider{Min: 2}),
		WithMapPromotionAsync(func(fn func()) {
			qmu.Lock()
			queue = append(queue, fn)
			qmu.Unlock()
		}),
	)
	ctx := context.Background()
	root, _ := store.CreateTree(ctx, "s", TreeNode{Title: "Goal"})

	// Threshold met after 2 children — but we will add 5 to try to enqueue
	// many promotions.
	for range 5 {
		_, _ = store.AddNode(ctx, "s", root.ID, TreeNode{Type: NodeFact, Title: "c"})
	}

	qmu.Lock()
	queued := len(queue)
	jobs := append([]func(){}, queue...)
	queue = queue[:0]
	qmu.Unlock()

	// Triggers fire 4 times (children 2..5) but the first reservation
	// owns the slot until the promoter returns. The other queued jobs
	// were rejected during reserve and never enqueued.
	if queued != 1 {
		t.Errorf("queued=%d want 1 (only the first trigger should be queued)", queued)
	}

	// Run the queued job in its own goroutine, then release the promoter.
	done := make(chan struct{})
	go func() {
		jobs[0]()
		close(done)
	}()
	close(release)
	<-done

	if got := promoterCalls.Load(); got != 1 {
		t.Errorf("promoterCalls=%d want 1", got)
	}

	// After the slot releases, fresh adds should be able to trigger again.
	// The earlier promoter run flipped all 5 original children to
	// Promoted=true, so two new children are required to clear the
	// ChildrenCountDecider{Min: 2} threshold.
	_, _ = store.AddNode(ctx, "s", root.ID, TreeNode{Type: NodeFact, Title: "post-1"})
	_, _ = store.AddNode(ctx, "s", root.ID, TreeNode{Type: NodeFact, Title: "post-2"})
	qmu.Lock()
	if len(queue) != 1 {
		t.Errorf("post-release queued=%d want 1", len(queue))
	}
	qmu.Unlock()
}

// TestMapStoreAutoTrigger_UpdateNode verifies that UpdateNode fires the
// decider for the *parent* of the updated node.
func TestMapStoreAutoTrigger_UpdateNode(t *testing.T) {
	var triggered atomic.Bool
	store := NewMapTreeStore(
		WithMapPromoter(PromoteFunc(func(_ context.Context, _ *TreeNode, _ []*TreeNode) (string, error) {
			triggered.Store(true)
			return "rolled", nil
		})),
		WithMapPromotionDecider(AllChildrenDoneDecider{}),
		WithMapPromotionAsync(func(fn func()) { fn() }),
	)
	ctx := context.Background()
	root, _ := store.CreateTree(ctx, "s", TreeNode{Title: "Goal"})
	c1, _ := store.AddNode(ctx, "s", root.ID, TreeNode{Type: NodeSubtask, Title: "c1"})

	if triggered.Load() {
		t.Fatal("trigger fired on Add of pending child")
	}
	_, _ = store.UpdateNode(ctx, "s", TreeNode{ID: c1.ID, Title: c1.Title, Status: StatusDone})
	if !triggered.Load() {
		t.Error("update to done did not fire decider")
	}
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
		wg.Go(func() {
			for range perGoroutine {
				if _, err := store.AddNode(ctx, "s", root.ID, TreeNode{Type: NodeFact, Title: "f"}); err == nil {
					ok.Add(1)
				}
			}
		})
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
