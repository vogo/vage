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

// Package session provides a first-class Session entity for the vage agent
// framework: identity, append-only events, structured state KV, and pluggable
// storage backends. It promotes the prior "session_id is just a string label
// on memory entries" model into an addressable entity so that conversations
// can be persisted, listed, and resumed across runs.
//
// The package intentionally excludes checkpoint/snapshot semantics; those are
// a follow-up iteration. Events are append-only; structured state has
// overwrite semantics.
package session

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"maps"
	"regexp"
	"strconv"
	"time"
)

// SessionState enumerates the lifecycle states a Session may occupy.
type SessionState string

// SessionState constants.
const (
	StateActive    SessionState = "active"
	StatePaused    SessionState = "paused"
	StateCompleted SessionState = "completed"
	StateFailed    SessionState = "failed"
)

// IDMaxLen caps the length of a session id. Mirrors the limit applied by
// vv/traces/tracelog so the same id is reusable across both subsystems
// without re-sanitisation.
const IDMaxLen = 128

// idPatternRaw is the regular-expression source for a valid session id.
const idPatternRaw = `^[A-Za-z0-9._-]{1,128}$`

// IDPattern is the compiled validation regex for a session id. Exported so
// integrators can validate ids at the edge of their system before calling
// the store, getting the same answer the store would give.
var IDPattern = regexp.MustCompile(idPatternRaw)

// Session is the metadata view of a persistent agent conversation. Events
// and structured state KV are addressable separately via SessionStore — the
// Session struct itself only carries identity and lifecycle metadata so that
// Get is O(1) regardless of how many events the session has accumulated.
type Session struct {
	ID        string         `json:"id"`
	AgentID   string         `json:"agent_id,omitempty"`
	UserID    string         `json:"user_id,omitempty"`
	Title     string         `json:"title,omitempty"`
	State     SessionState   `json:"state"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

// New constructs a Session with the given non-empty id, sets State=Active,
// and seeds CreatedAt/UpdatedAt to time.Now(). It panics when id == "" so
// that callers explicitly choose between supplying an external id and
// calling GenerateID themselves — silently auto-generating an id at this
// boundary masks integration bugs.
func New(id string) *Session {
	if id == "" {
		panic("session: New requires a non-empty id; call GenerateID() to mint one")
	}

	now := time.Now()

	return &Session{
		ID:        id,
		State:     StateActive,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// GenerateID returns a sortable, filesystem-safe session id of the form
// "<unix-nanos>-<8-byte-hex>". The character set is constrained to
// [A-Za-z0-9._-] (length ≤ IDMaxLen), matching tracelog's session-id rules.
//
// On the rare event that crypto/rand fails, GenerateID falls back to a
// timestamp-only id, which is still valid but loses uniqueness across
// processes that mint ids in the same nanosecond.
func GenerateID() string {
	prefix := strconv.FormatInt(time.Now().UnixNano(), 10)

	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return prefix
	}

	return prefix + "-" + hex.EncodeToString(buf[:])
}

// validateID checks an id against IDPattern and returns ErrInvalidArgument
// (wrapped) on rejection. Used by every store implementation so MapStore and
// FileStore agree on what a valid id looks like. Aside from the regex, the
// special filesystem names "." and ".." are rejected explicitly so that
// FileSessionStore cannot be tricked into operating on a parent or current
// directory even though both names match the regex.
func validateID(id string) error {
	if id == "" {
		return fmt.Errorf("%w: id is empty", ErrInvalidArgument)
	}

	if len(id) > IDMaxLen {
		return fmt.Errorf("%w: id length %d exceeds %d", ErrInvalidArgument, len(id), IDMaxLen)
	}

	if id == "." || id == ".." {
		return fmt.Errorf("%w: id %q is reserved", ErrInvalidArgument, id)
	}

	if !IDPattern.MatchString(id) {
		return fmt.Errorf("%w: id %q does not match %s", ErrInvalidArgument, id, idPatternRaw)
	}

	return nil
}

// cloneSession returns a deep-enough copy of s so that callers cannot mutate
// store-internal state through the returned pointer. Metadata map is copied
// shallowly (values are any; deep clone is impractical and unnecessary
// because callers conventionally treat metadata values as immutable).
func cloneSession(s *Session) *Session {
	if s == nil {
		return nil
	}

	out := *s
	if s.Metadata != nil {
		out.Metadata = make(map[string]any, len(s.Metadata))
		maps.Copy(out.Metadata, s.Metadata)
	}

	return &out
}
