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

// Package workspace provides a per-session, persistent plan + notes scratchpad.
//
// The shape is intentionally narrow:
//
//   - plan.md is a single human-readable Markdown string. Replace, don't patch.
//   - notes/ is a flat directory of <name>.md files indexed by name only.
//
// Anything richer (artifacts, scratch subtasks, schema-validated plan models)
// is out of scope for this MVP. The package mirrors the conventions of
// vage/session: name-pattern validation, atomic file writes, 0o700/0o600
// permissions, and per-session in-process locking.
package workspace

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"
)

// MaxPlanBytes caps the plan.md size to prevent prompt explosion. plan.md is
// designed to be a strategy doc, not a journal — 64 KiB is well above any
// reasonable plan and forces the LLM to summarise rather than dump.
const MaxPlanBytes = 64 * 1024

// MaxNoteBytes caps a single note size.
const MaxNoteBytes = 32 * 1024

// MaxNoteCount caps how many notes a session may keep. The cap protects the
// notes index injected via WorkspaceSource from blowing up.
const MaxNoteCount = 200

// Errors returned from Workspace methods.
var (
	// ErrInvalidName is returned when a note name fails validation.
	ErrInvalidName = errors.New("workspace: invalid note name")
	// ErrInvalidSession is returned when the session id fails validation.
	ErrInvalidSession = errors.New("workspace: invalid session id")
	// ErrTooLarge is returned when a payload exceeds MaxPlanBytes / MaxNoteBytes.
	ErrTooLarge = errors.New("workspace: payload exceeds limit")
	// ErrTooManyNotes is returned by WriteNote when adding the note would push
	// the session past MaxNoteCount.
	ErrTooManyNotes = errors.New("workspace: note count exceeds limit")
)

// NoteInfo is the index entry returned by ListNotes.
type NoteInfo struct {
	Name      string    `json:"name"`
	Bytes     int       `json:"bytes"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Workspace is the per-session plan + notes scratchpad.
//
// All methods take SessionID as the first arg; the implementation owns path
// construction so callers cannot escape the session sandbox.
//
// Implementations must be safe for concurrent use across distinct sessions.
// Writes against the same session are serialised inside the implementation.
type Workspace interface {
	// ReadPlan returns plan.md content. Empty string + nil error when the
	// session has no plan yet (the file does not exist). Errors are limited
	// to genuine IO failures and invalid-id rejection.
	ReadPlan(ctx context.Context, sessionID string) (string, error)

	// WritePlan replaces plan.md with content. content must be ≤
	// MaxPlanBytes; longer payloads return ErrTooLarge with no write. Empty
	// content clears the plan (deletes the file).
	WritePlan(ctx context.Context, sessionID, content string) error

	// ReadNote returns notes/<name>.md content. Empty string + nil error
	// when the note does not exist. Returns ErrInvalidName for malformed
	// names (the file is never read).
	ReadNote(ctx context.Context, sessionID, name string) (string, error)

	// WriteNote writes notes/<name>.md. content must be ≤ MaxNoteBytes;
	// total notes must remain ≤ MaxNoteCount. Empty content deletes the note.
	WriteNote(ctx context.Context, sessionID, name, content string) error

	// ListNotes returns notes ordered by UpdatedAt DESC.
	ListNotes(ctx context.Context, sessionID string) ([]NoteInfo, error)

	// Delete removes the entire workspace (plan + notes/) for a session.
	// Idempotent — deleting a session that has no workspace is a no-op.
	Delete(ctx context.Context, sessionID string) error

	// PathOf returns the on-disk root for a session (advisory; primarily
	// for logging). Returns "" when the implementation is not file-backed.
	PathOf(sessionID string) string
}

// noteNamePattern is the regex applied to user-supplied note names. It is a
// subset of session.IDPattern: same character class, but capped at 64 chars
// because notes are more numerous than session ids and shorter names keep
// the index injected into the prompt small.
var noteNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// NoteNameMaxLen exposes the length cap for callers who want to reject early.
const NoteNameMaxLen = 64

// validateNoteName returns ErrInvalidName (wrapped) if name is empty, is the
// reserved "." or "..", or contains any character outside [A-Za-z0-9._-].
func validateNoteName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: empty", ErrInvalidName)
	}
	if name == "." || name == ".." {
		return fmt.Errorf("%w: %q is reserved", ErrInvalidName, name)
	}
	if len(name) > NoteNameMaxLen {
		return fmt.Errorf("%w: length %d exceeds %d", ErrInvalidName, len(name), NoteNameMaxLen)
	}
	if !noteNamePattern.MatchString(name) {
		return fmt.Errorf("%w: %q does not match %s", ErrInvalidName, name, noteNamePattern.String())
	}
	return nil
}

// validateSessionID re-runs the same character class as session.IDPattern.
// We do not import vage/session here to avoid a dependency cycle (session
// has no need to know about workspace). The constraint duplication is
// acceptable because both regexes are stable and short.
var sessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

func validateSessionID(id string) error {
	if id == "" {
		return fmt.Errorf("%w: empty", ErrInvalidSession)
	}
	if id == "." || id == ".." {
		return fmt.Errorf("%w: %q is reserved", ErrInvalidSession, id)
	}
	if !sessionIDPattern.MatchString(id) {
		return fmt.Errorf("%w: %q does not match pattern", ErrInvalidSession, id)
	}
	return nil
}
