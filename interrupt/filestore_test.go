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
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestFileStore_Contract(t *testing.T) {
	runStoreContract(t, "FileStore", func(t *testing.T) Store {
		t.Helper()
		s, err := NewFileStore(t.TempDir())
		if err != nil {
			t.Fatalf("NewFileStore: %v", err)
		}
		return s
	})
}

func TestFileStore_New_RejectsEmptyRoot(t *testing.T) {
	if _, err := NewFileStore(""); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("NewFileStore(\"\") err = %v, want ErrInvalidArgument", err)
	}
}

// TestFileStore_CrossInstance_SeesEachOthersWrites simulates process A
// creating and deciding an interrupt, closing its store handle, and process
// B opening a brand-new FileStore over the same directory and reading/
// resuming it — the scenario the whole package exists for. Two independent
// FileStore values with zero shared Go memory stand in for two OS
// processes; only the filesystem is shared, which is the actual contract.
func TestFileStore_CrossInstance_SeesEachOthersWrites(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	storeA, err := NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore A: %v", err)
	}

	rec := newTestRecord("sess-x", []string{"call-1"})
	if err := storeA.Create(ctx, rec); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// storeA is dropped here with no Close/Flush step — FileStore has none,
	// by design: every mutation is already durable on disk when the method
	// returns.

	storeB, err := NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore B: %v", err)
	}

	got, err := storeB.Get(ctx, rec.ID)
	if err != nil {
		t.Fatalf("Get from independent instance: %v", err)
	}
	if got.SessionID != "sess-x" || got.Status != StatusPending {
		t.Fatalf("cross-instance Get mismatch: %+v", got)
	}

	updated, err := storeB.SubmitDecisions(ctx, rec.ID, []Decision{{ToolCallID: "call-1", Content: "approved"}})
	if err != nil {
		t.Fatalf("SubmitDecisions from B: %v", err)
	}
	if updated.Status != StatusReady {
		t.Fatalf("Status after B's decision = %q, want Ready", updated.Status)
	}

	leased, err := storeB.AcquireLease(ctx, rec.ID, "process-b", time.Minute)
	if err != nil {
		t.Fatalf("AcquireLease from B: %v", err)
	}
	if leased.LeaseOwner != "process-b" {
		t.Fatalf("lease owner = %q, want process-b", leased.LeaseOwner)
	}

	// storeA, independently re-reading the same directory, must observe
	// everything storeB did.
	fromA, err := storeA.Get(ctx, rec.ID)
	if err != nil {
		t.Fatalf("Get from A after B's writes: %v", err)
	}
	if fromA.Status != StatusResuming || fromA.LeaseOwner != "process-b" {
		t.Fatalf("A did not observe B's writes: %+v", fromA)
	}
}

// TestFileStore_CrossInstance_LeaseIsExclusive races AcquireLease from many
// independent FileStore instances (again standing in for many processes)
// against the same Ready record and asserts exactly one succeeds. This is
// the property the file lock exists for — an in-memory mutex could never
// prove it, since two MapStore values never share state to race over.
func TestFileStore_CrossInstance_LeaseIsExclusive(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	seed, err := NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore seed: %v", err)
	}
	rec := createReadyRecord(t, seed, "sess-race")

	const contenders = 8
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		wins    int
		lockErr []error
	)

	start := make(chan struct{})
	for i := range contenders {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()

			store, err := NewFileStore(root) // independent instance per goroutine
			if err != nil {
				mu.Lock()
				lockErr = append(lockErr, err)
				mu.Unlock()
				return
			}

			<-start
			_, err = store.AcquireLease(ctx, rec.ID, ownerName(i), time.Minute)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				wins++
			} else if !errors.Is(err, ErrLeaseHeld) {
				lockErr = append(lockErr, err)
			}
		}(i)
	}
	close(start)
	wg.Wait()

	if len(lockErr) > 0 {
		t.Fatalf("unexpected errors: %v", lockErr)
	}
	if wins != 1 {
		t.Fatalf("concurrent AcquireLease wins = %d, want exactly 1", wins)
	}
}

func ownerName(i int) string {
	return "owner-" + string(rune('a'+i))
}

