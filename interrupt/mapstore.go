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
	"fmt"
	"sync"
	"time"
)

// MapStore is the in-memory Store implementation: the default for unit
// tests and single-process integrations. A single mutex guards the whole
// map — interrupts are rare, human-decision-paced events, so per-record
// locking buys nothing.
//
// MapStore provides no cross-process guarantees: two MapStore values never
// share state, by construction, so it cannot participate in the
// cross-process conformance scenarios FileStore is exercised against. Use
// FileStore when a resume may happen from a different process than the
// suspend.
type MapStore struct {
	mu   sync.Mutex
	data map[string]*Record
}

// Compile-time check.
var _ Store = (*MapStore)(nil)

// NewMapStore constructs an empty in-memory store.
func NewMapStore() *MapStore {
	return &MapStore{data: make(map[string]*Record)}
}

// Create validates, assigns identity/audit fields, and stores a deep copy.
func (s *MapStore) Create(ctx context.Context, rec *Record) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if err := validateNewRecord(rec); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	rec.Version = CurrentVersion
	rec.ID = generateID()
	rec.Revision = 1
	rec.CreatedAt = now
	rec.UpdatedAt = now
	rec.Status = initialStatus(rec.Pending)

	s.data[rec.ID] = cloneRecord(rec)
	return nil
}

// Get returns a deep copy of the requested record.
func (s *MapStore) Get(ctx context.Context, id string) (*Record, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateID(id); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	r, ok := s.data[id]
	if !ok {
		return nil, ErrNotFound
	}
	if r.Version != CurrentVersion {
		return nil, ErrUnknownVersion
	}
	return cloneRecord(r), nil
}

// SubmitDecisions applies decisions in order, retaining the valid prefix
// when a later decision is rejected.
func (s *MapStore) SubmitDecisions(ctx context.Context, id string, decisions []Decision) (*Record, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateID(id); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	r, ok := s.data[id]
	if !ok {
		return nil, ErrNotFound
	}
	if r.Status == StatusCompleted {
		return nil, ErrAlreadyCompleted
	}

	if err := applyDecisions(r, decisions, time.Now()); err != nil {
		return cloneRecord(r), err
	}

	s.data[id] = r
	return cloneRecord(r), nil
}

// AcquireLease transitions Ready (or an expired Resuming) to Resuming.
func (s *MapStore) AcquireLease(ctx context.Context, id, owner string, ttl time.Duration) (*Record, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateID(id); err != nil {
		return nil, err
	}
	if owner == "" {
		return nil, fmt.Errorf("%w: owner is empty", ErrInvalidArgument)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	r, ok := s.data[id]
	if !ok {
		return nil, ErrNotFound
	}

	if err := acquireLeaseOn(r, owner, ttl, time.Now()); err != nil {
		return nil, err
	}

	s.data[id] = r
	return cloneRecord(r), nil
}

// ReleaseLease transitions Resuming back to Ready for the current owner.
func (s *MapStore) ReleaseLease(ctx context.Context, id, owner string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateID(id); err != nil {
		return err
	}
	if owner == "" {
		return fmt.Errorf("%w: owner is empty", ErrInvalidArgument)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	r, ok := s.data[id]
	if !ok {
		return ErrNotFound
	}

	if err := releaseLeaseOn(r, owner, time.Now()); err != nil {
		return err
	}

	s.data[id] = r
	return nil
}

// Complete transitions Resuming to the terminal Completed state.
func (s *MapStore) Complete(ctx context.Context, id, owner string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateID(id); err != nil {
		return err
	}
	if owner == "" {
		return fmt.Errorf("%w: owner is empty", ErrInvalidArgument)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	r, ok := s.data[id]
	if !ok {
		return ErrNotFound
	}

	if err := completeOn(r, owner, time.Now()); err != nil {
		return err
	}

	s.data[id] = r
	return nil
}

// List returns metadata for every record belonging to sessionID.
func (s *MapStore) List(ctx context.Context, sessionID string) ([]*Meta, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if sessionID == "" {
		return nil, fmt.Errorf("%w: session id is empty", ErrInvalidArgument)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]*Meta, 0)
	for _, r := range s.data {
		if r.SessionID == sessionID {
			out = append(out, metaFrom(r))
		}
	}
	return out, nil
}

// Delete removes the record identified by id. Idempotent on an unknown — but
// well-formed — id; a malformed one is rejected, matching FileStore, whose
// rejection is a path-safety requirement rather than a stylistic one.
func (s *MapStore) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateID(id); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, id)
	return nil
}
