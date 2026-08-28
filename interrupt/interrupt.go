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

// Package interrupt persists a TaskAgent ReAct loop's suspended state when a
// tool batch needs an external (human) decision before it may execute, and
// lets a second process — potentially a different one than took the
// suspend — inject that decision and resume from exactly the suspended tool
// batch.
//
// This is a distinct persistence contract from vage/checkpoint: a
// checkpoint.Checkpoint is a crash-replay snapshot written after a tool
// batch has already run, addressed by session; an interrupt.Record is a
// pending-decision snapshot written BEFORE a flagged tool batch runs,
// addressed by its own opaque ID, and carries a state machine (Pending →
// Ready → Resuming → Completed) that a checkpoint never needs. Neither
// package imports the other, and TaskAgent never writes an interrupt
// through the IterationStore or vice versa.
package interrupt

import (
	"crypto/rand"
	"encoding/hex"
	"maps"
	"time"

	"github.com/vogo/vage/schema"
)

// CurrentVersion is the Record schema version this package writes. Stores
// must reject records whose Version does not match a version they know how
// to read rather than guessing at an unfamiliar layout — see
// ErrUnknownVersion.
const CurrentVersion = 1

// Status is the interrupt state-machine position of a Record.
type Status string

// Status constants — see the package doc's state diagram.
const (
	// StatusPending means at least one entry in Pending has no committed
	// Decision yet.
	StatusPending Status = "pending"
	// StatusReady means every entry in Pending has a committed Decision;
	// the record is eligible for AcquireLease.
	StatusReady Status = "ready"
	// StatusResuming means a lease holder is actively re-entering the
	// ReAct loop from this record.
	StatusResuming Status = "resuming"
	// StatusCompleted is terminal: either the resumed run reached a normal
	// stop reason, or it durably created a follow-up Record (a nested
	// interrupt). Completed records are never resumed again.
	StatusCompleted Status = "completed"
)

// EffectiveParams is the snapshot of a Run's resolved parameters — the
// result of merging schema.RunOptions onto Agent defaults — captured at
// interrupt time. Resume uses these instead of re-reading the new process's
// Agent defaults so a config change on the resuming side cannot silently
// change the remaining budget, tool scope or model mid-run.
type EffectiveParams struct {
	Model          string   `json:"model"`
	Temperature    *float64 `json:"temperature,omitempty"`
	MaxIterations  int      `json:"max_iterations"`
	RunTokenBudget int      `json:"run_token_budget"`
	MaxTokens      *int     `json:"max_tokens,omitempty"`
	ToolFilter     []string `json:"tool_filter,omitempty"`
	StopSequences  []string `json:"stop_sequences,omitempty"`
}

// Decision is one committed external decision for a pending tool call.
// DecidedAt is stamped by the Store, not the caller.
type Decision struct {
	ToolCallID string    `json:"tool_call_id"`
	Content    string    `json:"content"`
	IsError    bool      `json:"is_error,omitempty"`
	DecidedAt  time.Time `json:"decided_at"`
}

