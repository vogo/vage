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

	"github.com/vogo/vage/schema"
)

// SessionFilter narrows the result of List. Zero-valued fields are ignored
// so that the empty filter returns every session in insertion order.
type SessionFilter struct {
	UserID  string       // exact match; empty means "any"
	AgentID string       // exact match; empty means "any"
	State   SessionState // exact match; empty means "any"
	Limit   int          // 0 means "no limit"
	Offset  int          // skip this many matches before collecting
}

// SessionMetaStore handles Session metadata: lifecycle CRUD plus a filtered
// list. Implementations must be safe for concurrent use.
type SessionMetaStore interface {
	// Create inserts s. Returns ErrSessionExists if s.ID already exists, or
	// ErrInvalidArgument when s is nil or s.ID fails validateID.
	Create(ctx context.Context, s *Session) error

	// Get returns a copy of the Session with the given id, or
	// ErrSessionNotFound if absent.
	Get(ctx context.Context, id string) (*Session, error)

	// Update overwrites the existing Session. Returns ErrSessionNotFound if
	// the id is unknown. Implementations must refresh UpdatedAt to time.Now().
	Update(ctx context.Context, s *Session) error

	// Delete removes the session and any associated events/state. Idempotent:
	// deleting an unknown id returns nil.
	Delete(ctx context.Context, id string) error

	// List returns sessions matching f. The order of results is
	// implementation-defined but stable within a single call.
	List(ctx context.Context, f SessionFilter) ([]*Session, error)
}

// SessionEventStore handles the append-only event log of a Session.
// Implementations must be safe for concurrent use.
//
// Event listing is intentionally minimal in the MVP; future filtering
// (type / time range / paging) will be added as a new ListEventsQuery method
// rather than by changing this signature.
type SessionEventStore interface {
	// AppendEvent appends e to the session's event log. Returns
	// ErrSessionNotFound if the id is unknown.
	AppendEvent(ctx context.Context, id string, e schema.Event) error

	// ListEvents returns every event recorded for the session in append
	// order. Returns ErrSessionNotFound if the id is unknown. The returned
	// slice is a fresh allocation owned by the caller.
	ListEvents(ctx context.Context, id string) ([]schema.Event, error)
}

// SessionStateStore handles the structured key/value state of a Session.
// State has overwrite semantics — Set replaces, Delete is idempotent, and
// missing keys are not errors. Implementations must be safe for concurrent use.
type SessionStateStore interface {
	// GetState returns (value, true, nil) when the key is present, (nil,
	// false, nil) when absent. Returns ErrSessionNotFound if the session
	// itself is unknown.
	GetState(ctx context.Context, id, key string) (any, bool, error)

	// SetState writes (key, value) into the session's state. Returns
	// ErrSessionNotFound if the session is unknown.
	SetState(ctx context.Context, id, key string, value any) error

	// DeleteState removes key from the session's state. Idempotent on a
	// missing key. Returns ErrSessionNotFound if the session is unknown.
	DeleteState(ctx context.Context, id, key string) error

	// ListState returns a fresh copy of the entire state map. Returns
	// ErrSessionNotFound if the session is unknown. An empty state yields a
	// non-nil empty map.
	ListState(ctx context.Context, id string) (map[string]any, error)
}

// SessionStore composes the three sub-interfaces. All built-in
// implementations satisfy this composite contract; consumers that need only
// a subset (for example, SessionHook only writes events) should depend on
// the narrower sub-interface.
type SessionStore interface {
	SessionMetaStore
	SessionEventStore
	SessionStateStore
}
