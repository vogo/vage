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

package session

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// fixedClock returns a clock that advances by 1 second on each tick.
// Each test gets its own instance so cross-test calls do not interfere.
type fixedClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFixedClock(start time.Time) *fixedClock {
	return &fixedClock{now: start}
}

func (c *fixedClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := c.now
	c.now = c.now.Add(time.Second)
	return t
}

// TestSessionMetrics_FirstUpdate_BootstrapsRecord verifies the
// "no record yet" → fresh metrics path. fn must see zero counters AND
// the store must stamp SessionID + FirstSeen so callers never have to
// special-case the first vs Nth update.
func TestSessionMetrics_FirstUpdate_BootstrapsRecord(t *testing.T) {
	clk := newFixedClock(time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC))
	store := NewMapMetricsStore().WithClock(clk.Now)

	var seen *SessionMetrics
	err := store.Update(context.Background(), "sid-bootstrap", func(m *SessionMetrics) {
		seen = m
		m.PromptTokens = 5
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	if seen == nil {
		t.Fatal("fn never invoked")
	}
	// fn must observe a non-nil record with zero counters and SessionID
	// already populated by the store bootstrap.
	if seen.SessionID != "sid-bootstrap" {
		t.Errorf("fn saw SessionID=%q, want sid-bootstrap (store-populated)", seen.SessionID)
	}
	if seen.CompletionTokens != 0 || seen.ResumeCount != 0 {
		t.Errorf("fn saw non-zero counters on first update: %+v", seen)
	}

	got, err := store.Get(context.Background(), "sid-bootstrap")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.PromptTokens != 5 {
		t.Errorf("PromptTokens = %d, want 5", got.PromptTokens)
	}
	if got.FirstSeen.IsZero() {
		t.Error("FirstSeen still zero after first Update")
	}
	if got.LastUpdated.IsZero() {
		t.Error("LastUpdated still zero after first Update")
	}
}

// TestSessionMetrics_AccumulatesAcrossUpdates checks that counters
// monotonically increase and that LastUpdated advances while FirstSeen
// stays pinned to the first write.
func TestSessionMetrics_AccumulatesAcrossUpdates(t *testing.T) {
	clk := newFixedClock(time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC))
	store := NewMapMetricsStore().WithClock(clk.Now)

	for range 3 {
		err := store.Update(context.Background(), "sid-accum", func(m *SessionMetrics) {
			m.PromptTokens += 7
			m.CompletionTokens += 3
			m.CostUSD += 0.001
			m.ContextEdits++
		})
		if err != nil {
			t.Fatalf("Update: %v", err)
		}
	}

	got, err := store.Get(context.Background(), "sid-accum")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.PromptTokens != 21 || got.CompletionTokens != 9 {
		t.Errorf("counters drift: %+v", got)
	}
	if got.TotalTokens != 30 {
		t.Errorf("TotalTokens = %d, want 30 (auto-derived)", got.TotalTokens)
	}
	if got.ContextEdits != 3 {
		t.Errorf("ContextEdits = %d, want 3", got.ContextEdits)
	}
	if got.CostUSD < 0.0029 || got.CostUSD > 0.0031 {
		t.Errorf("CostUSD = %f, want ≈0.003", got.CostUSD)
	}
	// First write happened at the seed time; the third advanced two
	// ticks. FirstSeen must remain the seed.
	wantFirst := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	if !got.FirstSeen.Equal(wantFirst) {
		t.Errorf("FirstSeen = %v, want %v", got.FirstSeen, wantFirst)
	}
	if !got.LastUpdated.After(wantFirst) {
		t.Errorf("LastUpdated %v not advanced past FirstSeen %v", got.LastUpdated, wantFirst)
	}
}

