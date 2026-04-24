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

// Package todo implements the todo_write built-in tool: a session-scoped,
// in-memory task tracker that the LLM agent maintains during a multi-step
// task. The list lives only for the lifetime of the run's sessionID; it is
// intentionally not persisted.
package todo

import (
	"errors"
	"fmt"
	"sync"
)

// maxItemsPerList caps how many todo items a single list can carry. The limit
// guards against prompt-bombing and UI avalanche; 100 is far above the
// typical 3-8 items per task observed in multi-step workflows.
const maxItemsPerList = 100

// Status is the lifecycle state of a single todo item.
type Status string

// Valid Status values.
const (
	StatusPending    Status = "pending"
	StatusInProgress Status = "in_progress"
	StatusCompleted  Status = "completed"
)

// Errors returned by Store.Apply.
var (
	ErrTooManyInProgress = errors.New("only one in_progress item allowed")
	ErrTooManyItems      = fmt.Errorf("too many items (max %d)", maxItemsPerList)
	ErrEmptyContent      = errors.New("item content must not be empty")
	ErrEmptyActiveForm   = errors.New("item active_form must not be empty")
	ErrInvalidStatus     = errors.New("item status must be one of pending|in_progress|completed")
)

// Item is the canonical form of a single todo list entry.
type Item struct {
	ID         string `json:"id"`
	Content    string `json:"content"`
	ActiveForm string `json:"active_form"`
	Status     Status `json:"status"`
}

// Snapshot is the immutable result of a single Apply call.
type Snapshot struct {
	Version int64  `json:"version"`
	Items   []Item `json:"items"`
}

// Store holds one todo list per sessionID. The zero value is not usable;
// callers must construct via NewStore.
type Store struct {
	mu        sync.Mutex
	sessions  map[string]*Snapshot
	idCounter uint64
}

// NewStore returns an empty Store ready for use.
func NewStore() *Store {
	return &Store{sessions: make(map[string]*Snapshot)}
}

// Apply validates the incoming items, merges server-assigned IDs, bumps the
// version, and returns a safe copy of the new snapshot.
//
// Semantics:
//   - nil and []Item{} are both treated as "clear the list"; version still
//     increments so downstream consumers see a state change.
//   - Items with an ID matching a previous entry keep that ID; unmatched or
//     missing IDs are assigned the next "todo_<N>" from the monotonic counter.
//   - At most one item may be StatusInProgress; violations return
//     ErrTooManyInProgress and leave the store state unchanged.
func (s *Store) Apply(sessionID string, items []Item) (Snapshot, error) {
	if sessionID == "" {
		return Snapshot{}, errors.New("sessionID is required")
	}

	if err := validateItems(items); err != nil {
		return Snapshot{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	prev := s.sessions[sessionID]

	prevIDs := map[string]struct{}{}
	if prev != nil {
		for _, it := range prev.Items {
			prevIDs[it.ID] = struct{}{}
		}
	}

	merged := make([]Item, len(items))
	for i, in := range items {
		m := Item{
			Content:    in.Content,
			ActiveForm: in.ActiveForm,
			Status:     in.Status,
		}
		if in.ID != "" {
			if _, ok := prevIDs[in.ID]; ok {
				m.ID = in.ID
			}
		}
		if m.ID == "" {
			s.idCounter++
			m.ID = fmt.Sprintf("todo_%d", s.idCounter)
		}
		merged[i] = m
	}

	var version int64 = 1
	if prev != nil {
		version = prev.Version + 1
	}

	snap := &Snapshot{Version: version, Items: merged}
	s.sessions[sessionID] = snap

	return copySnapshot(snap), nil
}

// Get returns a safe copy of the latest snapshot for a session, or an empty
// snapshot (Version=0, Items=nil) if the session has never written.
func (s *Store) Get(sessionID string) Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()

	snap := s.sessions[sessionID]
	if snap == nil {
		return Snapshot{}
	}

	return copySnapshot(snap)
}

// Clear removes the snapshot for a session. Reserved for future checkpoint /
// session-end integration; not invoked by the tool handler itself.
func (s *Store) Clear(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, sessionID)
}

func validateItems(items []Item) error {
	if len(items) > maxItemsPerList {
		return ErrTooManyItems
	}

	var inProgress int
	for _, it := range items {
		if it.Content == "" {
			return ErrEmptyContent
		}
		if it.ActiveForm == "" {
			return ErrEmptyActiveForm
		}
		switch it.Status {
		case StatusPending, StatusCompleted:
		case StatusInProgress:
			inProgress++
		default:
			return ErrInvalidStatus
		}
	}

	if inProgress > 1 {
		return ErrTooManyInProgress
	}

	return nil
}

func copySnapshot(snap *Snapshot) Snapshot {
	out := Snapshot{Version: snap.Version}
	if len(snap.Items) > 0 {
		out.Items = make([]Item, len(snap.Items))
		copy(out.Items, snap.Items)
	}
	return out
}
