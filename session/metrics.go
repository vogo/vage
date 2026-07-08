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
	"errors"
	"time"
)

// ErrMetricsNotFound is returned by MetricsStore.Get when no metrics
// document exists for the given session id. Sessions accumulate metrics
// lazily — a brand-new session has no record until the first Update —
// so callers should treat this as "zero counters" rather than a fault.
var ErrMetricsNotFound = errors.New("session: metrics not found")

// SessionMetrics is the per-session aggregate observability snapshot.
// Counters are monotonically non-decreasing within a session lifetime;
// the only fields that decrease are timestamps (replaced, not summed).
//
// The struct is the wire format for both the in-process MetricsStore
// API and the HTTP /v1/sessions/{id}/metrics envelope, so additions
// here flow through both transports without adapter code.
//
// Field ordering rules:
//   - Identity first (SessionID).
//   - Cumulative counters in cause-and-effect order (LLM usage → cost
//     → activity time → restart-class events → context edits).
//   - Timestamps last (FirstSeen, LastUpdated).
//
// Future fields should follow the same grouping; deprecate rather than
// rename to keep historical metrics.json files readable.
type SessionMetrics struct {
	SessionID string `json:"session_id"`

	// PromptTokens is the running sum of prompt tokens billed across
	// every LLM call in the session. Includes resumed iterations.
	PromptTokens int `json:"prompt_tokens"`

	// CompletionTokens is the running sum of completion tokens.
	CompletionTokens int `json:"completion_tokens"`

	// TotalTokens is PromptTokens + CompletionTokens. Carried explicitly
	// so wire consumers do not need to reconstruct it; the FileStore
	// trusts the in-memory value rather than recomputing it.
	TotalTokens int `json:"total_tokens"`

	// CostUSD is the cumulative USD cost computed by the caller (which
	// applies its own pricing) and added on each EventAgentEnd. The
	// session metrics layer does not look up pricing itself — it just
	// accumulates whatever the hook sends.
	CostUSD float64 `json:"cost_usd"`

	// ActiveSeconds is the wall-clock duration spent in agent runs,
	// summed across every Run + Resume of the session. It is NOT the
	// span FirstSeen → LastUpdated (which counts idle time).
	ActiveSeconds int64 `json:"active_seconds"`

	// ResumeCount counts successful Resume operations on the session.
	// Both transports (a CLI resume command, HTTP POST .../resume) increment
	// this exactly once per resumed run that returned a RunResponse.
	ResumeCount int `json:"resume_count"`

	// CheckpointSaveFailures counts non-fatal save failures from the
	// IterationStore. A non-zero value means at least one iteration's
	// checkpoint was lost — surface in dashboards as a yellow signal.
	CheckpointSaveFailures int `json:"checkpoint_save_failures"`

	// ContextEdits counts EventContextEdited events seen on the
	// session — each represents one ContextEditorMiddleware pass that
	// folded at least one tool_result. Useful for spotting prompts that
	// chronically rely on editing.
	ContextEdits int `json:"context_edits"`

	// ElidedArtifacts counts EventContextEdited events whose strategy
	// folds a single oversized message into an external artifact (see
	// P0-4 elide_to_artifact). Increments at most once per edit pass.
	ElidedArtifacts int `json:"elided_artifacts"`

	// FirstSeen is the wall-clock time of the first Update call. It is
	// set by the store, not the caller, and never overwritten on
	// subsequent updates.
	FirstSeen time.Time `json:"first_seen"`

	// LastUpdated is refreshed by the store on every successful Update.
	LastUpdated time.Time `json:"last_updated"`
}

// MetricsStore persists per-session SessionMetrics with CAS-style
// updates. Implementations must be safe for concurrent use across
// distinct sessions; concurrent Update calls against the same id are
// serialised so the closure observes a consistent "before" snapshot.
//
// All methods take ctx; implementations must respect ctx.Err() before
// performing IO.
type MetricsStore interface {
	// Get returns a fresh copy of the session's metrics. Returns
	// ErrMetricsNotFound when no Update has yet recorded anything for
	// the id; callers should treat that as "zero counters" rather than
	// fault.
	Get(ctx context.Context, sessionID string) (*SessionMetrics, error)

	// Update applies fn to the current SessionMetrics under the per-
	// session lock and writes the result back. fn always receives a
	// non-nil pointer:
	//   - First-time updates pre-populate SessionID and FirstSeen so
	//     fn only sees a zero counters state, never a partially
	//     constructed record.
	//   - Subsequent updates pass the existing record by value-copy;
	//     fn mutates it freely.
	// LastUpdated is set to time.Now() by the store after fn returns.
	// Returns the underlying IO error from the implementation.
	Update(ctx context.Context, sessionID string, fn func(*SessionMetrics)) error

	// Delete removes the metrics record. Idempotent — deleting a
	// missing id returns nil.
	Delete(ctx context.Context, sessionID string) error
}

// applyUpdate is the shared CAS body used by every MetricsStore: it
// pre-populates a zero-value record on first write, calls fn, then
// stamps LastUpdated. It is intentionally exported within the package
// (not the public API) so MapMetricsStore and FileMetricsStore stay
// behaviourally identical.
//
// fn is called even when the record is being created from zero so
// callers see the same closure semantics either way; passing an empty
// (no-op) fn is a valid way to materialise a record with just identity
// + timestamps populated.
func applyUpdate(sessionID string, current *SessionMetrics, fn func(*SessionMetrics), now time.Time) *SessionMetrics {
	if current == nil {
		current = &SessionMetrics{
			SessionID: sessionID,
			FirstSeen: now,
		}
	}
	if fn != nil {
		fn(current)
	}
	current.SessionID = sessionID // override any drift from fn
	current.LastUpdated = now
	if current.FirstSeen.IsZero() {
		current.FirstSeen = now
	}
	// Keep TotalTokens consistent with the two underlying counters
	// regardless of whether fn touched it directly. This stops a
	// hook author who only updates Prompt/Completion from leaving the
	// Total field stale.
	current.TotalTokens = current.PromptTokens + current.CompletionTokens
	return current
}

// cloneMetrics returns a deep copy. Used by Get implementations so the
// caller cannot scribble over the in-memory record.
func cloneMetrics(in *SessionMetrics) *SessionMetrics {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}