// TestFileStore_AbandonedLockFileDoesNotDeadlock verifies a crashed
// holder's leftover <id>.lock file does not wedge every future writer: the
// OS dropped that holder's lock when the process died, so the file is just
// an empty inode to be relocked.
func TestFileStore_AbandonedLockFileDoesNotDeadlock(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	s, err := NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	rec := createReadyRecord(t, s, "sess-abandoned")

	// Simulate a crashed holder: the lock file exists, but nobody holds
	// an OS lock on it.
	lockPath := s.lockPath(rec.ID)
	if err := os.WriteFile(lockPath, nil, filestoreFilePerm); err != nil {
		t.Fatalf("write abandoned lock: %v", err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(lockPath, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	if _, err := s.AcquireLease(ctx, rec.ID, "owner-a", time.Minute); err != nil {
		t.Fatalf("AcquireLease past abandoned lock: %v", err)
	}
}

// TestFileStore_LiveHolderIsNotPreemptedByLockAge is the regression for
// age-based lock reclamation: however old the lock file looks, a second
// instance must not enter while the first is still inside its critical
// section, and the first instance's release must not revoke a lock it no
// longer owns. The old mtime here stands in for any holder whose critical
// section outlives an arbitrary staleness threshold — a slow fsync, a
// paused process, a loaded machine.
func TestFileStore_LiveHolderIsNotPreemptedByLockAge(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	storeA, err := NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore A: %v", err)
	}
	storeB, err := NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore B: %v", err)
	}
	rec := createReadyRecord(t, storeA, "sess-live-holder")

	release, err := storeA.acquireFileLock(ctx, rec.ID)
	if err != nil {
		t.Fatalf("acquireFileLock: %v", err)
	}

	// Make the live lock look arbitrarily abandoned to any age heuristic.
	lockPath := storeA.lockPath(rec.ID)
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(lockPath, old, old); err != nil {
		release()
		t.Fatalf("chtimes: %v", err)
	}

	waitCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	if _, err := storeB.AcquireLease(waitCtx, rec.ID, "owner-b", time.Minute); !errors.Is(err, context.DeadlineExceeded) {
		release()
		t.Fatalf("AcquireLease err = %v, want context.DeadlineExceeded while holder is live", err)
	}

	got, err := storeA.readRecord(rec.ID)
	if err != nil {
		release()
		t.Fatalf("readRecord: %v", err)
	}
	if got.Status != StatusReady || got.LeaseOwner != "" {
		release()
		t.Fatalf("record mutated by a preempting waiter: status=%q owner=%q", got.Status, got.LeaseOwner)
	}

	release()

	// Releasing drops only this descriptor's lock; the file itself stays,
	// so no waiter can ever be locking a different inode than a newcomer.
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("lock file unlinked by release: %v", err)
	}

	if _, err := storeB.AcquireLease(ctx, rec.ID, "owner-b", time.Minute); err != nil {
		t.Fatalf("AcquireLease after release: %v", err)
	}
}

// TestFileStore_MalformedIDCannotEscapeRoot is the path-traversal
// regression: every id-taking entry point must refuse a caller-shaped id
// before it is joined onto root, so no read, write or — most damagingly —
// Delete can ever resolve to a file the store does not own.
func TestFileStore_MalformedIDCannotEscapeRoot(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "store")
	ctx := context.Background()

	s, err := NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	// A bystander file one level above root, named so that a naive
	// filepath.Join(root, id+".json") would land exactly on it.
	victim := filepath.Join(base, "victim.json")
	if err := os.WriteFile(victim, []byte(`{"keep":"me"}`), filestoreFilePerm); err != nil {
		t.Fatalf("write victim: %v", err)
	}
	victimLock := filepath.Join(base, "victim.lock")
	if err := os.WriteFile(victimLock, nil, filestoreFilePerm); err != nil {
		t.Fatalf("write victim lock: %v", err)
	}

	escapes := []string{
		"../victim",
		"..\\..\\victim",
		"sub/victim",
		"/etc/passwd",
		"",
	}

	for _, id := range escapes {
		if _, err := s.Get(ctx, id); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("Get(%q) err = %v, want ErrInvalidArgument", id, err)
		}
		if _, err := s.SubmitDecisions(ctx, id, []Decision{{ToolCallID: "call-1", Content: "x"}}); !errors.Is(err, ErrInvalidArgument) {
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

	for _, path := range []string{victim, victimLock} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("file outside root was touched by a traversal id: %s: %v", path, err)
		}
	}

	// Nothing may have been created outside root either.
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatalf("ReadDir base: %v", err)
	}
	if len(entries) != 3 { // store/, victim.json, victim.lock
		t.Errorf("base dir entries = %d, want 3; traversal id wrote outside root", len(entries))
	}
}

