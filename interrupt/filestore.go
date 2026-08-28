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
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	filestoreDirPerm  os.FileMode = 0o700
	filestoreFilePerm os.FileMode = 0o600
	recordFileExt                 = ".json"
	recordTmpExt                  = ".json.tmp"
	lockFileExt                   = ".lock"

	// lockRetryInterval / lockWaitTimeout bound how long a writer spins
	// trying to acquire the per-record mutex file below before giving up.
	// The mutex only ever guards a read-modify-write of one small JSON
	// file, so contention is expected to clear in microseconds; the
	// multi-second ceiling exists so a caller that passed no deadline
	// still gets an error instead of hanging. There is deliberately no
	// age-based reclamation: a lock is released by the kernel, never by a
	// waiter's guess about how long a holder "should" take.
	lockRetryInterval = 2 * time.Millisecond
	lockWaitTimeout   = 5 * time.Second
)

// FileStore persists interrupt Records under a root directory as one JSON
// file per record:
//
//	<root>/<id>.json
//
// Unlike checkpoint.FileIterationStore (which only needs same-process
// serialization because IterationStore.Save always races with itself, not
// with another process), FileStore's AcquireLease must provide real mutual
// exclusion across independent processes — that is the entire reason this
// package exists. It gets this from an OS advisory lock (flock / LockFileEx)
// taken on a companion `<id>.lock` file: the kernel, not this code, decides
// who holds it, so two FileStore instances — in the same process or in two
// separate ones — opening the same root directory can never both believe
// they do. Two properties follow, and both are load-bearing:
//
//   - A live holder is never preempted. Nothing reclaims a lock because it
//     looks old: a critical section stalled by a slow fsync, a paused
//     process or a busy scheduler keeps its exclusion for as long as it
//     takes. Age-based reclamation would hand the same record to two
//     resumers, which is precisely the failure this package exists to
//     prevent.
//   - A dead holder never wedges the store. The lock lives on the open file
//     description, so the kernel drops it when the process exits for any
//     reason, crash included.
//
// The lock is held only for the duration of one read-modify-write of the
// record (a mutex, not the lease itself); the lease's own identity and
// expiry live in the record's LeaseOwner / LeaseExpiresAt fields, which is
// what AcquireLease actually contests.
//
// `<id>.lock` files are created on demand and never unlinked — not even by
// Delete. Unlinking one would let a waiter that already opened that inode
// and a newcomer that creates a fresh file lock two different inodes and
// both enter, so the empty file is left behind deliberately; it is inert,
// and List ignores it.
//
// In-process concurrent callers additionally serialize through a per-id
// sync.Mutex so a single process never spins against its own lock file.
type FileStore struct {
	root  string
	locks sync.Map // map[string]*sync.Mutex, in-process fast path
}

// Compile-time check.
var _ Store = (*FileStore)(nil)

// NewFileStore creates the store rooted at the given directory, creating it
// (with parents) if it does not exist.
func NewFileStore(root string) (*FileStore, error) {
	if root == "" {
		return nil, fmt.Errorf("%w: root directory is empty", ErrInvalidArgument)
	}
	if err := os.MkdirAll(root, filestoreDirPerm); err != nil {
		return nil, fmt.Errorf("interrupt: create root %q: %w", root, err)
	}
	return &FileStore{root: root}, nil
}

// Root returns the configured root directory; useful in tests.
func (s *FileStore) Root() string { return s.root }

// recordPath / lockPath assume id already passed validateID — that check is
// the sole reason this join cannot escape root, so every public entry point
// runs it before reaching here.
func (s *FileStore) recordPath(id string) string {
	return filepath.Join(s.root, id+recordFileExt)
}

func (s *FileStore) lockPath(id string) string {
	return filepath.Join(s.root, id+lockFileExt)
}

