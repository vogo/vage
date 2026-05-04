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
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// metricsFilename is the file FileMetricsStore writes per session.
// Lives next to meta.json / events.jsonl / state.json so a session
// directory holds every per-session subsystem in one place — and
// FileSessionStore.Delete (os.RemoveAll'ing <root>/<id>) wipes it
// alongside the rest without explicit coordination.
const metricsFilename = "metrics.json"

// FileMetricsStore persists SessionMetrics under
// <root>/<session_id>/metrics.json. The on-disk format is the same
// JSON shape exposed on the wire by HTTP /v1/sessions/{id}/metrics, so
// ops can `cat metrics.json` and get a meaningful answer.
//
// Concurrency: writes against the same session id are serialised via a
// per-session mutex (allocated lazily). Reads bypass the lock — a
// reader racing with Update may observe either snapshot but never a
// partially written file because writeJSONAtomic does a tmp+rename.
//
// Cross-process coordination is NOT provided.
type FileMetricsStore struct {
	root  string
	locks sync.Map // map[string]*sync.Mutex
	clock func() time.Time
}

// Compile-time check.
var _ MetricsStore = (*FileMetricsStore)(nil)

// NewFileMetricsStore constructs a store rooted at the given directory.
// The directory is created (with parents) if it does not exist. Returns
// ErrInvalidArgument when root is empty.
func NewFileMetricsStore(root string) (*FileMetricsStore, error) {
	if root == "" {
		return nil, fmt.Errorf("%w: root directory is empty", ErrInvalidArgument)
	}
	if err := os.MkdirAll(root, filestoreDirPerm); err != nil {
		return nil, fmt.Errorf("session: create metrics root %q: %w", root, err)
	}
	return &FileMetricsStore{
		root:  root,
		clock: time.Now,
	}, nil
}

// Root returns the configured root directory; useful in tests.
func (s *FileMetricsStore) Root() string { return s.root }

// WithClock overrides the timestamp source. Should only be called
// during test setup before any Update.
func (s *FileMetricsStore) WithClock(clock func() time.Time) *FileMetricsStore {
	s.clock = clock
	return s
}

func (s *FileMetricsStore) lockFor(id string) *sync.Mutex {
	v, _ := s.locks.LoadOrStore(id, &sync.Mutex{})
	return v.(*sync.Mutex)
}

func (s *FileMetricsStore) metricsPath(sessionID string) string {
	return filepath.Join(s.root, sessionID, metricsFilename)
}

// Get reads metrics.json off disk and decodes it. Returns
// ErrMetricsNotFound when the file does not exist (including the case
// where the session directory itself is missing — same outcome from a
// caller's perspective).
func (s *FileMetricsStore) Get(ctx context.Context, sessionID string) (*SessionMetrics, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateID(sessionID); err != nil {
		return nil, err
	}

	path := s.metricsPath(sessionID)
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: session_id=%q", ErrMetricsNotFound, sessionID)
		}
		return nil, fmt.Errorf("session: open metrics %q: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	var out SessionMetrics
	if err := json.NewDecoder(f).Decode(&out); err != nil {
		return nil, fmt.Errorf("session: decode metrics %q: %w", path, err)
	}
	return &out, nil
}

// Update reads the current metrics (or zero-initialises), invokes fn
// under the per-session lock, then atomically writes the result back.
// Creates <root>/<session_id>/ if needed so callers do not have to
// pre-create the directory through SessionStore.Create.
func (s *FileMetricsStore) Update(ctx context.Context, sessionID string, fn func(*SessionMetrics)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateID(sessionID); err != nil {
		return err
	}

	mu := s.lockFor(sessionID)
	mu.Lock()
	defer mu.Unlock()

	dir := filepath.Join(s.root, sessionID)
	if err := os.MkdirAll(dir, filestoreDirPerm); err != nil {
		return fmt.Errorf("session: mkdir %q: %w", dir, err)
	}

	path := s.metricsPath(sessionID)
	current, err := loadMetricsFile(path)
	if err != nil {
		return err
	}

	updated := applyUpdate(sessionID, current, fn, s.clock())
	if err := writeJSONAtomic(path, updated); err != nil {
		return fmt.Errorf("session: write metrics %q: %w", path, err)
	}
	return nil
}

// Delete removes the metrics.json file. Idempotent — calling on a
// session without metrics is a no-op. Does not remove the parent
// directory (FileSessionStore.Delete owns that and may want to leave
// the dir intact for other subsystems).
func (s *FileMetricsStore) Delete(ctx context.Context, sessionID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateID(sessionID); err != nil {
		return err
	}

	mu := s.lockFor(sessionID)
	mu.Lock()
	defer mu.Unlock()

	if err := os.Remove(s.metricsPath(sessionID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("session: remove metrics: %w", err)
	}
	return nil
}

// loadMetricsFile reads and decodes metrics.json. Returns nil + nil
// when the file does not exist so applyUpdate can mint a fresh record.
func loadMetricsFile(path string) (*SessionMetrics, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("session: open metrics %q: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	var out SessionMetrics
	if err := json.NewDecoder(f).Decode(&out); err != nil {
		return nil, fmt.Errorf("session: decode metrics %q: %w", path, err)
	}
	return &out, nil
}