// Record is the complete, restorable persistence of one suspended tool
// batch. Only Store implementations construct and mutate Records; callers
// receive copies.
//
// Invariants:
//   - len(Pending) >= 1 always (a Record is only created when a policy
//     flagged at least one call in the batch).
//   - Every ID in Pending is a ToolCall.ID present in ToolCalls.
//   - Status == StatusReady  ⇒ every Pending ID has a Decisions entry.
//   - Status == StatusResuming ⇒ LeaseOwner != "" and !LeaseExpiresAt.IsZero().
type Record struct {
	// Identity.
	Version int    `json:"version"`
	ID      string `json:"id"`

	SessionID string          `json:"session_id"`
	AgentID   string          `json:"agent_id"`
	Protocol  schema.Protocol `json:"protocol"`

	// ToolCalls is the full assistant tool-call batch, in the model's
	// original order. Pending is the subset of ToolCalls[i].ID the
	// InterruptPolicy flagged; the rest are ordinary siblings executed
	// normally once every Pending ID has a Decision.
	ToolCalls []schema.ToolCall   `json:"tool_calls"`
	Pending   []string            `json:"pending"`
	Decisions map[string]Decision `json:"decisions,omitempty"`

	// Messages is the continuation: the full message history up to and
	// including the assistant message that produced ToolCalls. Resume
	// only appends tool-result messages to this slice — it never
	// reconstructs or replays the model turn that produced it.
	Messages        []schema.Message `json:"messages"`
	SessionMsgCount int              `json:"session_msg_count"`

	// Params is the effective Run configuration captured at interrupt
	// time — see EffectiveParams.
	Params EffectiveParams `json:"params"`

	// Iteration/Usage/Estimated mirror checkpoint.Checkpoint's resumable
	// counters: the iteration whose tool batch is suspended, and the
	// accumulated usage up to (and including) that model turn.
	Iteration int          `json:"iteration"`
	Usage     schema.Usage `json:"usage"`
	Estimated bool         `json:"estimated,omitempty"`

	// TokensConsumed is what the suspended Run's token-budget tracker had
	// already charged against Params.RunTokenBudget. It is deliberately
	// separate from Usage: a streamed turn with no vendor usage report
	// charges the budget an estimate that never enters Usage, so restoring
	// the budget from Usage would silently hand the resumed half of the
	// same logical Run more tokens than it has left.
	TokensConsumed int `json:"tokens_consumed,omitempty"`

	// State machine.
	Status   Status `json:"status"`
	Revision int    `json:"revision"`

	// Lease. Only meaningful while Status == StatusResuming.
	LeaseOwner     string    `json:"lease_owner,omitempty"`
	LeaseExpiresAt time.Time `json:"lease_expires_at"`

	// Audit.
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Meta is the slim metadata returned by Store.List. It never embeds
// Messages or Decisions so a listing scan never exposes conversation
// content or decision payloads.
type Meta struct {
	ID        string          `json:"id"`
	SessionID string          `json:"session_id"`
	AgentID   string          `json:"agent_id"`
	Protocol  schema.Protocol `json:"protocol"`
	Status    Status          `json:"status"`
	Iteration int             `json:"iteration"`
	Pending   []string        `json:"pending"`
	Revision  int             `json:"revision"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// metaFrom projects the slim metadata view from a full Record.
func metaFrom(r *Record) *Meta {
	return &Meta{
		ID:        r.ID,
		SessionID: r.SessionID,
		AgentID:   r.AgentID,
		Protocol:  r.Protocol,
		Status:    r.Status,
		Iteration: r.Iteration,
		Pending:   append([]string(nil), r.Pending...),
		Revision:  r.Revision,
		CreatedAt: r.CreatedAt,
		UpdatedAt: r.UpdatedAt,
	}
}

// generateID returns a 16-byte hex token suitable as an interrupt ID. Falls
// back to a timestamp-derived placeholder if crypto/rand fails so Create
// never panics.
func generateID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "fb" + hex.EncodeToString([]byte(time.Now().Format("150405.000000")))
	}
	return hex.EncodeToString(buf[:])
}

// cloneToolCalls copies the slice; schema.ToolCall is a plain value type.
func cloneToolCalls(in []schema.ToolCall) []schema.ToolCall {
	if len(in) == 0 {
		return nil
	}
	out := make([]schema.ToolCall, len(in))
	copy(out, in)
	return out
}

// cloneStrings copies a string slice.
func cloneStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}

// cloneMessages copies the top-level slice. schema.Message internals are
// immutable post-creation by TaskAgent convention, matching checkpoint's
// cloneMessages.
func cloneMessages(in []schema.Message) []schema.Message {
	if len(in) == 0 {
		return nil
	}
	out := make([]schema.Message, len(in))
	copy(out, in)
	return out
}

// cloneDecisions deep-copies the decision map.
func cloneDecisions(in map[string]Decision) map[string]Decision {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]Decision, len(in))
	maps.Copy(out, in)
	return out
}

// cloneParams copies the pointer fields so the clone cannot alias the
// original's mutable state.
func cloneParams(p EffectiveParams) EffectiveParams {
	out := p
	if p.Temperature != nil {
		t := *p.Temperature
		out.Temperature = &t
	}
	if p.MaxTokens != nil {
		m := *p.MaxTokens
		out.MaxTokens = &m
	}
	out.ToolFilter = cloneStrings(p.ToolFilter)
	out.StopSequences = cloneStrings(p.StopSequences)
	return out
}

// cloneRecord returns a defensive deep copy of r suitable for handing out
// from Store.Get / internal map storage so external or cross-call mutation
// cannot bleed back into store-internal state.
func cloneRecord(r *Record) *Record {
	if r == nil {
		return nil
	}
	out := *r
	out.ToolCalls = cloneToolCalls(r.ToolCalls)
	out.Pending = cloneStrings(r.Pending)
	out.Decisions = cloneDecisions(r.Decisions)
	out.Messages = cloneMessages(r.Messages)
	out.Params = cloneParams(r.Params)
	return &out
}