func (s *FileStore) localLock(id string) *sync.Mutex {
	v, _ := s.locks.LoadOrStore(id, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// withExclusive runs fn while holding both the in-process mutex for id and
// the cross-process file lock, so fn is the only critical section touching
// this id across every FileStore instance (any process) pointed at this
// root. Delete uses this directly; withRecordLock layers a read-modify-write
// on top.
func (s *FileStore) withExclusive(ctx context.Context, id string, fn func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateID(id); err != nil {
		return err
	}

	mu := s.localLock(id)
	mu.Lock()
	defer mu.Unlock()

	release, err := s.acquireFileLock(ctx, id)
	if err != nil {
		return err
	}
	defer release()

	return fn()
}

// withRecordLock runs fn while holding the exclusive lock for id.
// fn sees the current record (nil if none exists yet — used by Create)
// and returns the record to persist, or a nil record with a non-nil error
// to abort without writing.
func (s *FileStore) withRecordLock(ctx context.Context, id string, fn func(cur *Record) (*Record, error)) (*Record, error) {
	var next *Record
	err := s.withExclusive(ctx, id, func() error {
		cur, err := s.readRecord(id)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}

		var fnErr error
		next, fnErr = fn(cur)
		if fnErr != nil {
			return fnErr
		}

		return s.writeRecord(next)
	})
	return next, err
}

// acquireFileLock opens <id>.lock and spins on a non-blocking exclusive
// OS lock over it until it succeeds, the context is canceled, or
// lockWaitTimeout elapses. The returned release drops only this
// descriptor's own lock: it never unlinks the file, so it cannot revoke
// exclusion that meanwhile belongs to somebody else. A lock file left by a
// crashed process is reused as-is — its lock died with the process — which
// is why no age heuristic is needed to avoid a permanent deadlock.
func (s *FileStore) acquireFileLock(ctx context.Context, id string) (release func(), err error) {
	path := s.lockPath(id)
	deadline := time.Now().Add(lockWaitTimeout)

	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, filestoreFilePerm)
	if err != nil {
		return nil, fmt.Errorf("interrupt: open lock %q: %w", path, err)
	}
	release = func() {
		_ = unlockFile(f)
		_ = f.Close()
	}

	for {
		locked, lockErr := tryLockFile(f)
		if lockErr != nil {
			_ = f.Close()
			return nil, fmt.Errorf("interrupt: lock %q: %w", path, lockErr)
		}
		if locked {
			return release, nil
		}

		if err := ctx.Err(); err != nil {
			_ = f.Close()
			return nil, err
		}
		if time.Now().After(deadline) {
			_ = f.Close()
			return nil, fmt.Errorf("interrupt: timed out waiting for lock on %q", id)
		}
		time.Sleep(lockRetryInterval)
	}
}

// readRecord decodes the JSON file for id, or ErrNotFound if it does not
// exist.
func (s *FileStore) readRecord(id string) (*Record, error) {
	data, err := os.ReadFile(s.recordPath(id))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("interrupt: read %q: %w", id, err)
	}

	var r Record
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("interrupt: decode %q: %w", id, err)
	}
	if r.Version != CurrentVersion {
		return nil, ErrUnknownVersion
	}
	return &r, nil
}

// writeRecord encodes rec to its file atomically (temp file + rename).
func (s *FileStore) writeRecord(rec *Record) (err error) {
	path := s.recordPath(rec.ID)
	tmp := path + ".tmp"

	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, filestoreFilePerm)
	if err != nil {
		return fmt.Errorf("interrupt: open tmp: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp)
		}
	}()

	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err = enc.Encode(rec); err != nil {
		_ = f.Close()
		return fmt.Errorf("interrupt: encode: %w", err)
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("interrupt: fsync: %w", err)
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("interrupt: close tmp: %w", err)
	}
	if err = os.Rename(tmp, path); err != nil {
		return fmt.Errorf("interrupt: rename: %w", err)
	}
	return nil
}

// Create validates, assigns identity/audit fields, and writes the record.
func (s *FileStore) Create(ctx context.Context, rec *Record) error {
	if err := validateNewRecord(rec); err != nil {
		return err
	}

	// Generate the ID before taking the lock: Create never contends with
	// anyone else for a not-yet-existing ID.
	id := generateID()
	rec.ID = id

	_, err := s.withRecordLock(ctx, id, func(_ *Record) (*Record, error) {
		now := time.Now()
		rec.Version = CurrentVersion
		rec.Revision = 1
		rec.CreatedAt = now
		rec.UpdatedAt = now
		rec.Status = initialStatus(rec.Pending)
		return rec, nil
	})
	return err
}

// Get returns the record identified by id.
func (s *FileStore) Get(ctx context.Context, id string) (*Record, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateID(id); err != nil {
		return nil, err
	}
	return s.readRecord(id)
}

