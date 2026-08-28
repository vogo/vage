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

package interrupt

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/vogo/vage/schema"
)

// runStoreContract exercises every Store method and is shared between
// MapStore_test and FileStore_test so the two implementations stay
// byte-for-byte equivalent on observable behavior. factory must return a
// fresh, empty store for each subtest.
func runStoreContract(t *testing.T, name string, factory func(t *testing.T) Store) {
	t.Helper()

	t.Run(name+"/create_assigns_identity_and_pending_status", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()

		rec := newTestRecord("sess-1", []string{"call-1"})
		if err := s.Create(ctx, rec); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if rec.ID == "" {
			t.Error("Create left ID empty")
		}
		if rec.Version != CurrentVersion {
			t.Errorf("Version = %d, want %d", rec.Version, CurrentVersion)
		}
		if rec.Revision != 1 {
			t.Errorf("Revision = %d, want 1", rec.Revision)
		}
		if rec.Status != StatusPending {
			t.Errorf("Status = %q, want %q", rec.Status, StatusPending)
		}
		if rec.CreatedAt.IsZero() || rec.UpdatedAt.IsZero() {
			t.Error("CreatedAt/UpdatedAt left zero")
		}
	})

	t.Run(name+"/create_rejects_empty_or_duplicate_pending", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()

		emptyPending := newTestRecord("sess-1", nil)
		if err := s.Create(ctx, emptyPending); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("Create empty pending err = %v, want ErrInvalidArgument", err)
		}

		dupPending := newTestRecord("sess-1", []string{"call-1", "call-1"})
		if err := s.Create(ctx, dupPending); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("Create duplicate pending err = %v, want ErrInvalidArgument", err)
		}
	})

	t.Run(name+"/create_validates_inputs", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()

		if err := s.Create(ctx, nil); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("Create nil err = %v, want ErrInvalidArgument", err)
		}

		empty := newTestRecord("", nil)
		if err := s.Create(ctx, empty); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("Create empty session err = %v, want ErrInvalidArgument", err)
		}

		badPending := newTestRecord("sess-1", []string{"not-a-call"})
		if err := s.Create(ctx, badPending); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("Create unknown pending err = %v, want ErrInvalidArgument", err)
		}
	})

	t.Run(name+"/get_unknown_id_returns_not_found", func(t *testing.T) {
		s := factory(t)
		if _, err := s.Get(context.Background(), "nope"); !errors.Is(err, ErrNotFound) {
			t.Errorf("Get unknown err = %v, want ErrNotFound", err)
		}
	})

	t.Run(name+"/submit_decisions_transitions_pending_to_ready", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()

		rec := newTestRecord("sess-2", []string{"call-1", "call-2"})
		if err := s.Create(ctx, rec); err != nil {
			t.Fatalf("Create: %v", err)
		}

		updated, _, err := s.SubmitDecisions(ctx, rec.ID, []Decision{{ToolCallID: "call-1", Content: "yes"}})
		if err != nil {
			t.Fatalf("SubmitDecisions partial: %v", err)
		}
		if updated.Status != StatusPending {
			t.Errorf("Status after partial submit = %q, want %q", updated.Status, StatusPending)
		}

		updated, _, err = s.SubmitDecisions(ctx, rec.ID, []Decision{{ToolCallID: "call-2", Content: "no", IsError: true}})
		if err != nil {
			t.Fatalf("SubmitDecisions final: %v", err)
		}
		if updated.Status != StatusReady {
			t.Errorf("Status after full submit = %q, want %q", updated.Status, StatusReady)
		}
		if updated.Decisions["call-1"].Content != "yes" || updated.Decisions["call-2"].Content != "no" {
			t.Errorf("Decisions not persisted: %+v", updated.Decisions)
		}
		if updated.Decisions["call-1"].DecidedAt.IsZero() {
			t.Error("DecidedAt left zero")
		}
	})

	t.Run(name+"/submit_decisions_idempotent_resubmit", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()

		rec := newTestRecord("sess-3", []string{"call-1"})
		if err := s.Create(ctx, rec); err != nil {
			t.Fatalf("Create: %v", err)
		}

		d := []Decision{{ToolCallID: "call-1", Content: "ok"}}
		if _, _, err := s.SubmitDecisions(ctx, rec.ID, d); err != nil {
			t.Fatalf("first submit: %v", err)
		}
		if _, _, err := s.SubmitDecisions(ctx, rec.ID, d); err != nil {
			t.Errorf("idempotent resubmit err = %v, want nil", err)
		}
	})

	t.Run(name+"/submit_decisions_conflict_rejected", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()

		rec := newTestRecord("sess-4", []string{"call-1"})
		if err := s.Create(ctx, rec); err != nil {
			t.Fatalf("Create: %v", err)
		}

		if _, _, err := s.SubmitDecisions(ctx, rec.ID, []Decision{{ToolCallID: "call-1", Content: "a"}}); err != nil {
			t.Fatalf("first submit: %v", err)
		}
		_, _, err := s.SubmitDecisions(ctx, rec.ID, []Decision{{ToolCallID: "call-1", Content: "b"}})
		if !errors.Is(err, ErrDecisionConflict) {
			t.Errorf("conflicting resubmit err = %v, want ErrDecisionConflict", err)
		}

		got, gerr := s.Get(ctx, rec.ID)
		if gerr != nil {
			t.Fatalf("Get: %v", gerr)
		}
		if got.Decisions["call-1"].Content != "a" {
			t.Errorf("conflict mutated state: %+v", got.Decisions["call-1"])
		}
	})

	t.Run(name+"/submit_decisions_unknown_tool_call_rejected", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()

		rec := newTestRecord("sess-5", []string{"call-1"})
		if err := s.Create(ctx, rec); err != nil {
			t.Fatalf("Create: %v", err)
		}

		_, _, err := s.SubmitDecisions(ctx, rec.ID, []Decision{{ToolCallID: "call-999", Content: "x"}})
		if !errors.Is(err, ErrUnknownToolCall) {
			t.Errorf("unknown tool call err = %v, want ErrUnknownToolCall", err)
		}
	})

	t.Run(name+"/acquire_lease_requires_ready", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()

		rec := newTestRecord("sess-6", []string{"call-1"})
		if err := s.Create(ctx, rec); err != nil {
			t.Fatalf("Create: %v", err)
		}

		_, err := s.AcquireLease(ctx, rec.ID, "owner-a", time.Minute)
		if !errors.Is(err, ErrNotReady) {
			t.Errorf("AcquireLease on Pending err = %v, want ErrNotReady", err)
		}
	})

	t.Run(name+"/lease_lifecycle", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()

		rec := createReadyRecord(t, s, "sess-7")

		leased, err := s.AcquireLease(ctx, rec.ID, "owner-a", time.Minute)
		if err != nil {
			t.Fatalf("AcquireLease: %v", err)
		}
		if leased.Status != StatusResuming || leased.LeaseOwner != "owner-a" {
			t.Errorf("after AcquireLease: status=%q owner=%q", leased.Status, leased.LeaseOwner)
		}

		if _, err := s.AcquireLease(ctx, rec.ID, "owner-b", time.Minute); !errors.Is(err, ErrLeaseHeld) {
			t.Errorf("second AcquireLease err = %v, want ErrLeaseHeld", err)
		}

		if err := s.ReleaseLease(ctx, rec.ID, "owner-b"); !errors.Is(err, ErrLeaseNotOwned) {
			t.Errorf("ReleaseLease wrong owner err = %v, want ErrLeaseNotOwned", err)
		}

		if err := s.ReleaseLease(ctx, rec.ID, "owner-a"); err != nil {
			t.Fatalf("ReleaseLease: %v", err)
		}

		reacquired, err := s.AcquireLease(ctx, rec.ID, "owner-b", time.Minute)
		if err != nil {
			t.Fatalf("re-AcquireLease after release: %v", err)
		}
		if reacquired.LeaseOwner != "owner-b" {
			t.Errorf("re-AcquireLease owner = %q, want owner-b", reacquired.LeaseOwner)
		}

		if err := s.Complete(ctx, rec.ID, "owner-a"); !errors.Is(err, ErrLeaseNotOwned) {
			t.Errorf("Complete wrong owner err = %v, want ErrLeaseNotOwned", err)
		}
		if err := s.Complete(ctx, rec.ID, "owner-b"); err != nil {
			t.Fatalf("Complete: %v", err)
		}

		if _, err := s.AcquireLease(ctx, rec.ID, "owner-c", time.Minute); !errors.Is(err, ErrAlreadyCompleted) {
			t.Errorf("AcquireLease on Completed err = %v, want ErrAlreadyCompleted", err)
		}
		if _, _, err := s.SubmitDecisions(ctx, rec.ID, nil); !errors.Is(err, ErrAlreadyCompleted) {
			t.Errorf("SubmitDecisions on Completed err = %v, want ErrAlreadyCompleted", err)
		}
	})

	t.Run(name+"/expired_lease_is_reclaimable", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()

		rec := createReadyRecord(t, s, "sess-8")

		if _, err := s.AcquireLease(ctx, rec.ID, "owner-a", -time.Second); err != nil {
			t.Fatalf("AcquireLease with already-expired ttl: %v", err)
		}

		reclaimed, err := s.AcquireLease(ctx, rec.ID, "owner-b", time.Minute)
		if err != nil {
			t.Fatalf("reclaim expired lease: %v", err)
		}
		if reclaimed.LeaseOwner != "owner-b" {
			t.Errorf("reclaimed owner = %q, want owner-b", reclaimed.LeaseOwner)
		}
	})

	t.Run(name+"/list_returns_meta_without_body", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()

		for i := range 3 {
			rec := newTestRecord("sess-list", []string{"call-1"})
			rec.Iteration = i
			if err := s.Create(ctx, rec); err != nil {
				t.Fatalf("Create %d: %v", i, err)
			}
		}
		other := newTestRecord("sess-other", []string{"call-1"})
		if err := s.Create(ctx, other); err != nil {
			t.Fatalf("Create other: %v", err)
		}

		out, err := s.List(ctx, "sess-list")
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(out) != 3 {
			t.Fatalf("List len = %d, want 3", len(out))
		}
		for _, m := range out {
			if m.SessionID != "sess-list" {
				t.Errorf("List leaked other session: %+v", m)
			}
			if len(m.Pending) == 0 {
				t.Errorf("Meta.Pending empty: %+v", m)
			}
		}
	})

	t.Run(name+"/list_validates_session_id", func(t *testing.T) {
		s := factory(t)
		if _, err := s.List(context.Background(), ""); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("List empty session err = %v, want ErrInvalidArgument", err)
		}
	})

	t.Run(name+"/delete_removes_record", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()

		rec := newTestRecord("sess-9", []string{"call-1"})
		if err := s.Create(ctx, rec); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if err := s.Delete(ctx, rec.ID); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if _, err := s.Get(ctx, rec.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("Get after delete err = %v, want ErrNotFound", err)
		}
	})

	t.Run(name+"/delete_unknown_id_is_noop", func(t *testing.T) {
		s := factory(t)
		if err := s.Delete(context.Background(), "nope"); err != nil {
			t.Errorf("Delete unknown: %v", err)
		}
	})

	// A record ID is a store-minted opaque token; anything else must be
	// rejected identically by every backend, before it reaches the storage
	// medium. For FileStore that is a path-safety requirement (see
	// TestFileStore_MalformedIDCannotEscapeRoot); asserting it here keeps
	// MapStore from drifting into accepting ids FileStore refuses.
	t.Run(name+"/malformed_ids_rejected_at_every_entry_point", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()

		for _, id := range []string{"", "../escape", "sub/dir", "a\\b", ".", "..", "id.with.dots"} {
			if _, err := s.Get(ctx, id); !errors.Is(err, ErrInvalidArgument) {
				t.Errorf("Get(%q) err = %v, want ErrInvalidArgument", id, err)
			}
			if _, _, err := s.SubmitDecisions(ctx, id, nil); !errors.Is(err, ErrInvalidArgument) {
				t.Errorf("SubmitDecisions(%q) err = %v, want ErrInvalidArgument", id, err)
			}
			if _, err := s.AcquireLease(ctx, id, "owner", time.Minute); !errors.Is(err, ErrInvalidArgument) {
				t.Errorf("AcquireLease(%q) err = %v, want ErrInvalidArgument", id, err)
			}
			if err := s.ReleaseLease(ctx, id, "owner"); !errors.Is(err, ErrInvalidArgument) {
				t.Errorf("ReleaseLease(%q) err = %v, want ErrInvalidArgument", id, err)
			}
			if err := s.Complete(ctx, id, "owner"); !errors.Is(err, ErrInvalidArgument) {
				t.Errorf("Complete(%q) err = %v, want ErrInvalidArgument", id, err)
			}
			if err := s.Delete(ctx, id); !errors.Is(err, ErrInvalidArgument) {
				t.Errorf("Delete(%q) err = %v, want ErrInvalidArgument", id, err)
			}
		}
	})

	// Decisions commit in order. Both backends must retain the same valid
	// prefix before a later conflict or unknown tool-call ID is rejected.
	t.Run(name+"/submit_decisions_retains_valid_prefix", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()

		rec := newTestRecord("sess-atomic", []string{"call-1", "call-2"})
		if err := s.Create(ctx, rec); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if _, _, err := s.SubmitDecisions(ctx, rec.ID, []Decision{{ToolCallID: "call-1", Content: "a"}}); err != nil {
			t.Fatalf("seed decision: %v", err)
		}

		// call-2 is valid and undecided; call-1 conflicts with "a".
		prefix, _, err := s.SubmitDecisions(ctx, rec.ID, []Decision{
			{ToolCallID: "call-2", Content: "b"},
			{ToolCallID: "call-1", Content: "different"},
		})
		if !errors.Is(err, ErrDecisionConflict) {
			t.Fatalf("mixed batch err = %v, want ErrDecisionConflict", err)
		}
		if prefix == nil {
			t.Fatal("SubmitDecisions returned nil record alongside a prefix error")
		}

		got, gerr := s.Get(ctx, rec.ID)
		if gerr != nil {
			t.Fatalf("Get: %v", gerr)
		}
		if got.Decisions["call-2"].Content != "b" {
			t.Errorf("valid prefix was not committed: %+v", got.Decisions)
		}
		if got.Decisions["call-1"].Content != "a" {
			t.Errorf("call-1 = %q, want unchanged \"a\"", got.Decisions["call-1"].Content)
		}
		if got.Status != StatusReady {
			t.Errorf("Status = %q, want %q after prefix completed decisions", got.Status, StatusReady)
		}

		unknownRec := newTestRecord("sess-prefix-unknown", []string{"call-1", "call-2"})
		if err := s.Create(ctx, unknownRec); err != nil {
			t.Fatalf("Create unknown-case record: %v", err)
		}
		unknownPrefix, _, err := s.SubmitDecisions(ctx, unknownRec.ID, []Decision{
			{ToolCallID: "call-1", Content: "a"},
			{ToolCallID: "call-999", Content: "x"},
		})
		if !errors.Is(err, ErrUnknownToolCall) {
			t.Fatalf("unknown-id batch err = %v, want ErrUnknownToolCall", err)
		}
		if unknownPrefix == nil {
			t.Fatal("SubmitDecisions returned nil record alongside a prefix error")
		}
		got, gerr = s.Get(ctx, unknownRec.ID)
		if gerr != nil {
			t.Fatalf("Get: %v", gerr)
		}
		if got.Decisions["call-1"].Content != "a" {
			t.Errorf("valid prefix before unknown ID was not committed: %+v", got.Decisions)
		}
		if _, committed := got.Decisions["call-2"]; committed {
			t.Error("decision after rejected entry was committed")
		}
	})

	// A batch that names the same call twice is judged like any other
	// resubmission: identical is idempotent, while a divergent duplicate
	// rejects the duplicate after retaining the first entry.
	t.Run(name+"/submit_decisions_duplicate_within_batch", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()

		rec := newTestRecord("sess-dup", []string{"call-1", "call-2"})
		if err := s.Create(ctx, rec); err != nil {
			t.Fatalf("Create: %v", err)
		}

		_, _, err := s.SubmitDecisions(ctx, rec.ID, []Decision{
			{ToolCallID: "call-1", Content: "a"},
			{ToolCallID: "call-1", Content: "b"},
		})
		if !errors.Is(err, ErrDecisionConflict) {
			t.Fatalf("divergent duplicate err = %v, want ErrDecisionConflict", err)
		}
		got, gerr := s.Get(ctx, rec.ID)
		if gerr != nil {
			t.Fatalf("Get: %v", gerr)
		}
		if got.Decisions["call-1"].Content != "a" {
			t.Errorf("Decisions = %+v, want first duplicate committed", got.Decisions)
		}

		if _, _, err := s.SubmitDecisions(ctx, rec.ID, []Decision{
			{ToolCallID: "call-1", Content: "a"},
			{ToolCallID: "call-1", Content: "a"},
		}); err != nil {
			t.Errorf("identical duplicate err = %v, want nil", err)
		}
	})

	// An idempotent SubmitDecisions after AcquireLease must not demote
	// Resuming back to Ready, or a second owner can take a live lease.
	t.Run(name+"/submit_decisions_does_not_demote_resuming", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()

		rec := createReadyRecord(t, s, "sess-resuming")
		if _, err := s.AcquireLease(ctx, rec.ID, "owner-a", time.Minute); err != nil {
			t.Fatalf("AcquireLease: %v", err)
		}

		updated, _, err := s.SubmitDecisions(ctx, rec.ID, []Decision{{ToolCallID: "call-1", Content: "ok"}})
		if err != nil {
			t.Fatalf("idempotent SubmitDecisions during Resuming: %v", err)
		}
		if updated.Status != StatusResuming {
			t.Errorf("Status after idempotent submit = %q, want %q", updated.Status, StatusResuming)
		}
		if updated.LeaseOwner != "owner-a" {
			t.Errorf("LeaseOwner = %q, want owner-a", updated.LeaseOwner)
		}

		if _, err := s.AcquireLease(ctx, rec.ID, "owner-b", time.Minute); !errors.Is(err, ErrLeaseHeld) {
			t.Errorf("second AcquireLease err = %v, want ErrLeaseHeld", err)
		}

		got, gerr := s.Get(ctx, rec.ID)
		if gerr != nil {
			t.Fatalf("Get: %v", gerr)
		}
		if got.Status != StatusResuming || got.LeaseOwner != "owner-a" {
			t.Errorf("persisted status=%q owner=%q, want Resuming/owner-a", got.Status, got.LeaseOwner)
		}
	})

	// Exactly one concurrent submitter of the same decision may claim the
	// write. The committed slice — not a before/after diff by the caller —
	// is what makes this decidable: every racer observes the same final
	// record, so a caller comparing its own pre-submit read against the
	// result would have them all report a write and emit a duplicate
	// interrupt_decision_stored event.
	t.Run(name+"/concurrent_identical_submits_commit_once", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()

		rec := newTestRecord("sess-race", []string{"call-1"})
		if err := s.Create(ctx, rec); err != nil {
			t.Fatalf("Create: %v", err)
		}

		const submitters = 8
		var (
			wg     sync.WaitGroup
			mu     sync.Mutex
			claims int
			errs   []error
		)
		wg.Add(submitters)
		for range submitters {
			go func() {
				defer wg.Done()
				_, committed, err := s.SubmitDecisions(ctx, rec.ID, []Decision{{ToolCallID: "call-1", Content: "ok"}})
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					errs = append(errs, err)
					return
				}
				claims += len(committed)
			}()
		}
		wg.Wait()

		if len(errs) > 0 {
			t.Fatalf("concurrent identical submits returned errors: %v", errs)
		}
		if claims != 1 {
			t.Errorf("decisions reported as committed = %d, want 1 across %d identical submits", claims, submitters)
		}

		got, gerr := s.Get(ctx, rec.ID)
		if gerr != nil {
			t.Fatalf("Get: %v", gerr)
		}
		if got.Revision != 2 {
			t.Errorf("Revision = %d, want 2 (Create + one commit)", got.Revision)
		}
		if got.Status != StatusReady {
			t.Errorf("Status = %q, want %q", got.Status, StatusReady)
		}
	})

	// A rejected batch reports only the prefix it actually wrote.
	t.Run(name+"/submit_decisions_reports_committed_prefix", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()

		rec := newTestRecord("sess-committed", []string{"call-1", "call-2"})
		if err := s.Create(ctx, rec); err != nil {
			t.Fatalf("Create: %v", err)
		}

		_, committed, err := s.SubmitDecisions(ctx, rec.ID, []Decision{{ToolCallID: "call-1", Content: "a"}})
		if err != nil {
			t.Fatalf("seed decision: %v", err)
		}
		if len(committed) != 1 || committed[0] != "call-1" {
			t.Fatalf("committed = %v, want [call-1]", committed)
		}

		// call-2 commits, then the conflicting call-1 aborts the batch.
		_, committed, err = s.SubmitDecisions(ctx, rec.ID, []Decision{
			{ToolCallID: "call-2", Content: "b"},
			{ToolCallID: "call-1", Content: "different"},
		})
		if !errors.Is(err, ErrDecisionConflict) {
			t.Fatalf("mixed batch err = %v, want ErrDecisionConflict", err)
		}
		if len(committed) != 1 || committed[0] != "call-2" {
			t.Errorf("committed = %v, want [call-2]", committed)
		}

		// Everything is decided now, so a full replay writes nothing.
		_, committed, err = s.SubmitDecisions(ctx, rec.ID, []Decision{
			{ToolCallID: "call-1", Content: "a"},
			{ToolCallID: "call-2", Content: "b"},
		})
		if err != nil {
			t.Fatalf("replay: %v", err)
		}
		if len(committed) != 0 {
			t.Errorf("committed on replay = %v, want empty", committed)
		}
	})

	t.Run(name+"/delete_respects_canceled_context", func(t *testing.T) {
		s := factory(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := s.Delete(ctx, "any-well-formed-id"); !errors.Is(err, context.Canceled) {
			t.Errorf("Delete canceled ctx err = %v, want context.Canceled", err)
		}
	})
}

