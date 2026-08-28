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

import "errors"

// Sentinel errors returned by Store implementations and TaskAgent's
// interrupt-resume path. Match with errors.Is to remain forward-compatible
// with wrapped errors.
var (
	// ErrNotFound is returned when Get/SubmitDecisions/AcquireLease/
	// ReleaseLease/Complete cannot locate a matching record.
	ErrNotFound = errors.New("interrupt: not found")

	// ErrInvalidArgument is returned when input fails validation (nil
	// record, empty session/agent/id, empty Pending, an ID in Pending
	// that is not among ToolCalls, etc.).
	ErrInvalidArgument = errors.New("interrupt: invalid argument")

	// ErrUnknownToolCall is returned by SubmitDecisions when a decision's
	// ToolCallID is not in the record's Pending set.
	ErrUnknownToolCall = errors.New("interrupt: unknown or already-resolved tool call id")

	// ErrDecisionConflict is returned by SubmitDecisions when a decision
	// resubmits a ToolCallID that already has a committed Decision with
	// different Content/IsError. Resubmitting an identical decision is
	// idempotent and returns nil.
	ErrDecisionConflict = errors.New("interrupt: conflicting decision for tool call")

	// ErrNotReady is returned by AcquireLease when the record still has
	// undecided Pending entries (Status != StatusReady).
	ErrNotReady = errors.New("interrupt: not all pending tool calls have a decision yet")

	// ErrLeaseHeld is returned by AcquireLease when another owner holds a
	// live (non-expired) lease on the record.
	ErrLeaseHeld = errors.New("interrupt: lease held by another resumer")

	// ErrLeaseNotOwned is returned by ReleaseLease/Complete when owner
	// does not match the record's current LeaseOwner (including "no
	// lease at all").
	ErrLeaseNotOwned = errors.New("interrupt: caller does not hold the lease")

	// ErrAlreadyCompleted is returned by AcquireLease/SubmitDecisions when
	// the record's Status is already StatusCompleted — a terminal state
	// that never resumes again.
	ErrAlreadyCompleted = errors.New("interrupt: record already completed")

	// ErrUnknownVersion is returned when a stored record's Version does
	// not match a version this package knows how to read. Stores must
	// fail rather than guess-read an unfamiliar layout.
	ErrUnknownVersion = errors.New("interrupt: unknown record version")
)
