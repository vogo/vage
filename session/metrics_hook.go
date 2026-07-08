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
	"log/slog"

	"github.com/vogo/vage/schema"
)

// PricingFunc returns the per-1k-token USD price for a given model.
// Returns ok=false when the model is unknown — the hook treats that
// as "do not bill" rather than zero so callers can audit gaps in the
// pricing table. The hook never logs missing pricing as a warning;
// the cost field is best-effort by design.
type PricingFunc func(model string) (promptUSDPer1k, completionUSDPer1k float64, ok bool)

// ContextEditStrategyElideArtifact is the strategy string emitted by
// the ContextEditorMiddleware single-message-elision path (P0-4). The
// hook special-cases this so the ElidedArtifacts counter advances
// exactly when an artifact is written to disk.
const ContextEditStrategyElideArtifact = "elide_to_artifact"

// SessionMetricsHook subscribes to lifecycle events that move
// SessionMetrics counters and writes through to a MetricsStore. It
// implements hook.Hook (sync) — updates are tiny CAS calls on a map
// or a small JSON file and complete inside a single dispatch tick.
//
// Filter() narrows delivery to the three event types the counters
// actually need (LLMCallEnd / AgentEnd / ContextEdited); everything
// else short-circuits in the hook manager without entering OnEvent.
//
// Two counters cannot be derived from events alone:
//   - ResumeCount: no event fires on resume, the resume entry point is
//     a separate API. Use RecordResume from the resume callers.
//   - CheckpointSaveFailures: no event fires on save FAILURE (success
//     does, but failure is intentionally silent so the hook count
//     stays an invariant for "successful saves"). Use
//     RecordCheckpointFailure from taskagent's failure path via the
//     CheckpointFailureCallback option.
type SessionMetricsHook struct {
	store   MetricsStore
	pricing PricingFunc
	logger  *slog.Logger
}

// MetricsHookOption configures a SessionMetricsHook.
type MetricsHookOption func(*SessionMetricsHook)

// WithMetricsPricing wires a pricing lookup so EventLLMCallEnd
// contributions accumulate CostUSD. nil disables cost accumulation —
// CostUSD will stay zero regardless of token volume.
func WithMetricsPricing(p PricingFunc) MetricsHookOption {
	return func(h *SessionMetricsHook) { h.pricing = p }
}

// WithMetricsLogger overrides the slog.Logger used for non-fatal
// errors (e.g., MetricsStore.Update returning an IO error). Default
// is slog.Default(). Errors are intentionally logged-and-ignored
// because metrics are observability, not correctness — a failed
// update should never abort an in-flight LLM call.
func WithMetricsLogger(l *slog.Logger) MetricsHookOption {
	return func(h *SessionMetricsHook) {
		if l != nil {
			h.logger = l
		}
	}
}

