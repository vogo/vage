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

	"github.com/vogo/vage/schema"
)

// TestStoreConformance runs the same behavioural suite against every built-in
// SessionStore implementation. New backends should add a t.Run block here.
func TestStoreConformance(t *testing.T) {
	t.Parallel()

	backends := []struct {
		name    string
		factory func(t *testing.T) SessionStore
	}{
		{
			name: "MapSessionStore",
			factory: func(*testing.T) SessionStore {
				return NewMapSessionStore()
			},
		},
		{
			name: "FileSessionStore",
			factory: func(t *testing.T) SessionStore {
				st, err := NewFileSessionStore(t.TempDir())
				if err != nil {
					t.Fatalf("file store init: %v", err)
				}
				return st
			},
		},
	}

	suite := []struct {
		name string
		fn   func(t *testing.T, store SessionStore)
	}{
		{"CreateThenGetReturnsCopy", testCreateThenGetReturnsCopy},
		{"CreateRejectsInvalidID", testCreateRejectsInvalidID},
		{"CreateRejectsDuplicate", testCreateRejectsDuplicate},
		{"GetUnknownReturnsNotFound", testGetUnknownReturnsNotFound},
		{"UpdateRefreshesUpdatedAt", testUpdateRefreshesUpdatedAt},
		{"UpdateUnknownReturnsNotFound", testUpdateUnknownReturnsNotFound},
		{"DeleteIsIdempotent", testDeleteIsIdempotent},
		{"DeleteRemovesEventsAndState", testDeleteRemovesEventsAndState},
		{"ListFiltersByUserAndAgent", testListFiltersByUserAndAgent},
		{"ListLimitOffset", testListLimitOffset},
		{"AppendEventOrderPreserved", testAppendEventOrderPreserved},
		{"AppendUnknownReturnsNotFound", testAppendUnknownReturnsNotFound},
		{"ListEventsReturnsCopy", testListEventsReturnsCopy},
		{"StateOverwriteSemantics", testStateOverwriteSemantics},
		{"StateGetMissingKey", testStateGetMissingKey},
		{"StateDeleteIsIdempotent", testStateDeleteIsIdempotent},
		{"StateOnUnknownSessionReturnsNotFound", testStateOnUnknownSessionReturnsNotFound},
		{"ConcurrentAppendNoLoss", testConcurrentAppendNoLoss},
	}

	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			for _, c := range suite {
				t.Run(c.name, func(t *testing.T) {
					t.Parallel()
					c.fn(t, b.factory(t))
				})
			}
		})
	}
}

func mustCreate(t *testing.T, store SessionStore, id, user, agent string) *Session {
	t.Helper()
	s := New(id)
	s.UserID = user
	s.AgentID = agent
	if err := store.Create(context.Background(), s); err != nil {
		t.Fatalf("create %q: %v", id, err)
	}
	return s
}

func testCreateThenGetReturnsCopy(t *testing.T, store SessionStore) {
	mustCreate(t, store, "abc", "u1", "a1")
	got, err := store.Get(context.Background(), "abc")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ID != "abc" || got.UserID != "u1" || got.AgentID != "a1" {
		t.Fatalf("unexpected: %+v", got)
	}
	if got.State != StateActive {
		t.Fatalf("expected default state Active, got %q", got.State)
	}

	// Mutating returned copy must not affect store.
	got.UserID = "tampered"
	again, _ := store.Get(context.Background(), "abc")
	if again.UserID != "u1" {
		t.Fatalf("mutation leaked: %q", again.UserID)
	}
}

func testCreateRejectsInvalidID(t *testing.T, store SessionStore) {
	cases := []struct {
		name string
		id   string
	}{
		{"slash", "a/b"},
		{"dotdot", ".."},
		{"space", "ab c"},
		{"unicode", "中文"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := &Session{ID: c.id, State: StateActive}
			err := store.Create(context.Background(), s)
			if !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("expected ErrInvalidArgument for %q, got %v", c.id, err)
			}
		})
	}

	// Empty id and oversized id.
	if err := store.Create(context.Background(), &Session{ID: ""}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument for empty id, got %v", err)
	}
	long := make([]byte, IDMaxLen+1)
	for i := range long {
		long[i] = 'a'
	}
	if err := store.Create(context.Background(), &Session{ID: string(long)}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument for oversize id, got %v", err)
	}
}

