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
	"time"
)

// Store persists interrupt Records across the state machine documented on
// the package. Implementations must be safe for concurrent use, and the
// File-backed implementation must additionally be safe across independent
// processes opening the same root directory — that is the whole point of
// this package: a second process must be able to submit decisions and
// resume without sharing memory with the process that suspended.
//
// AcquireLease is the only method that must provide real mutual exclusion
// across independent Store instances (see FileStore); every other method
// is a plain atomic read/update of one record.
//
// Record IDs are opaque tokens the store itself mints in Create. Every
// id-taking method rejects an id that is empty, over-long, or built from
// anything but [A-Za-z0-9_-] with ErrInvalidArgument — before looking
// anything up — so a caller-shaped id can never be resolved against the
// storage medium (for FileStore, never against a path outside its root).
type Store interface {
	// Create persists a brand-new record. The store assigns ID,
	// CreatedAt, UpdatedAt and Revision (starting at 1); any
	// caller-supplied values for these fields are overwritten. Status
	// starts as StatusPending: Pending must be a non-empty unique subset
	// of ToolCalls[i].ID (nothing left to decide is not a Create case —
	// that is SubmitDecisions' Pending → Ready transition).
	//
	// Returns ErrInvalidArgument when rec is nil, when SessionID,
	// AgentID, Protocol or ToolCalls is empty, when Pending is empty,
	// contains duplicates, or names an ID that is not a known
	// ToolCalls[i].ID.
	//
	// After Create returns nil, rec's assigned fields are populated so
	// the caller can address it and emit an interrupt_created event.
	Create(ctx context.Context, rec *Record) error

	// Get returns the full record identified by id, including Messages
	// and Decisions. Returns ErrNotFound when id is unknown, and
	// ErrUnknownVersion when the persisted record's Version does not
	// match CurrentVersion.
	Get(ctx context.Context, id string) (*Record, error)

	// SubmitDecisions atomically merges decisions into the record's
	// Decisions map, keyed by ToolCallID, and returns the updated record.
	//
	// Each decision must address a ToolCallID in the record's Pending
	// set: ErrUnknownToolCall otherwise. Resubmitting an identical
	// decision (same Content and IsError) for an already-decided
	// ToolCallID is idempotent; resubmitting a different one returns
	// ErrDecisionConflict without changing that decision. Decisions are
	// applied one at a time in slice order, so a conflict or unknown ID on
	// the Nth decision leaves the first N-1 committed. When a prefix was
	// committed before the error, the returned *Record reflects that
	// prefix and is non-nil alongside the error.
	//
	// When every Pending ID has a Decision after this call and Status is
	// still StatusPending, Status transitions Pending → Ready. A record
	// already in StatusResuming stays Resuming: SubmitDecisions never
	// revokes a live lease. During Resuming, identical resubmits are
	// no-ops; conflicting or unknown IDs still error. Returns ErrNotFound
	// for an unknown id, ErrAlreadyCompleted for a completed record.
	SubmitDecisions(ctx context.Context, id string, decisions []Decision) (*Record, error)

	// AcquireLease transitions a Ready record to Resuming under owner's
	// name for ttl, and returns the updated record. Concurrent
	// AcquireLease calls — including from independent Store instances in
	// separate processes — must yield exactly one success while the
	// lease is live; a live lease held by a different owner returns
	// ErrLeaseHeld. A record already in Resuming whose LeaseExpiresAt has
	// passed is reclaimable: the caller may acquire it as if it were
	// Ready.
	//
	// Returns ErrNotFound for an unknown id, ErrAlreadyCompleted for a
	// completed record, ErrNotReady when Status == StatusPending (some
	// Pending entries still undecided).
	AcquireLease(ctx context.Context, id, owner string, ttl time.Duration) (*Record, error)

	// ReleaseLease transitions a Resuming record back to Ready, for
	// retry by any future owner, without touching Decisions. Only the
	// current lease holder may release: ErrLeaseNotOwned otherwise
	// (including when the lease already expired and no one owns it, or
	// when Status is not Resuming). Used when a resume attempt fails
	// after acquiring the lease, so the record — and the decisions
	// already paid for — remain resumable without re-asking the human.
	ReleaseLease(ctx context.Context, id, owner string) error

	// Complete transitions a Resuming record to the terminal Completed
	// state. Only the current lease holder may complete:
	// ErrLeaseNotOwned otherwise. Completed records are excluded from no
	// further state transitions — Complete/AcquireLease/SubmitDecisions
	// on an already-Completed id return ErrAlreadyCompleted (Complete
	// itself is NOT idempotent: calling it twice is a caller bug, not a
	// retryable condition, since the second caller no longer holds the
	// lease that was consumed by the first).
	Complete(ctx context.Context, id, owner string) error

	// List returns metadata (Meta — no Messages, no Decisions) for every
	// record belonging to sessionID, in unspecified order. sessionID is
	// required; an empty value returns ErrInvalidArgument. This is the
	// path a caller uses to recover an interrupt_id it never received —
	// e.g. context was canceled before the RunResponse reached it — by
	// listing pending records for a known session.
	List(ctx context.Context, sessionID string) ([]*Meta, error)

	// Delete removes the record identified by id. Idempotent on an
	// unknown — but well-formed — id; a malformed id returns
	// ErrInvalidArgument like every other id-taking method. FileStore
	// takes the same per-record cross-process lock as every other
	// mutation, so Delete cannot unlink a lock another instance holds
	// and cannot race a concurrent read-modify-write of the same id.
	Delete(ctx context.Context, id string) error
}