// NewSessionMetricsHook constructs a hook bound to the given store.
// store must be non-nil.
func NewSessionMetricsHook(store MetricsStore, opts ...MetricsHookOption) *SessionMetricsHook {
	h := &SessionMetricsHook{
		store:  store,
		logger: slog.Default(),
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// Filter returns the event types this hook subscribes to. Sticking to
// exactly the subset that moves a counter keeps the hook manager from
// dispatching no-op events (which still cost a goroutine wake under
// the async manager).
func (h *SessionMetricsHook) Filter() []string {
	return []string{
		schema.EventLLMCallEnd,
		schema.EventAgentEnd,
		schema.EventContextEdited,
	}
}

// OnEvent applies the event's contribution to the session's metrics.
// SessionID is taken from event.SessionID; events with empty
// SessionID are dropped silently (no session-scoped state to update).
func (h *SessionMetricsHook) OnEvent(ctx context.Context, event schema.Event) error {
	if h.store == nil || event.SessionID == "" {
		return nil
	}

	switch event.Type {
	case schema.EventLLMCallEnd:
		h.handleLLMCallEnd(ctx, event)
	case schema.EventAgentEnd:
		h.handleAgentEnd(ctx, event)
	case schema.EventContextEdited:
		h.handleContextEdited(ctx, event)
	}
	return nil
}

// handleLLMCallEnd accumulates token counts and (when pricing is
// configured) cost per call. The model is read from the payload so
// each call is billed at its own rate — important when a session
// mixes multiple models via the router.
func (h *SessionMetricsHook) handleLLMCallEnd(ctx context.Context, event schema.Event) {
	d, ok := event.Data.(schema.LLMCallEndData)
	if !ok {
		return
	}
	cost := computeCost(d, h.pricing)
	h.update(ctx, event.SessionID, func(m *SessionMetrics) {
		m.PromptTokens += d.PromptTokens
		m.CompletionTokens += d.CompletionTokens
		m.CostUSD += cost
	})
}

// handleAgentEnd accumulates wall-clock active time. AgentEndData's
// Duration is per-Run; summing across Runs and Resumes gives the
// "time spent in agent" total regardless of idle gaps.
func (h *SessionMetricsHook) handleAgentEnd(ctx context.Context, event schema.Event) {
	d, ok := event.Data.(schema.AgentEndData)
	if !ok {
		return
	}
	if d.Duration <= 0 {
		return
	}
	h.update(ctx, event.SessionID, func(m *SessionMetrics) {
		m.ActiveSeconds += d.Duration / 1000
	})
}

// handleContextEdited counts edit passes. The Strategy field
// determines whether the pass is also a "single-message elide" pass
// that wrote an artifact — those bump ElidedArtifacts in addition to
// the generic ContextEdits counter so dashboards can distinguish a
// chronic keep_last_k user from a chronic single-message-bloat user.
func (h *SessionMetricsHook) handleContextEdited(ctx context.Context, event schema.Event) {
	d, ok := event.Data.(schema.ContextEditedData)
	if !ok {
		return
	}
	h.update(ctx, event.SessionID, func(m *SessionMetrics) {
		m.ContextEdits++
		if d.Strategy == ContextEditStrategyElideArtifact {
			m.ElidedArtifacts++
		}
	})
}

// RecordResume bumps ResumeCount. Both transports (a CLI resume command
// and HTTP POST .../resume) call this exactly once after a successful
// Resume — i.e., after the underlying TaskAgent.Resume returned a
// RunResponse. Failed resumes do NOT increment so the counter stays
// "successful resumes only", matching the EventCheckpointWritten
// invariant of "successful saves only".
func (h *SessionMetricsHook) RecordResume(ctx context.Context, sessionID string) error {
	if h.store == nil {
		return nil
	}
	return h.store.Update(ctx, sessionID, func(m *SessionMetrics) {
		m.ResumeCount++
	})
}

// RecordCheckpointFailure bumps CheckpointSaveFailures. Wired into
// taskagent via WithCheckpointFailureCallback — the callback runs
// after the slog.Warn that already fires on save failure. Best-effort
// by design: an error from the metrics store is logged and dropped so
// metrics observability cannot break the hot path.
func (h *SessionMetricsHook) RecordCheckpointFailure(ctx context.Context, sessionID string) error {
	if h.store == nil {
		return nil
	}
	return h.store.Update(ctx, sessionID, func(m *SessionMetrics) {
		m.CheckpointSaveFailures++
	})
}

// update is the shared error-handling wrapper. Errors are logged but
// never returned to the hook framework so metrics writes cannot abort
// the dispatch chain — this hook is observability, not correctness.
func (h *SessionMetricsHook) update(ctx context.Context, sessionID string, fn func(*SessionMetrics)) {
	if err := h.store.Update(ctx, sessionID, fn); err != nil {
		h.logger.Warn("session metrics: update failed",
			"session_id", sessionID,
			"error", err)
	}
}

// computeCost prices a single LLM call. Returns 0 when pricing is
// nil or the model is unknown — the caller's CostUSD field accumulates
// best-effort, treating gaps in the pricing table as zero rather than
// erroring out.
func computeCost(d schema.LLMCallEndData, pricing PricingFunc) float64 {
	if pricing == nil || d.Model == "" {
		return 0
	}
	promptPer1k, completionPer1k, ok := pricing(d.Model)
	if !ok {
		return 0
	}
	const tokensPerUnit = 1000.0
	return float64(d.PromptTokens)*promptPer1k/tokensPerUnit +
		float64(d.CompletionTokens)*completionPer1k/tokensPerUnit
}