// SubmitDecisions applies decisions under the record lock.
func (s *FileStore) SubmitDecisions(ctx context.Context, id string, decisions []Decision) (*Record, error) {
	if err := validateID(id); err != nil {
		return nil, err
	}

	var decisionErr error
	next, err := s.withRecordLock(ctx, id, func(cur *Record) (*Record, error) {
		if cur == nil {
			return nil, ErrNotFound
		}

		revision := cur.Revision
		decisionErr = applyDecisions(cur, decisions, time.Now())
		if decisionErr != nil && cur.Revision == revision {
			return nil, decisionErr
		}
		return cur, nil
	})
	if err != nil {
		return nil, err
	}
	if decisionErr != nil {
		return next, decisionErr
	}
	return next, nil
}

// AcquireLease transitions Ready (or an expired Resuming) to Resuming. The
// mutual exclusion across processes comes from withRecordLock's file lock:
// only one FileStore instance anywhere can be inside this critical section
// for id at a time, so "read status, decide, write" is atomic even though
// the filesystem itself has no native compare-and-swap.
func (s *FileStore) AcquireLease(ctx context.Context, id, owner string, ttl time.Duration) (*Record, error) {
	if err := validateID(id); err != nil {
		return nil, err
	}
	if owner == "" {
		return nil, fmt.Errorf("%w: owner is empty", ErrInvalidArgument)
	}

	return s.withRecordLock(ctx, id, func(cur *Record) (*Record, error) {
		if cur == nil {
			return nil, ErrNotFound
		}
		if err := acquireLeaseOn(cur, owner, ttl, time.Now()); err != nil {
			return nil, err
		}
		return cur, nil
	})
}

// ReleaseLease transitions Resuming back to Ready for owner.
func (s *FileStore) ReleaseLease(ctx context.Context, id, owner string) error {
	if err := validateID(id); err != nil {
		return err
	}
	if owner == "" {
		return fmt.Errorf("%w: owner is empty", ErrInvalidArgument)
	}

	_, err := s.withRecordLock(ctx, id, func(cur *Record) (*Record, error) {
		if cur == nil {
			return nil, ErrNotFound
		}
		if err := releaseLeaseOn(cur, owner, time.Now()); err != nil {
			return nil, err
		}
		return cur, nil
	})
	return err
}

// Complete transitions Resuming to the terminal Completed state for owner.
func (s *FileStore) Complete(ctx context.Context, id, owner string) error {
	if err := validateID(id); err != nil {
		return err
	}
	if owner == "" {
		return fmt.Errorf("%w: owner is empty", ErrInvalidArgument)
	}

	_, err := s.withRecordLock(ctx, id, func(cur *Record) (*Record, error) {
		if cur == nil {
			return nil, ErrNotFound
		}
		if err := completeOn(cur, owner, time.Now()); err != nil {
			return nil, err
		}
		return cur, nil
	})
	return err
}

// List scans every record file in root and returns metadata for those
// belonging to sessionID. Half-written or corrupt files are skipped rather
// than failing the scan, matching checkpoint.FileIterationStore.List.
func (s *FileStore) List(ctx context.Context, sessionID string) ([]*Meta, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if sessionID == "" {
		return nil, fmt.Errorf("%w: session id is empty", ErrInvalidArgument)
	}

	entries, err := os.ReadDir(s.root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("interrupt: read dir %q: %w", s.root, err)
	}

	out := make([]*Meta, 0)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, recordFileExt) || strings.HasSuffix(name, recordTmpExt) {
			continue // skips .lock and .json.tmp
		}

		id := name[:len(name)-len(recordFileExt)]
		r, err := s.readRecord(id)
		if err != nil {
			continue
		}
		if r.SessionID == sessionID {
			out = append(out, metaFrom(r))
		}
	}
	return out, nil
}

// Delete removes the record file for id under the same exclusive lock as
// every other mutation. The companion lock file is left in place on
// purpose (see the type comment): unlinking it would break exclusion for
// an instance already waiting on that inode. Idempotent on an unknown —
// but well-formed — id; a malformed one is rejected rather than resolved,
// so no id can ever name a file outside root.
func (s *FileStore) Delete(ctx context.Context, id string) error {
	return s.withExclusive(ctx, id, func() error {
		if err := os.Remove(s.recordPath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("interrupt: delete %q: %w", id, err)
		}
		return nil
	})
}
