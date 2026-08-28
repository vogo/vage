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
// JSON file plus a lock file) differs. Each function mutates rec in place;
// applyDecisions deliberately retains the valid prefix before an error, and
// reports exactly what it committed, as required by Store.SubmitDecisions.

// maxIDLen bounds an interrupt ID so a hostile caller cannot drive a store
// into filesystem-dependent name-length errors. generateID emits 32 hex
// characters, so this leaves ample headroom.
const maxIDLen = 128

// validateID rejects any id that is not an opaque, store-issued token.
// IDs are always minted by generateID and never supplied by a caller, so
// constraining them to [A-Za-z0-9_-] costs nothing — and it is what makes
// FileStore's <root>/<id>.json join provably stay inside root: no separator,
// no "..", no absolute path and no NUL can survive this check. Both backends
// apply it at every public entry point so a malformed id fails identically
// whichever Store a caller holds.
func validateID(id string) error {
	if id == "" {
		return fmt.Errorf("%w: id is empty", ErrInvalidArgument)
	}
	if len(id) > maxIDLen {
		return fmt.Errorf("%w: id is longer than %d characters", ErrInvalidArgument, maxIDLen)
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return fmt.Errorf("%w: id %q is not an opaque interrupt id", ErrInvalidArgument, id)
		}
	}
	return nil
}

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

	if len(rec.Pending) == 0 {
		return fmt.Errorf("%w: pending is empty", ErrInvalidArgument)
	}

	seenPending := make(map[string]struct{}, len(rec.Pending))
	for _, id := range rec.Pending {
		if id == "" {
			return fmt.Errorf("%w: pending id is empty", ErrInvalidArgument)
		}
		if _, ok := known[id]; !ok {
			return fmt.Errorf("%w: pending id %q is not among tool calls", ErrInvalidArgument, id)
		}
		if _, dup := seenPending[id]; dup {
			return fmt.Errorf("%w: duplicate pending id %q", ErrInvalidArgument, id)
		}
		seenPending[id] = struct{}{}
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

// applyDecisions merges decisions into rec.Decisions in slice order and
// returns the ToolCallIDs this call durably committed, in that order. If a
// decision is rejected, decisions committed earlier in the slice remain,
// the audit fields reflect that prefix, and the returned slice names it.
//
// An idempotent resubmission is absent from the returned slice: it wrote
// nothing. Callers rely on this being computed inside the store's critical
// section — a before/after comparison of two independent reads cannot tell
// a first write from a replay when two submitters race.
func applyDecisions(rec *Record, decisions []Decision, now time.Time) ([]string, error) {
	if rec.Status == StatusCompleted {
		return nil, ErrAlreadyCompleted
	}

	pendingSet := make(map[string]struct{}, len(rec.Pending))
	for _, id := range rec.Pending {
		pendingSet[id] = struct{}{}
	}

	var committed []string
	commitAudit := func() {
		// Pending → Ready only. An idempotent resubmit (or a prefix commit)
		// must never demote Resuming back to Ready: that would drop a live
		// lease and let a second owner AcquireLease concurrently.
		if rec.Status == StatusPending && allDecided(rec.Pending, rec.Decisions) {
			rec.Status = StatusReady
		}
		rec.UpdatedAt = now
		rec.Revision++
	}

	for _, d := range decisions {
		if _, isPending := pendingSet[d.ToolCallID]; !isPending {
			if len(committed) > 0 {
				commitAudit()
			}
			return committed, fmt.Errorf("%w: %q", ErrUnknownToolCall, d.ToolCallID)
		}

		existing, has := rec.Decisions[d.ToolCallID]
		if has {
			if existing.Content == d.Content && existing.IsError == d.IsError {
				continue // idempotent resubmission
			}
			if len(committed) > 0 {
				commitAudit()
			}
			return committed, fmt.Errorf("%w: %q", ErrDecisionConflict, d.ToolCallID)
		}

		if rec.Decisions == nil {
			rec.Decisions = make(map[string]Decision, len(decisions))
		}
		rec.Decisions[d.ToolCallID] = Decision{
			ToolCallID: d.ToolCallID,
			Content:    d.Content,
			IsError:    d.IsError,
			DecidedAt:  now,
		}
		committed = append(committed, d.ToolCallID)
	}

	if len(committed) > 0 {
		commitAudit()
	}

	return committed, nil
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
