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
	"maps"
	"sync"
	"time"

	"github.com/vogo/vage/schema"
)

// MapSessionStore is an in-process SessionStore backed by a map. It is the
// default for tests and short-lived integrations; FileSessionStore is the
// recommended persistent option.
//
// All operations are guarded by a single sync.RWMutex; this is deliberately
// the simplest correct strategy. Per-session locking would buy nothing for
// the workloads SessionStore is designed for (read/append at conversation
// pace, not high-throughput streaming).
type MapSessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*sessionRecord
	order    []string // insertion order; List honours it
}

type sessionRecord struct {
	meta   Session
	events []schema.Event
	state  map[string]any
}

// Compile-time check.
var _ SessionStore = (*MapSessionStore)(nil)

// NewMapSessionStore constructs an empty in-memory store.
func NewMapSessionStore() *MapSessionStore {
	return &MapSessionStore{
		sessions: make(map[string]*sessionRecord),
	}
}

// --- SessionMetaStore ---

// Create inserts s into the store.
func (s *MapSessionStore) Create(ctx context.Context, in *Session) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if in == nil {
		return fmt.Errorf("%w: session is nil", ErrInvalidArgument)
	}
	if err := validateID(in.ID); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.sessions[in.ID]; ok {
		return ErrSessionExists
	}

	rec := &sessionRecord{
		meta:   *cloneSession(in),
		events: nil,
		state:  make(map[string]any),
	}
	if rec.meta.State == "" {
		rec.meta.State = StateActive
	}

	now := time.Now()
	if rec.meta.CreatedAt.IsZero() {
		rec.meta.CreatedAt = now
	}
	rec.meta.UpdatedAt = now

	s.sessions[in.ID] = rec
	s.order = append(s.order, in.ID)

	return nil
}

// Get returns a copy of the Session metadata for id.
func (s *MapSessionStore) Get(ctx context.Context, id string) (*Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	rec, ok := s.sessions[id]
	if !ok {
		return nil, ErrSessionNotFound
	}
	return cloneSession(&rec.meta), nil
}

// Update replaces the metadata for s.ID, refreshing UpdatedAt.
func (s *MapSessionStore) Update(ctx context.Context, in *Session) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if in == nil {
		return fmt.Errorf("%w: session is nil", ErrInvalidArgument)
	}
	if err := validateID(in.ID); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	rec, ok := s.sessions[in.ID]
	if !ok {
		return ErrSessionNotFound
	}

	cloned := cloneSession(in)
	cloned.UpdatedAt = time.Now()
	if cloned.CreatedAt.IsZero() {
		cloned.CreatedAt = rec.meta.CreatedAt
	}
	rec.meta = *cloned
	return nil
}

// Delete removes the session and all its associated events/state.
func (s *MapSessionStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.sessions[id]; !ok {
		return nil
	}
	delete(s.sessions, id)
	for i, oid := range s.order {
		if oid == id {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
	return nil
}

// List returns sessions matching f in insertion order.
func (s *MapSessionStore) List(ctx context.Context, f SessionFilter) ([]*Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]*Session, 0, len(s.order))
	skipped := 0
	for _, id := range s.order {
		rec := s.sessions[id]
		if !sessionMatches(&rec.meta, f) {
			continue
		}
		if f.Offset > 0 && skipped < f.Offset {
			skipped++
			continue
		}
		out = append(out, cloneSession(&rec.meta))
		if f.Limit > 0 && len(out) >= f.Limit {
			break
		}
	}
	return out, nil
}

func sessionMatches(s *Session, f SessionFilter) bool {
	if f.UserID != "" && s.UserID != f.UserID {
		return false
	}
	if f.AgentID != "" && s.AgentID != f.AgentID {
		return false
	}
	if f.State != "" && s.State != f.State {
		return false
	}
	return true
}

// --- SessionEventStore ---

// AppendEvent appends e to the session's event log.
func (s *MapSessionStore) AppendEvent(ctx context.Context, id string, e schema.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	rec, ok := s.sessions[id]
	if !ok {
		return ErrSessionNotFound
	}
	rec.events = append(rec.events, e)
	rec.meta.UpdatedAt = time.Now()
	return nil
}

// ListEvents returns the entire event log for id in append order.
func (s *MapSessionStore) ListEvents(ctx context.Context, id string) ([]schema.Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	rec, ok := s.sessions[id]
	if !ok {
		return nil, ErrSessionNotFound
	}
	out := make([]schema.Event, len(rec.events))
	copy(out, rec.events)
	return out, nil
}

// --- SessionStateStore ---

// GetState returns (value, true) when key is present, (nil, false) when absent.
func (s *MapSessionStore) GetState(ctx context.Context, id, key string) (any, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	rec, ok := s.sessions[id]
	if !ok {
		return nil, false, ErrSessionNotFound
	}
	v, present := rec.state[key]
	return v, present, nil
}

// SetState writes (key, value) into the session's state.
func (s *MapSessionStore) SetState(ctx context.Context, id, key string, value any) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	rec, ok := s.sessions[id]
	if !ok {
		return ErrSessionNotFound
	}
	rec.state[key] = value
	rec.meta.UpdatedAt = time.Now()
	return nil
}

// DeleteState removes key from the session's state. Idempotent.
func (s *MapSessionStore) DeleteState(ctx context.Context, id, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	rec, ok := s.sessions[id]
	if !ok {
		return ErrSessionNotFound
	}
	if _, present := rec.state[key]; present {
		delete(rec.state, key)
		rec.meta.UpdatedAt = time.Now()
	}
	return nil
}

// ListState returns a copy of the session's state map.
func (s *MapSessionStore) ListState(ctx context.Context, id string) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	rec, ok := s.sessions[id]
	if !ok {
		return nil, ErrSessionNotFound
	}
	out := make(map[string]any, len(rec.state))
	maps.Copy(out, rec.state)
	return out, nil
}