// TestSessionMetrics_TotalTokensAutoDerived guards the invariant that
// TotalTokens = PromptTokens + CompletionTokens regardless of whether
// fn touches the field. A hook that updates only the two underlying
// counters must not leave Total stale.
func TestSessionMetrics_TotalTokensAutoDerived(t *testing.T) {
	store := NewMapMetricsStore()

	// fn intentionally writes Total to a wrong value to confirm the
	// store overrides it.
	err := store.Update(context.Background(), "sid-total", func(m *SessionMetrics) {
		m.PromptTokens = 10
		m.CompletionTokens = 4
		m.TotalTokens = 999 // should be replaced by 14
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, _ := store.Get(context.Background(), "sid-total")
	if got.TotalTokens != 14 {
		t.Errorf("TotalTokens = %d, want 14 (auto-derived overrides fn)", got.TotalTokens)
	}
}

// TestSessionMetrics_NoOpUpdate_StillStampsLastUpdated covers the
// pattern where the hook decides nothing changed but the caller still
// wants to record activity. A fn that no-ops must still bump
// LastUpdated and create a record on first call.
func TestSessionMetrics_NoOpUpdate_StillStampsLastUpdated(t *testing.T) {
	clk := newFixedClock(time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC))
	store := NewMapMetricsStore().WithClock(clk.Now)

	if err := store.Update(context.Background(), "sid-noop", func(m *SessionMetrics) {}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, err := store.Get(context.Background(), "sid-noop")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.LastUpdated.IsZero() {
		t.Error("no-op fn left LastUpdated zero")
	}
}

// TestSessionMetrics_NilFn allows callers to materialise a record
// without mutating it (e.g., to create the file early so a follow-up
// Get returns a zero document instead of ErrMetricsNotFound).
func TestSessionMetrics_NilFn(t *testing.T) {
	store := NewMapMetricsStore()
	if err := store.Update(context.Background(), "sid-nilfn", nil); err != nil {
		t.Fatalf("Update(nil fn): %v", err)
	}
	got, err := store.Get(context.Background(), "sid-nilfn")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.SessionID != "sid-nilfn" {
		t.Errorf("SessionID = %q", got.SessionID)
	}
}

// TestSessionMetrics_GetMissing returns ErrMetricsNotFound for a
// session that has never been Updated. Callers map this to "zero
// counters" or 404 depending on transport.
func TestSessionMetrics_GetMissing(t *testing.T) {
	store := NewMapMetricsStore()
	_, err := store.Get(context.Background(), "ghost-sid")
	if !errors.Is(err, ErrMetricsNotFound) {
		t.Fatalf("err = %v, want ErrMetricsNotFound", err)
	}
}

// TestSessionMetrics_Delete_Idempotent guards the Resume-cleanup path:
// callers should be able to Delete unconditionally without tripping
// over a missing record.
func TestSessionMetrics_Delete_Idempotent(t *testing.T) {
	store := NewMapMetricsStore()
	if err := store.Delete(context.Background(), "ghost-sid"); err != nil {
		t.Fatalf("delete missing: %v", err)
	}

	if err := store.Update(context.Background(), "real-sid", func(m *SessionMetrics) {
		m.ResumeCount = 1
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if err := store.Delete(context.Background(), "real-sid"); err != nil {
		t.Fatalf("delete real: %v", err)
	}
	if _, err := store.Get(context.Background(), "real-sid"); !errors.Is(err, ErrMetricsNotFound) {
		t.Fatalf("Get after Delete: err = %v, want ErrMetricsNotFound", err)
	}
}

// TestSessionMetrics_InvalidID forces every method through validateID
// to confirm the store rejects malformed ids consistently.
func TestSessionMetrics_InvalidID(t *testing.T) {
	store := NewMapMetricsStore()

	if err := store.Update(context.Background(), "", func(*SessionMetrics) {}); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("Update empty id: err = %v, want ErrInvalidArgument", err)
	}
	if _, err := store.Get(context.Background(), "bad/id"); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("Get bad id: err = %v, want ErrInvalidArgument", err)
	}
	if err := store.Delete(context.Background(), "bad id"); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("Delete bad id: err = %v, want ErrInvalidArgument", err)
	}
}

// TestSessionMetrics_ContextCancelled ensures every method respects
// context cancellation before performing IO. Implementations consult
// ctx.Err() at the top.
func TestSessionMetrics_ContextCancelled(t *testing.T) {
	store := NewMapMetricsStore()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := store.Update(ctx, "sid-ctx", func(*SessionMetrics) {}); !errors.Is(err, context.Canceled) {
		t.Errorf("Update: err = %v, want context.Canceled", err)
	}
	if _, err := store.Get(ctx, "sid-ctx"); !errors.Is(err, context.Canceled) {
		t.Errorf("Get: err = %v, want context.Canceled", err)
	}
	if err := store.Delete(ctx, "sid-ctx"); !errors.Is(err, context.Canceled) {
		t.Errorf("Delete: err = %v, want context.Canceled", err)
	}
}

// TestSessionMetrics_GetReturnsCopy ensures consumers cannot scribble
// over the in-memory record by mutating a returned snapshot.
func TestSessionMetrics_GetReturnsCopy(t *testing.T) {
	store := NewMapMetricsStore()
	if err := store.Update(context.Background(), "sid-iso", func(m *SessionMetrics) {
		m.PromptTokens = 100
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, _ := store.Get(context.Background(), "sid-iso")
	got.PromptTokens = 999

	fresh, _ := store.Get(context.Background(), "sid-iso")
	if fresh.PromptTokens != 100 {
		t.Errorf("Get returned mutable handle: stored = %d", fresh.PromptTokens)
	}
}

// Concurrency smoke: many goroutines each adding to the same counter
// must end at exactly N — proves Update serialises the closure body.
func TestSessionMetrics_ConcurrentUpdatesSerialised(t *testing.T) {
	store := NewMapMetricsStore()

	const N = 200
	var wg sync.WaitGroup
	wg.Add(N)
	for range N {
		go func() {
			defer wg.Done()
			_ = store.Update(context.Background(), "sid-concurrent", func(m *SessionMetrics) {
				m.PromptTokens++
			})
		}()
	}
	wg.Wait()

	got, _ := store.Get(context.Background(), "sid-concurrent")
	if got.PromptTokens != N {
		t.Errorf("PromptTokens = %d, want %d (race)", got.PromptTokens, N)
	}
}
