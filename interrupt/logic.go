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
	"fmt"
	"time"
)

// This file holds the state-machine transition logic shared verbatim by
// MapStore and FileStore, so the two backends cannot drift on validation,
// idempotency or conflict rules — only their storage medium (a Go map vs a
// JSON file plus a lock file) differs. Each function mutates rec in place
// and returns an error without partial side effects when it fails, so
// callers can hold whatever lock they use and write back only on success.

// validateNewRecord checks the fields Create requires the caller to supply.
func validateNewRecord(rec *Record) error {
	if rec == nil {
		return fmt.Errorf("%w: record is nil", ErrInvalidArgument)
	}
	if rec.SessionID == "" {
		return fmt.Errorf("%w: session id is empty", ErrInvalidArgument)
	}
	if rec.AgentID == "" {
		return fmt.Errorf("%w: agent id is empty", ErrInvalidArgument)
	}
	if rec.Protocol == "" {
		return fmt.Errorf("%w: protocol is empty", ErrInvalidArgument)
	}
	if len(rec.ToolCalls) == 0 {
		return fmt.Errorf("%w: tool calls is empty", ErrInvalidArgument)
	}

	known := make(map[string]struct{}, len(rec.ToolCalls))
	for _, tc := range rec.ToolCalls {
		if tc.ID == "" {
			return fmt.Errorf("%w: tool call id is empty", ErrInvalidArgument)
		}
		if _, dup := known[tc.ID]; dup {
			return fmt.Errorf("%w: duplicate tool call id %q", ErrInvalidArgument, tc.ID)
		}
		known[tc.ID] = struct{}{}
	}

	for _, id := range rec.Pending {
		if _, ok := known[id]; !ok {
			return fmt.Errorf("%w: pending id %q is not among tool calls", ErrInvalidArgument, id)
		}
	}

	return nil
}

// initialStatus computes Create's starting Status from the Pending set.
func initialStatus(pending []string) Status {
	if len(pending) == 0 {
		return StatusReady
	}
	return StatusPending
}

// applyDecisions merges decisions into rec.Decisions in slice order,
// stopping at the first unknown-tool-call or conflicting resubmission, and
// recomputes Status/UpdatedAt/Revision on success. On error rec is left
// exactly as it was after any decisions committed before the failing one —
// callers persist rec regardless of the returned error so a resubmit-safe
// caller does not repeat already-applied decisions, but MUST NOT persist
// when this returns an error together with zero applied decisions being
// the common case; see callers for the exact write timing.
func applyDecisions(rec *Record, decisions []Decision, now time.Time) error {
	if rec.Status == StatusCompleted {
		return ErrAlreadyCompleted
	}

	pendingSet := make(map[string]struct{}, len(rec.Pending))
	for _, id := range rec.Pending {
		pendingSet[id] = struct{}{}
	}

	if rec.Decisions == nil {
		rec.Decisions = make(map[string]Decision, len(decisions))
	}

	for _, d := range decisions {
		if _, isPending := pendingSet[d.ToolCallID]; !isPending {
			return fmt.Errorf("%w: %q", ErrUnknownToolCall, d.ToolCallID)
		}

		existing, has := rec.Decisions[d.ToolCallID]
		if has {
			if existing.Content == d.Content && existing.IsError == d.IsError {
				continue // idempotent resubmission
			}
			return fmt.Errorf("%w: %q", ErrDecisionConflict, d.ToolCallID)
		}

		rec.Decisions[d.ToolCallID] = Decision{
			ToolCallID: d.ToolCallID,
			Content:    d.Content,
			IsError:    d.IsError,
			DecidedAt:  now,
		}
	}

	if allDecided(rec.Pending, rec.Decisions) {
		rec.Status = StatusReady
	}
	rec.UpdatedAt = now
	rec.Revision++

	return nil
}

// allDecided reports whether every id in pending has a committed decision.
func allDecided(pending []string, decisions map[string]Decision) bool {
	for _, id := range pending {
		if _, ok := decisions[id]; !ok {
			return false
		}
	}
	return true
}

// acquireLeaseOn transitions rec from Ready (or an expired Resuming lease)
// to Resuming under owner.
func acquireLeaseOn(rec *Record, owner string, ttl time.Duration, now time.Time) error {
	switch rec.Status {
	case StatusCompleted:
		return ErrAlreadyCompleted
	case StatusPending:
		return ErrNotReady
	case StatusResuming:
		if rec.LeaseExpiresAt.After(now) {
			return ErrLeaseHeld
		}
		// Expired lease: reclaimable as if Ready.
	case StatusReady:
		// Falls through to acquisition below.
	}

	rec.Status = StatusResuming
	rec.LeaseOwner = owner
	rec.LeaseExpiresAt = now.Add(ttl)
	rec.UpdatedAt = now
	rec.Revision++

	return nil
}

// releaseLeaseOn transitions rec from Resuming back to Ready for owner.
func releaseLeaseOn(rec *Record, owner string, now time.Time) error {
	if rec.Status != StatusResuming || rec.LeaseOwner != owner {
		return ErrLeaseNotOwned
	}

	rec.Status = StatusReady
	rec.LeaseOwner = ""
	rec.LeaseExpiresAt = time.Time{}
	rec.UpdatedAt = now
	rec.Revision++

	return nil
}

// completeOn transitions rec from Resuming to the terminal Completed state
// for owner.
func completeOn(rec *Record, owner string, now time.Time) error {
	if rec.Status != StatusResuming || rec.LeaseOwner != owner {
		return ErrLeaseNotOwned
	}

	rec.Status = StatusCompleted
	rec.LeaseOwner = ""
	rec.LeaseExpiresAt = time.Time{}
	rec.UpdatedAt = now
	rec.Revision++

	return nil
}
