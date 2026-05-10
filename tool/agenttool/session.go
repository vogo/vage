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

package agenttool

import (
	"context"
	"errors"
	"strings"

	"github.com/vogo/vage/agent"
	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/session"
	"github.com/vogo/vage/sessionview"
)

// ViewBuilder synthesises the SessionView a subagent receives. It is
// invoked once per dispatch, after the child session has been created
// but before agent.Run is called. parentSID is "" when the dispatcher
// has no session context (e.g. CLI without --session).
//
// Returning nil is allowed — the dispatcher will skip the
// sessionview.WithContext call and the subagent will run with only the
// child session id on the ctx (no SessionView).
type ViewBuilder func(parentSID, childSID, subgoal string) *sessionview.SessionView

// sessionConfig is the agenttool's slice of the per-call session wiring.
// Stored on config when WithSessionContext is applied.
type sessionConfig struct {
	store     session.SessionMetaStore
	childIDFn func() string
	viewFn    ViewBuilder
}

// WithSessionContext makes the agent tool create one child session per
// invocation. The child gets:
//
//   - a fresh, sortable session id (override via WithChildIDFunc);
//   - Session.ParentID = the parent session id read from ctx via
//     schema.SessionIDFromContext (empty allowed: the child is then a
//     standalone top-level session);
//   - Session.AgentID = ag.ID() of the dispatched subagent.
//
// Without this option, agent-as-tool stays in legacy "shared session"
// mode: the subagent inherits the parent's ctx unchanged.
func WithSessionContext(store session.SessionMetaStore) Option {
	return func(c *config) {
		if c.session == nil {
			c.session = &sessionConfig{}
		}
		c.session.store = store
	}
}

// WithChildIDFunc overrides how the child session id is minted.
// Defaults to session.GenerateID. Useful for tests (deterministic ids)
// and for callers that want a derived format (e.g. parent_id-suffix).
func WithChildIDFunc(fn func() string) Option {
	return func(c *config) {
		if c.session == nil {
			c.session = &sessionConfig{}
		}
		c.session.childIDFn = fn
	}
}

// WithViewBuilder overrides the SessionView populated for the subagent.
// The default builder produces a minimal view with ParentSessionID,
// ChildSessionID, Subgoal, and ScratchSlot derived from childSID;
// callers wanting to capture parent plan/notes snapshots replace it.
func WithViewBuilder(fn ViewBuilder) Option {
	return func(c *config) {
		if c.session == nil {
			c.session = &sessionConfig{}
		}
		c.session.viewFn = fn
	}
}

// setupChildSession is invoked from newHandler when sessionConfig is
// non-nil. It mints the child id, persists the child session, and
// returns a derived ctx with the child session id and (optionally)
// SessionView attached. childSID == "" indicates "no child session
// wiring" — the caller should run the subagent under the original ctx.
//
// On error, the call is aborted: it is preferable to surface a clear
// 'session setup failed' to the parent LLM than to silently fall back
// and lose the child session record. Store failures are exceptional;
// the dispatcher chose to use this option, so honour the contract.
func setupChildSession(ctx context.Context, cfg *sessionConfig, ag agent.Agent, subgoal string) (context.Context, string, error) {
	if cfg == nil || cfg.store == nil {
		return ctx, "", nil
	}

	childSID := mintChildID(cfg)
	parentSID := schema.SessionIDFromContext(ctx)

	child := session.New(childSID)
	child.ParentID = parentSID
	if ag != nil {
		child.AgentID = ag.ID()
	}
	if err := cfg.store.Create(ctx, child); err != nil {
		// Treat ErrSessionExists as fatal: the id minter is supposed to
		// produce uniques, and reusing one would mean events from two
		// dispatches collide. The caller can override the minter.
		return ctx, "", err
	}

	runCtx := schema.WithSessionID(ctx, childSID)

	view := buildView(cfg.viewFn, parentSID, childSID, subgoal)
	if err := view.Validate(); err == nil {
		runCtx = sessionview.WithContext(runCtx, view)
	}
	return runCtx, childSID, nil
}

// mintChildID consults the override or falls back to GenerateID.
func mintChildID(cfg *sessionConfig) string {
	if cfg.childIDFn != nil {
		return cfg.childIDFn()
	}
	return session.GenerateID()
}

// buildView delegates to a user-supplied builder when present and falls
// back to a minimal default. The default attaches the canonical scratch
// slot id derived from childSID — keeping it in one place ensures
// agenttool and the slot consumer (workspace tools) agree.
func buildView(fn ViewBuilder, parentSID, childSID, subgoal string) *sessionview.SessionView {
	if fn != nil {
		return fn(parentSID, childSID, subgoal)
	}
	return &sessionview.SessionView{
		ParentSessionID: parentSID,
		ChildSessionID:  childSID,
		Subgoal:         subgoal,
		ScratchSlot:     defaultScratchSlot(childSID),
	}
}

// defaultScratchSlot derives a slot name from childSID. The slot
// validator caps at 64 chars and accepts [A-Za-z0-9._-]; child session
// ids satisfy the same character class so we can use the leading
// segment up to the cap. We do NOT hash — collisions across distinct
// child ids would silently mix scratch entries.
func defaultScratchSlot(childSID string) string {
	const slotMax = 64
	if len(childSID) <= slotMax {
		return childSID
	}
	return childSID[:slotMax]
}

// SubgoalFromArgs is a small helper for callers writing a custom
// ArgExtractor that wants to also stash the subgoal in their own
// records. It returns the trimmed input string from a "input" key.
// (Kept as exported helper rather than a private duplicate so users
// extending agenttool with richer schemas can reuse the convention.)
func SubgoalFromArgs(parsed map[string]any) string {
	v, ok := parsed["input"]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}

// errSessionSetup is exposed for callers that want to differentiate
// session-related failures from agent-execution failures with
// errors.Is. The handler currently wraps the message inline; future
// refinement can return this sentinel directly.
var errSessionSetup = errors.New("agenttool: session setup")

// Compile-time guard that errSessionSetup is referenced (the package
// reserves the symbol; keep it linked even if the handler does not yet
// emit it).
var _ = errSessionSetup
