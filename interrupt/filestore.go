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
	// file, so contention is expected to clear in microseconds; a
	// multi-second ceiling only protects against a crashed holder that
	// left a stale lock file (cleaned up once its age exceeds this).
	lockRetryInterval = 2 * time.Millisecond
	lockWaitTimeout   = 5 * time.Second
	staleLockAge      = 30 * time.Second
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
// package exists. It gets this from an OS-level exclusive create
// (O_CREATE|O_EXCL) on a companion `<id>.lock` file: atomic at the
// filesystem level, so two FileStore instances — in the same process or in
// two separate ones — opening the same root directory can never both
// believe they hold it. The lock file is held only for the duration of one
// read-modify-write of the record (a mutex, not the lease itself); the
// lease's own identity and expiry live in the record's LeaseOwner /
// LeaseExpiresAt fields, which is what AcquireLease actually contests.
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

// withRecordLock runs fn while holding both the in-process mutex for id and
// the cross-process file lock, guaranteeing fn is the only critical section
// touching <id>.json across every FileStore instance (any process) pointed
// at this root. fn reads the current record (nil, nil if none exists yet —
// used by Create) and returns the record to persist, or a nil record with a
// non-nil error to abort without writing.
func (s *FileStore) withRecordLock(ctx context.Context, id string, fn func(cur *Record) (*Record, error)) (*Record, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	mu := s.localLock(id)
	mu.Lock()
	defer mu.Unlock()

	release, err := s.acquireFileLock(ctx, id)
	if err != nil {
		return nil, err
	}
	defer release()

	cur, err := s.readRecord(id)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}

	next, err := fn(cur)
	if err != nil {
		return nil, err
	}

	if err := s.writeRecord(next); err != nil {
		return nil, err
	}

	return next, nil
}

// acquireFileLock spins on an O_CREATE|O_EXCL create of <id>.lock until it
// succeeds, the context is canceled, or lockWaitTimeout elapses. A lock
// file older than staleLockAge is treated as abandoned (its holder crashed
// mid-critical-section) and removed so the store cannot deadlock forever on
// a dead process.
func (s *FileStore) acquireFileLock(ctx context.Context, id string) (release func(), err error) {
	path := s.lockPath(id)
	deadline := time.Now().Add(lockWaitTimeout)

	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, filestoreFilePerm)
		if err == nil {
			_ = f.Close()
			return func() { _ = os.Remove(path) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("interrupt: create lock %q: %w", path, err)
		}

		if info, statErr := os.Stat(path); statErr == nil && time.Since(info.ModTime()) > staleLockAge {
			_ = os.Remove(path)
			continue
		}

		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if time.Now().After(deadline) {
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
	if id == "" {
		return nil, fmt.Errorf("%w: id is empty", ErrInvalidArgument)
	}
	return s.readRecord(id)
}

// SubmitDecisions applies decisions under the record lock.
func (s *FileStore) SubmitDecisions(ctx context.Context, id string, decisions []Decision) (*Record, error) {
	if id == "" {
		return nil, fmt.Errorf("%w: id is empty", ErrInvalidArgument)
	}

	return s.withRecordLock(ctx, id, func(cur *Record) (*Record, error) {
		if cur == nil {
			return nil, ErrNotFound
		}
		if err := applyDecisions(cur, decisions, time.Now()); err != nil {
			return nil, err
		}
		return cur, nil
	})
}

// AcquireLease transitions Ready (or an expired Resuming) to Resuming. The
// mutual exclusion across processes comes from withRecordLock's file lock:
// only one FileStore instance anywhere can be inside this critical section
// for id at a time, so "read status, decide, write" is atomic even though
// the filesystem itself has no native compare-and-swap.
func (s *FileStore) AcquireLease(ctx context.Context, id, owner string, ttl time.Duration) (*Record, error) {
	if id == "" || owner == "" {
		return nil, fmt.Errorf("%w: id and owner are required", ErrInvalidArgument)
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
	if id == "" || owner == "" {
		return fmt.Errorf("%w: id and owner are required", ErrInvalidArgument)
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
	if id == "" || owner == "" {
		return fmt.Errorf("%w: id and owner are required", ErrInvalidArgument)
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

// Delete removes the record file (and any stale lock file) for id.
// Idempotent on unknown id.
func (s *FileStore) Delete(_ context.Context, id string) error {
	if id == "" {
		return nil
	}

	mu := s.localLock(id)
	mu.Lock()
	defer mu.Unlock()

	if err := os.Remove(s.recordPath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("interrupt: delete %q: %w", id, err)
	}
	_ = os.Remove(s.lockPath(id))
	s.locks.Delete(id)
	return nil
}
