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

package todo

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func item(content, active string, status Status) Item {
	return Item{Content: content, ActiveForm: active, Status: status}
}

func TestApply_EmptySession(t *testing.T) {
	s := NewStore()
	if _, err := s.Apply("", []Item{}); err == nil {
		t.Fatal("expected error for empty sessionID")
	}
}

func TestApply_NilAndEmpty_SameSemantics(t *testing.T) {
	s := NewStore()

	snap1, err := s.Apply("sess", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if snap1.Version != 1 || len(snap1.Items) != 0 {
		t.Fatalf("nil items: version=%d items=%d", snap1.Version, len(snap1.Items))
	}

	snap2, err := s.Apply("sess", []Item{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if snap2.Version != 2 || len(snap2.Items) != 0 {
		t.Fatalf("empty slice items: version=%d items=%d", snap2.Version, len(snap2.Items))
	}
}

func TestApply_AssignsIDs(t *testing.T) {
	s := NewStore()
	snap, err := s.Apply("sess", []Item{
		item("Read code", "Reading code", StatusPending),
		item("Edit code", "Editing code", StatusPending),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if snap.Version != 1 {
		t.Fatalf("expected version 1, got %d", snap.Version)
	}
	if len(snap.Items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(snap.Items))
	}
	if snap.Items[0].ID == "" || snap.Items[1].ID == "" {
		t.Fatalf("expected auto-assigned ids: %+v", snap.Items)
	}
	if snap.Items[0].ID == snap.Items[1].ID {
		t.Fatalf("expected distinct ids, got %q twice", snap.Items[0].ID)
	}
}

func TestApply_ReusesExistingIDs(t *testing.T) {
	s := NewStore()
	first, err := s.Apply("sess", []Item{item("A", "Doing A", StatusPending)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	idA := first.Items[0].ID

	second, err := s.Apply("sess", []Item{
		{ID: idA, Content: "A", ActiveForm: "Doing A", Status: StatusInProgress},
		item("B", "Doing B", StatusPending),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if second.Version != 2 {
		t.Fatalf("expected version 2, got %d", second.Version)
	}
	if second.Items[0].ID != idA {
		t.Fatalf("expected id %q reused, got %q", idA, second.Items[0].ID)
	}
	if second.Items[1].ID == "" || second.Items[1].ID == idA {
		t.Fatalf("expected new id for B, got %q", second.Items[1].ID)
	}
}

func TestApply_IgnoresClientIDsNotMatchingPrior(t *testing.T) {
	s := NewStore()
	snap, err := s.Apply("sess", []Item{
		{ID: "made-up", Content: "X", ActiveForm: "Doing X", Status: StatusPending},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if snap.Items[0].ID == "made-up" {
		t.Fatalf("expected server-assigned id, got %q", snap.Items[0].ID)
	}
	if !strings.HasPrefix(snap.Items[0].ID, "todo_") {
		t.Fatalf("expected todo_ prefix, got %q", snap.Items[0].ID)
	}
}

func TestApply_RejectsMultipleInProgress(t *testing.T) {
	s := NewStore()
	_, err := s.Apply("sess", []Item{
		item("A", "Doing A", StatusInProgress),
		item("B", "Doing B", StatusInProgress),
	})
	if !errors.Is(err, ErrTooManyInProgress) {
		t.Fatalf("expected ErrTooManyInProgress, got %v", err)
	}
	if got := s.Get("sess"); got.Version != 0 {
		t.Fatalf("store must remain untouched after rejection, got version %d", got.Version)
	}
}

func TestApply_RejectsInvalidStatus(t *testing.T) {
	s := NewStore()
	_, err := s.Apply("sess", []Item{
		{Content: "X", ActiveForm: "Doing X", Status: Status("done")},
	})
	if !errors.Is(err, ErrInvalidStatus) {
		t.Fatalf("expected ErrInvalidStatus, got %v", err)
	}
}

func TestApply_RejectsEmptyContentOrActiveForm(t *testing.T) {
	s := NewStore()
	_, err := s.Apply("sess", []Item{{Content: "", ActiveForm: "Doing X", Status: StatusPending}})
	if !errors.Is(err, ErrEmptyContent) {
		t.Fatalf("expected ErrEmptyContent, got %v", err)
	}
	_, err = s.Apply("sess", []Item{{Content: "X", ActiveForm: "", Status: StatusPending}})
	if !errors.Is(err, ErrEmptyActiveForm) {
		t.Fatalf("expected ErrEmptyActiveForm, got %v", err)
	}
}

func TestApply_TooManyItems(t *testing.T) {
	s := NewStore()
	big := make([]Item, maxItemsPerList+1)
	for i := range big {
		big[i] = item(fmt.Sprintf("t%d", i), fmt.Sprintf("doing %d", i), StatusPending)
	}
	_, err := s.Apply("sess", big)
	if !errors.Is(err, ErrTooManyItems) {
		t.Fatalf("expected ErrTooManyItems, got %v", err)
	}
}

func TestApply_ExactlyMaxItemsAllowed(t *testing.T) {
	s := NewStore()
	full := make([]Item, maxItemsPerList)
	for i := range full {
		full[i] = item(fmt.Sprintf("t%d", i), fmt.Sprintf("doing %d", i), StatusPending)
	}
	if _, err := s.Apply("sess", full); err != nil {
		t.Fatalf("expected %d items to be accepted, got %v", maxItemsPerList, err)
	}
}

func TestGet_UnknownSession(t *testing.T) {
	s := NewStore()
	snap := s.Get("unknown")
	if snap.Version != 0 || snap.Items != nil {
		t.Fatalf("expected empty snapshot, got %+v", snap)
	}
}

func TestGet_ReturnsSafeCopy(t *testing.T) {
	s := NewStore()
	if _, err := s.Apply("sess", []Item{item("A", "Doing A", StatusPending)}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	a := s.Get("sess")
	a.Items[0].Content = "mutated"

	b := s.Get("sess")
	if b.Items[0].Content == "mutated" {
		t.Fatalf("Get must return a defensive copy; internal state was mutated")
	}
}

func TestClear_RemovesSession(t *testing.T) {
	s := NewStore()
	if _, err := s.Apply("sess", []Item{item("A", "Doing A", StatusPending)}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	s.Clear("sess")
	if got := s.Get("sess"); got.Version != 0 {
		t.Fatalf("expected cleared session, got version %d", got.Version)
	}
}

func TestApply_MonotonicAcrossAgents(t *testing.T) {
	// Simulates the "multiple agents in one session" contract: every write
	// bumps the version, regardless of which agent made the call.
	s := NewStore()
	for i := 1; i <= 5; i++ {
		snap, err := s.Apply("sess", []Item{item("A", "Doing A", StatusPending)})
		if err != nil {
			t.Fatalf("apply #%d: %v", i, err)
		}
		if snap.Version != int64(i) {
			t.Fatalf("expected version %d, got %d", i, snap.Version)
		}
	}
}

func TestApply_SessionsAreIsolated(t *testing.T) {
	s := NewStore()
	if _, err := s.Apply("s1", []Item{item("A", "Doing A", StatusPending)}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := s.Apply("s2", []Item{
		item("X", "Doing X", StatusPending),
		item("Y", "Doing Y", StatusPending),
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	s1 := s.Get("s1")
	s2 := s.Get("s2")
	if len(s1.Items) != 1 || len(s2.Items) != 2 {
		t.Fatalf("sessions leaked: s1=%d items, s2=%d items", len(s1.Items), len(s2.Items))
	}
	if s1.Version != 1 || s2.Version != 1 {
		t.Fatalf("per-session versions must start at 1 independently: s1=%d, s2=%d", s1.Version, s2.Version)
	}
}

func TestApply_ConcurrentWritesRaceFree(t *testing.T) {
	s := NewStore()
	const sessions = 4
	const writesPerSession = 50

	var wg sync.WaitGroup
	for i := range sessions {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sid := fmt.Sprintf("sess-%d", idx)
			for range writesPerSession {
				_, _ = s.Apply(sid, []Item{
					item("A", "Doing A", StatusPending),
					item("B", "Doing B", StatusPending),
				})
			}
		}(i)
	}
	wg.Wait()

	for i := range sessions {
		sid := fmt.Sprintf("sess-%d", i)
		snap := s.Get(sid)
		if snap.Version != writesPerSession {
			t.Fatalf("%s: expected version %d, got %d", sid, writesPerSession, snap.Version)
		}
	}
}