func createReadyRecord(t *testing.T, s Store, sessionID string) *Record {
	t.Helper()
	ctx := context.Background()
	rec := newTestRecord(sessionID, []string{"call-1"})
	if err := s.Create(ctx, rec); err != nil {
		t.Fatalf("Create: %v", err)
	}
	updated, _, err := s.SubmitDecisions(ctx, rec.ID, []Decision{{ToolCallID: "call-1", Content: "ok"}})
	if err != nil {
		t.Fatalf("SubmitDecisions to Ready: %v", err)
	}
	if updated.Status != StatusReady {
		t.Fatalf("Status after seed decisions = %q, want %q", updated.Status, StatusReady)
	}
	return updated
}

func newTestRecord(sessionID string, pending []string) *Record {
	calls := []schema.ToolCall{
		{ID: "call-1", Name: "ask_user", Arguments: `{"question":"proceed?"}`},
	}
	if len(pending) > 1 {
		calls = append(calls, schema.ToolCall{ID: "call-2", Name: "ask_user", Arguments: `{"question":"more?"}`})
	}

	return &Record{
		SessionID: sessionID,
		AgentID:   "test-agent",
		Protocol:  schema.ProtocolOpenAIChat,
		ToolCalls: calls,
		Pending:   pending,
		Messages: []schema.Message{
			schema.NewTextMessage(schema.ProtocolOpenAIChat, schema.RoleSystem, "sys"),
			schema.NewTextMessage(schema.ProtocolOpenAIChat, schema.RoleUser, "hi"),
		},
		Params: EffectiveParams{
			Model:         "gpt-test",
			MaxIterations: 10,
		},
		Iteration: 0,
		Usage:     schema.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	}
}