// TestFileStore_CrossInstance_LockBlocksDeleteAndWrite holds the
// cross-process record lock from instance A and asserts that instance B
// cannot Delete or SubmitDecisions until A releases — the lock file must
// not be unlinked out from under a live holder.
func TestFileStore_CrossInstance_LockBlocksDeleteAndWrite(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	storeA, err := NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore A: %v", err)
	}
	rec := newTestRecord("sess-lock", []string{"call-1"})
	if err := storeA.Create(ctx, rec); err != nil {
		t.Fatalf("Create: %v", err)
	}

	storeB, err := NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore B: %v", err)
	}

	t.Run("delete_waits_for_holder", func(t *testing.T) {
		release, lockErr := storeA.acquireFileLock(ctx, rec.ID)
		if lockErr != nil {
			t.Fatalf("acquireFileLock: %v", lockErr)
		}

		done := make(chan error, 1)
		go func() {
			done <- storeB.Delete(ctx, rec.ID)
		}()

		select {
		case err := <-done:
			release()
			t.Fatalf("Delete returned while A held the lock: %v", err)
		case <-time.After(50 * time.Millisecond):
		}

		if _, err := os.Stat(storeA.recordPath(rec.ID)); err != nil {
			release()
			t.Fatalf("record vanished while lock held: %v", err)
		}

		release()

		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("Delete after release: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Delete did not complete after lock released")
		}

		if _, err := storeB.Get(ctx, rec.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("Get after Delete err = %v, want ErrNotFound", err)
		}
	})

	t.Run("write_waits_for_holder", func(t *testing.T) {
		// Recreate after the delete subtest.
		rec := newTestRecord("sess-lock-w", []string{"call-1"})
		if err := storeA.Create(ctx, rec); err != nil {
			t.Fatalf("Create: %v", err)
		}

		release, lockErr := storeA.acquireFileLock(ctx, rec.ID)
		if lockErr != nil {
			t.Fatalf("acquireFileLock: %v", lockErr)
		}

		type result struct {
			rec *Record
			err error
		}
		done := make(chan result, 1)
		go func() {
			updated, err := storeB.SubmitDecisions(ctx, rec.ID, []Decision{{ToolCallID: "call-1", Content: "ok"}})
			done <- result{updated, err}
		}()

		select {
		case got := <-done:
			release()
			t.Fatalf("SubmitDecisions returned while A held the lock: rec=%v err=%v", got.rec, got.err)
		case <-time.After(50 * time.Millisecond):
		}

		got, gerr := storeA.readRecord(rec.ID)
		if gerr != nil {
			release()
			t.Fatalf("readRecord while lock held: %v", gerr)
		}
		if got.Status != StatusPending || len(got.Decisions) != 0 {
			release()
			t.Fatalf("record mutated while lock held: status=%q decisions=%+v", got.Status, got.Decisions)
		}

		release()

		select {
		case got := <-done:
			if got.err != nil {
				t.Fatalf("SubmitDecisions after release: %v", got.err)
			}
			if got.rec.Status != StatusReady {
				t.Errorf("Status = %q, want Ready", got.rec.Status)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("SubmitDecisions did not complete after lock released")
		}
	})

	t.Run("delete_observes_canceled_ctx_while_waiting", func(t *testing.T) {
		rec := newTestRecord("sess-lock-ctx", []string{"call-1"})
		if err := storeA.Create(ctx, rec); err != nil {
			t.Fatalf("Create: %v", err)
		}

		release, lockErr := storeA.acquireFileLock(ctx, rec.ID)
		if lockErr != nil {
			t.Fatalf("acquireFileLock: %v", lockErr)
		}
		defer release()

		waitCtx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			done <- storeB.Delete(waitCtx, rec.ID)
		}()

		select {
		case err := <-done:
			t.Fatalf("Delete returned before cancel: %v", err)
		case <-time.After(20 * time.Millisecond):
		}

		cancel()

		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("Delete err = %v, want context.Canceled", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Delete did not observe ctx cancel")
		}

		if _, err := storeA.readRecord(rec.ID); err != nil {
			t.Errorf("record deleted despite canceled Delete: %v", err)
		}
	})
}

// TestFileStore_UnknownVersionRejected verifies a record written by a
// future/unknown schema version is rejected rather than guess-read.
func TestFileStore_UnknownVersionRejected(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	s, err := NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	rec := newTestRecord("sess-v", []string{"call-1"})
	if err := s.Create(ctx, rec); err != nil {
		t.Fatalf("Create: %v", err)
	}

	rec.Version = CurrentVersion + 1
	if err := s.writeRecord(rec); err != nil {
		t.Fatalf("writeRecord: %v", err)
	}

	if _, err := s.Get(ctx, rec.ID); !errors.Is(err, ErrUnknownVersion) {
		t.Errorf("Get unknown version err = %v, want ErrUnknownVersion", err)
	}
}