func testCreateRejectsDuplicate(t *testing.T, store SessionStore) {
	mustCreate(t, store, "dup", "", "")
	err := store.Create(context.Background(), New("dup"))
	if !errors.Is(err, ErrSessionExists) {
		t.Fatalf("expected ErrSessionExists, got %v", err)
	}
}

func testGetUnknownReturnsNotFound(t *testing.T, store SessionStore) {
	if _, err := store.Get(context.Background(), "missing"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("expected ErrSessionNotFound, got %v", err)
	}
}

func testUpdateRefreshesUpdatedAt(t *testing.T, store SessionStore) {
	s := mustCreate(t, store, "upd", "", "")
	original := s.UpdatedAt
	time.Sleep(2 * time.Millisecond)

	s.Title = "renamed"
	if err := store.Update(context.Background(), s); err != nil {
		t.Fatalf("update: %v", err)
	}

	got, _ := store.Get(context.Background(), "upd")
	if got.Title != "renamed" {
		t.Fatalf("title not persisted: %q", got.Title)
	}
	if !got.UpdatedAt.After(original) {
		t.Fatalf("UpdatedAt not refreshed: was %v, now %v", original, got.UpdatedAt)
	}
}

func testUpdateUnknownReturnsNotFound(t *testing.T, store SessionStore) {
	err := store.Update(context.Background(), New("unknown"))
	if !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("expected ErrSessionNotFound, got %v", err)
	}
}

func testDeleteIsIdempotent(t *testing.T, store SessionStore) {
	if err := store.Delete(context.Background(), "never-existed"); err != nil {
		t.Fatalf("delete missing: %v", err)
	}
	mustCreate(t, store, "to-del", "", "")
	if err := store.Delete(context.Background(), "to-del"); err != nil {
		t.Fatalf("delete first: %v", err)
	}
	if err := store.Delete(context.Background(), "to-del"); err != nil {
		t.Fatalf("delete second: %v", err)
	}
	if _, err := store.Get(context.Background(), "to-del"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("expected gone after delete, got %v", err)
	}
}

func testDeleteRemovesEventsAndState(t *testing.T, store SessionStore) {
	mustCreate(t, store, "tomb", "", "")
	if err := store.AppendEvent(context.Background(), "tomb", schema.NewEvent("x", "", "tomb", schema.AgentStartData{})); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := store.SetState(context.Background(), "tomb", "k", "v"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := store.Delete(context.Background(), "tomb"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := store.ListEvents(context.Background(), "tomb"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("events leaked: %v", err)
	}
	if _, err := store.ListState(context.Background(), "tomb"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("state leaked: %v", err)
	}
}

func testListFiltersByUserAndAgent(t *testing.T, store SessionStore) {
	mustCreate(t, store, "s1", "alice", "coder")
	mustCreate(t, store, "s2", "bob", "coder")
	mustCreate(t, store, "s3", "alice", "researcher")

	got, err := store.List(context.Background(), SessionFilter{UserID: "alice"})
	if err != nil {
		t.Fatalf("list alice: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 alice sessions, got %d", len(got))
	}

	got, _ = store.List(context.Background(), SessionFilter{UserID: "alice", AgentID: "coder"})
	if len(got) != 1 || got[0].ID != "s1" {
		t.Fatalf("expected s1, got %+v", got)
	}

	got, _ = store.List(context.Background(), SessionFilter{State: StateActive})
	if len(got) != 3 {
		t.Fatalf("expected 3 active, got %d", len(got))
	}
}

func testListLimitOffset(t *testing.T, store SessionStore) {
	for i := range 5 {
		// Use trailing digits, all valid IDs.
		mustCreate(t, store, "p"+string(rune('0'+i)), "", "")
		time.Sleep(time.Millisecond) // ensure distinct CreatedAt for FileStore ordering
	}

	got, err := store.List(context.Background(), SessionFilter{Limit: 2})
	if err != nil {
		t.Fatalf("limit: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2, got %d", len(got))
	}

	got, _ = store.List(context.Background(), SessionFilter{Offset: 3})
	if len(got) != 2 {
		t.Fatalf("expected 2 after offset, got %d", len(got))
	}

	got, _ = store.List(context.Background(), SessionFilter{Limit: 1, Offset: 2})
	if len(got) != 1 {
		t.Fatalf("expected 1, got %d", len(got))
	}
}

func testAppendEventOrderPreserved(t *testing.T, store SessionStore) {
	mustCreate(t, store, "ord", "", "")

	want := []string{"a", "b", "c", "d"}
	for _, k := range want {
		ev := schema.NewEvent(schema.EventAgentStart, "agent", "ord", schema.AgentStartData{})
		ev.AgentID = k // smuggle a label so we can identify order
		if err := store.AppendEvent(context.Background(), "ord", ev); err != nil {
			t.Fatalf("append %s: %v", k, err)
		}
	}

	got, err := store.ListEvents(context.Background(), "ord")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d events, got %d", len(want), len(got))
	}
	for i, ev := range got {
		if ev.AgentID != want[i] {
			t.Fatalf("event %d: expected %q, got %q", i, want[i], ev.AgentID)
		}
	}
}

func testAppendUnknownReturnsNotFound(t *testing.T, store SessionStore) {
	err := store.AppendEvent(context.Background(), "not-here", schema.NewEvent("x", "", "not-here", schema.AgentStartData{}))
	if !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("expected ErrSessionNotFound, got %v", err)
	}
}

