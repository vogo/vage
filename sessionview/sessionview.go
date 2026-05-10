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

// Package sessionview defines the read-only snapshot a dispatcher hands
// to a subagent at agent-as-tool dispatch time. The shape is:
//
//   - identity for the subagent's own session (ChildSessionID), with a
//     pointer back to the dispatching parent (ParentSessionID);
//   - the natural-language subgoal that scoped the dispatch;
//   - a per-subtask scratch slot (consumed by vage/tool/workspace's
//     scratch_* tools), so the subagent can write drafts that won't
//     pollute the parent's notes/ and that get wiped on retry;
//   - an optional resource budget (advisory — the subagent's own
//     RunOptions still bind);
//   - a frozen snapshot of parent state (plan body + notes index) that
//     the dispatcher captured at dispatch time. This package does not
//     prescribe how the snapshot is built; it only carries it.
//
// The view rides via context — no struct fields on agent.Agent change.
// agenttool publishes the view into the child agent's context just
// before calling Run, and child-side tools (scratch, future paging)
// consume FromContext to know which slot they belong to.
package sessionview

import (
	"context"
	"errors"
	"maps"
	"slices"
	"time"
)

// ResourceBudget is an advisory cap on subagent compute. The dispatcher
// is the source of truth for whether the cap is enforced — TaskAgent's
// own MaxIterations / Budget options have priority. Zero means
// "unspecified" and the dispatcher should read the agent's defaults.
type ResourceBudget struct {
	// MaxIterations caps the subagent's ReAct loop count. 0 == unspecified.
	MaxIterations int

	// MaxTokens is a soft prompt-budget hint for the subagent's
	// ContextBuilder. 0 == unspecified.
	MaxTokens int

	// Deadline, when non-zero, is propagated as ctx deadline so the
	// subagent stops within the parent's wall-clock budget.
	Deadline time.Time
}

// SessionView is the read-only snapshot a subagent receives.
//
// Construction is the dispatcher's responsibility. The struct is value
// type by convention; callers should treat received views as immutable.
type SessionView struct {
	// ParentSessionID is the dispatching parent's session id. May be
	// empty when the dispatcher itself runs without a session.
	ParentSessionID string

	// ChildSessionID is the subagent's own session id. Assigned by the
	// dispatcher right before dispatch; persisted to the SessionStore
	// with Metadata["parent_id"] = ParentSessionID.
	ChildSessionID string

	// Subgoal is the natural-language scope handed to the subagent.
	// Typically the input string the LLM passed to the agent-as-tool
	// call, normalised (trimmed) by the dispatcher.
	Subgoal string

	// ScratchSlot is the per-subtask draft area id. The dispatcher
	// passes this to tool/workspace.RegisterScratch when wiring the
	// subagent's tool registry.
	ScratchSlot string

	// Budget is an advisory cap. Zero values mean "no opinion".
	Budget ResourceBudget

	// PlanSnapshot, when non-empty, is the parent's plan.md captured at
	// dispatch time. Empty means "no parent plan available". The
	// dispatcher decides whether to inject this into the subagent's
	// prompt; this struct only carries it.
	PlanSnapshot string

	// NotesIndex lists the names of the parent's notes at dispatch
	// time. The dispatcher does NOT eagerly read note bodies — bodies
	// are pulled on demand via the workspace API if the subagent has
	// access. Empty slice means "no notes".
	NotesIndex []string

	// Metadata is a free-form bag for caller-specific data
	// (route_label, parent agent_id, retry_attempt). Treat as
	// read-only; copy on derive.
	Metadata map[string]any
}

// ErrInvalidView is returned by Validate when a view fails its
// minimum-shape contract: a child session id is required because the
// dispatcher must always have minted one before dispatch (otherwise
// nothing routes events to the right place).
var ErrInvalidView = errors.New("sessionview: invalid view")

// Validate checks the minimum contract: ChildSessionID must be non-empty.
// Other fields are advisory. Validate is intentionally cheap so the
// dispatcher can run it on every call.
func (v *SessionView) Validate() error {
	if v == nil {
		return ErrInvalidView
	}
	if v.ChildSessionID == "" {
		return errors.New("sessionview: child session id is empty")
	}
	return nil
}

// Clone returns a deep-enough copy: the slice + map are independent of
// the source. Used by FromContext consumers that intend to mutate the
// metadata without poisoning the dispatcher's master record.
func (v *SessionView) Clone() *SessionView {
	if v == nil {
		return nil
	}
	out := *v
	if v.NotesIndex != nil {
		out.NotesIndex = slices.Clone(v.NotesIndex)
	}
	if v.Metadata != nil {
		out.Metadata = maps.Clone(v.Metadata)
	}
	return &out
}

// viewCtxKey is the unexported context key for SessionView propagation.
// Using an unexported struct prevents accidental collision with other
// packages' context values.
type viewCtxKey struct{}

// WithContext returns ctx with view stored under the package's private
// key. nil view returns ctx unchanged so callers can compose without
// nil-checks.
func WithContext(ctx context.Context, view *SessionView) context.Context {
	if view == nil {
		return ctx
	}
	return context.WithValue(ctx, viewCtxKey{}, view)
}

// FromContext returns the SessionView attached by WithContext, or
// (nil, false) when no view is present. The returned pointer is the
// same one stored — callers that intend to mutate should Clone first.
func FromContext(ctx context.Context) (*SessionView, bool) {
	if ctx == nil {
		return nil, false
	}
	v, ok := ctx.Value(viewCtxKey{}).(*SessionView)
	if !ok || v == nil {
		return nil, false
	}
	return v, true
}
