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
	"fmt"
	"sync"
	"time"
)

// MapMetricsStore is the in-memory MetricsStore used by tests and
// stand-alone integrations. The on-disk equivalent is FileMetricsStore;
// both apply the same applyUpdate body so they exhibit identical
// counter and timestamp behaviour.
//
// All operations hold a single sync.RWMutex — the per-session
// granularity FileMetricsStore uses would buy nothing for the in-process
// pace at which metrics typically tick (a few updates per Run).
type MapMetricsStore struct {
	mu      sync.RWMutex
	records map[string]*SessionMetrics
	clock   func() time.Time
}

// Compile-time check.
var _ MetricsStore = (*MapMetricsStore)(nil)

// NewMapMetricsStore constructs an empty in-memory metrics store. The
// store uses time.Now() for first-seen / last-updated stamps; tests
// that require deterministic timestamps can call WithClock.
func NewMapMetricsStore() *MapMetricsStore {
	return &MapMetricsStore{
		records: make(map[string]*SessionMetrics),
		clock:   time.Now,
	}
}

// WithClock overrides the clock used for FirstSeen / LastUpdated. The
// override is in-place (no copy) so it should only be called during
// test setup before any Update.
func (s *MapMetricsStore) WithClock(clock func() time.Time) *MapMetricsStore {
	s.clock = clock
	return s
}

// Get returns a copy of the metrics record. Returns ErrMetricsNotFound
// when no Update has yet been recorded for sessionID.
func (s *MapMetricsStore) Get(ctx context.Context, sessionID string) (*SessionMetrics, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateID(sessionID); err != nil {
		return nil, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	rec, ok := s.records[sessionID]
	if !ok {
		return nil, fmt.Errorf("%w: session_id=%q", ErrMetricsNotFound, sessionID)
	}
	return cloneMetrics(rec), nil
}

// Update applies fn under the store-wide write lock and persists the
// result. fn observes a non-nil pointer; the store handles the
// zero-value bootstrap and timestamp stamping.
func (s *MapMetricsStore) Update(ctx context.Context, sessionID string, fn func(*SessionMetrics)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateID(sessionID); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	current := s.records[sessionID]
	updated := applyUpdate(sessionID, cloneMetrics(current), fn, s.clock())
	s.records[sessionID] = updated
	return nil
}

// Delete removes sessionID's record. Idempotent — deleting a missing
// id returns nil so Resume + cleanup paths can call it unconditionally.
func (s *MapMetricsStore) Delete(ctx context.Context, sessionID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateID(sessionID); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.records, sessionID)
	return nil
}
