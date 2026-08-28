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
	rec := newTestRecord("sess-race", nil) // Ready immediately
	if err := seed.Create(ctx, rec); err != nil {
		t.Fatalf("Create: %v", err)
	}

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

// TestFileStore_StaleLockIsReclaimed verifies a crashed holder's leftover
// <id>.lock file does not deadlock every future writer.
func TestFileStore_StaleLockIsReclaimed(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	s, err := NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	rec := newTestRecord("sess-stale", nil)
	if err := s.Create(ctx, rec); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Simulate a crashed holder: create the lock file directly and
	// backdate its mtime past staleLockAge.
	lockPath := s.lockPath(rec.ID)
	if err := os.WriteFile(lockPath, nil, filestoreFilePerm); err != nil {
		t.Fatalf("write stale lock: %v", err)
	}
	old := time.Now().Add(-2 * staleLockAge)
	if err := os.Chtimes(lockPath, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	if _, err := s.AcquireLease(ctx, rec.ID, "owner-a", time.Minute); err != nil {
		t.Fatalf("AcquireLease past stale lock: %v", err)
	}
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
	rec := newTestRecord("sess-v", nil)
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