func testListEventsReturnsCopy(t *testing.T, store SessionStore) {
	mustCreate(t, store, "cpy", "", "")
	if err := store.AppendEvent(context.Background(), "cpy", schema.NewEvent("e", "", "cpy", schema.AgentStartData{})); err != nil {
		t.Fatalf("append: %v", err)
	}
	got, _ := store.ListEvents(context.Background(), "cpy")
	got[0].AgentID = "tampered"

	again, _ := store.ListEvents(context.Background(), "cpy")
	if again[0].AgentID == "tampered" {
		t.Fatalf("ListEvents leaked internal slice")
	}
}

func testStateOverwriteSemantics(t *testing.T, store SessionStore) {
	mustCreate(t, store, "st", "", "")
	if err := store.SetState(context.Background(), "st", "k", "v1"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := store.SetState(context.Background(), "st", "k", "v2"); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	v, ok, _ := store.GetState(context.Background(), "st", "k")
	if !ok || v != "v2" {
		t.Fatalf("expected v2, got %v ok=%v", v, ok)
	}

	all, _ := store.ListState(context.Background(), "st")
	if len(all) != 1 || all["k"] != "v2" {
		t.Fatalf("ListState mismatch: %+v", all)
	}
}

func testStateGetMissingKey(t *testing.T, store SessionStore) {
	mustCreate(t, store, "miss", "", "")
	v, ok, err := store.GetState(context.Background(), "miss", "nope")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if ok || v != nil {
		t.Fatalf("expected (nil,false), got (%v,%v)", v, ok)
	}
}

func testStateDeleteIsIdempotent(t *testing.T, store SessionStore) {
	mustCreate(t, store, "del", "", "")
	if err := store.DeleteState(context.Background(), "del", "absent"); err != nil {
		t.Fatalf("delete absent: %v", err)
	}
	if err := store.SetState(context.Background(), "del", "k", "v"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := store.DeleteState(context.Background(), "del", "k"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := store.DeleteState(context.Background(), "del", "k"); err != nil {
		t.Fatalf("re-delete: %v", err)
	}
}

func testStateOnUnknownSessionReturnsNotFound(t *testing.T, store SessionStore) {
	cases := []struct {
		name string
		fn   func() error
	}{
		{"GetState", func() error { _, _, err := store.GetState(context.Background(), "x", "k"); return err }},
		{"SetState", func() error { return store.SetState(context.Background(), "x", "k", "v") }},
		{"DeleteState", func() error { return store.DeleteState(context.Background(), "x", "k") }},
		{"ListState", func() error { _, err := store.ListState(context.Background(), "x"); return err }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.fn(); !errors.Is(err, ErrSessionNotFound) {
				t.Fatalf("expected ErrSessionNotFound, got %v", err)
			}
		})
	}
}

func testConcurrentAppendNoLoss(t *testing.T, store SessionStore) {
	mustCreate(t, store, "race", "", "")

	const writers = 8
	const perWriter = 50
	var wg sync.WaitGroup
	wg.Add(writers)
	for range writers {
		go func() {
			defer wg.Done()
			for range perWriter {
				ev := schema.NewEvent(schema.EventAgentStart, "", "race", schema.AgentStartData{})
				if err := store.AppendEvent(context.Background(), "race", ev); err != nil {
					t.Errorf("append: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	got, err := store.ListEvents(context.Background(), "race")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != writers*perWriter {
		t.Fatalf("expected %d events, got %d", writers*perWriter, len(got))
	}
}
