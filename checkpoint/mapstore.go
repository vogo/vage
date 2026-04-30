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

package checkpoint

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// MapIterationStore is the in-memory IterationStore implementation. It
// is the default for tests and short-lived integrations. A single
// sync.RWMutex guards the entire map: per-session locking would buy
// nothing at conversation pace and would complicate the
// across-sessions-but-not-within-one Save scenario.
type MapIterationStore struct {
	mu   sync.RWMutex
	data map[string][]*Checkpoint // sessionID -> ordered by Sequence
	seq  map[string]int           // sessionID -> last assigned Sequence
}

// Compile-time check.
var _ IterationStore = (*MapIterationStore)(nil)

// NewMapIterationStore constructs an empty in-memory store.
func NewMapIterationStore() *MapIterationStore {
	return &MapIterationStore{
		data: make(map[string][]*Checkpoint),
		seq:  make(map[string]int),
	}
}

// Save assigns Sequence/ID/CreatedAt and appends a deep copy.
func (s *MapIterationStore) Save(ctx context.Context, cp *Checkpoint) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if cp == nil {
		return fmt.Errorf("%w: checkpoint is nil", ErrInvalidArgument)
	}
	if cp.SessionID == "" {
		return fmt.Errorf("%w: session id is empty", ErrInvalidArgument)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.seq[cp.SessionID]++
	cp.Sequence = s.seq[cp.SessionID]
	cp.ID = generateID()
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = time.Now()
	}

	s.data[cp.SessionID] = append(s.data[cp.SessionID], cloneCheckpoint(cp))
	return nil
}

// Load returns a deep copy of the requested checkpoint.
func (s *MapIterationStore) Load(ctx context.Context, sessionID, id string) (*Checkpoint, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if sessionID == "" {
		return nil, fmt.Errorf("%w: session id is empty", ErrInvalidArgument)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	list := s.data[sessionID]
	if len(list) == 0 {
		return nil, ErrCheckpointNotFound
	}

	if id == "" {
		return cloneCheckpoint(list[len(list)-1]), nil
	}
	for _, cp := range list {
		if cp.ID == id {
			return cloneCheckpoint(cp), nil
		}
	}
	return nil, ErrCheckpointNotFound
}

// List returns metadata only, in Sequence order.
func (s *MapIterationStore) List(ctx context.Context, sessionID string) ([]*CheckpointMeta, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if sessionID == "" {
		return nil, fmt.Errorf("%w: session id is empty", ErrInvalidArgument)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	list := s.data[sessionID]
	out := make([]*CheckpointMeta, 0, len(list))
	for _, cp := range list {
		out = append(out, metaFrom(cp))
	}
	return out, nil
}

// Delete drops every checkpoint for sessionID.
func (s *MapIterationStore) Delete(_ context.Context, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, sessionID)
	delete(s.seq, sessionID)
	return nil
}
