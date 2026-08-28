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

package taskagent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/vogo/vage/interrupt"
	"github.com/vogo/vage/schema"
)

// Sentinel errors for the interrupt/resume path. Match with errors.Is.
var (
	// ErrInterruptConfig is returned at the first Run/RunStream/
	// ResumeInterrupt call when exactly one of WithInterruptStore /
	// WithInterruptPolicy is configured. Both or neither is required —
	// see the option docs.
	ErrInterruptConfig = errors.New("vage: WithInterruptStore and WithInterruptPolicy must both be configured, or neither")

	// ErrInterruptAgentMismatch is returned by ResumeInterrupt when the
	// persisted record's AgentID does not match this Agent's ID.
	ErrInterruptAgentMismatch = errors.New("vage: interrupt record belongs to a different agent")

	// ErrIncompatibleInterruptTools is returned by ResumeInterrupt when a
	// non-pending tool call in the record's batch names a tool this
	// Agent's registry does not recognize. Pending calls need no
	// registered handler — their result comes from the submitted
	// decision, not execution — so only the ordinary sibling calls in the
	// same batch are checked.
	ErrIncompatibleInterruptTools = errors.New("vage: interrupt record references tools this agent cannot execute")
)

// checkInterruptConfig enforces that WithInterruptStore and
// WithInterruptPolicy are configured together or not at all. Called from
// preflightRun (Run/RunStream) and from ResumeInterrupt directly, since
// Resume does not go through preflightRun.
func (a *Agent) checkInterruptConfig() error {
	if (a.interruptStore == nil) != (a.interruptPolicy == nil) {
		return ErrInterruptConfig
	}
	return nil
}

// maybeInterrupt asks the configured InterruptPolicy about the current tool
// batch and, if it flags at least one call, durably persists an interrupt
// record before reporting the batch must suspend. continuation is the
// message list up to and including the assistant tool-call message that
// produced calls (already appended by runReactLoop). A non-nil error here
// is a hard Run error: an unpersisted suspend has no resumable meaning, so
// runReactLoop must not fabricate StopReasonInterrupted around it.
func (a *Agent) maybeInterrupt(
	ctx context.Context,
	rc *runContext,
	p runParams,
	continuation []schema.Message,
	calls []schema.ToolCall,
) (*schema.InterruptDescriptor, bool, error) {
	pending := a.interruptPolicy.Intercept(ctx, rc.sessionID, calls)
	if len(pending) == 0 {
		return nil, false, nil
	}

	if err := validateBatchAddressable(calls); err != nil {
		return nil, false, fmt.Errorf("vage: interrupt: %w", err)
	}
	if err := validatePendingSubset(calls, pending); err != nil {
		return nil, false, fmt.Errorf("vage: interrupt: %w", err)
	}

	rec := &interrupt.Record{
		SessionID: rc.sessionID,
		AgentID:   a.ID(),
		Protocol:  a.Protocol(),
		ToolCalls: calls,
		Pending:   pending,
		Messages:  continuation,
		// This Run's finalize (guaranteed to run synchronously before Run
		// returns to its own caller, on this same suspend path) promotes
		// rc.reqMsgs starting at rc.br.sessionMsgCount and withholds the
		// response messages — see finalizeRun/interruptSafeRespMsgs.
		// Reserving len(rc.reqMsgs) here so Resume's own eventual write
		// starts right after them, instead of colliding on the same keys.
		SessionMsgCount: rc.br.sessionMsgCount + len(rc.reqMsgs),
		Params:          runParamsToEffective(p),
		Iteration:       rc.iteration,
		Usage:           rc.totalUsage,
		Estimated:       rc.estimated,
		TokensConsumed:  rc.tracker.Consumed(),
	}

	if err := a.interruptStore.Create(ctx, rec); err != nil {
		return nil, false, fmt.Errorf("vage: persist interrupt: %w", err)
	}

	a.dispatch(ctx, schema.NewEvent(schema.EventInterruptCreated, a.ID(), rc.sessionID, schema.InterruptCreatedData{
		InterruptID:        rec.ID,
		Iteration:          rec.Iteration,
		PendingToolCallIDs: rec.Pending,
	}))

	return interruptDescriptorFromRecord(rec), true, nil
}

// validateBatchAddressable enforces the framework's addressing contract for
// a batch that is about to suspend: every ToolCall.ID must be non-empty and
// unique. The framework never synthesizes or rewrites a vendor call ID, so
// a batch that violates this cannot be persisted in a resumable way.
func validateBatchAddressable(calls []schema.ToolCall) error {
	seen := make(map[string]struct{}, len(calls))
	for _, tc := range calls {
		if tc.ID == "" {
			return fmt.Errorf("tool call %q has an empty id", tc.Name)
		}
		if _, dup := seen[tc.ID]; dup {
			return fmt.Errorf("duplicate tool call id %q", tc.ID)
		}
		seen[tc.ID] = struct{}{}
	}
	return nil
}

// validatePendingSubset enforces that Pending is a unique subset of the
// batch's ToolCall IDs. Empty is not a valid flagged set — Intercept
// already treated that as "do not interrupt". Duplicates would make
// resume allocate a negative sibling slice and panic.
func validatePendingSubset(calls []schema.ToolCall, pending []string) error {
	known := make(map[string]struct{}, len(calls))
	for _, tc := range calls {
		known[tc.ID] = struct{}{}
	}
	seen := make(map[string]struct{}, len(pending))
	for _, id := range pending {
		if id == "" {
			return fmt.Errorf("%w: pending id is empty", interrupt.ErrInvalidArgument)
		}
		if _, ok := known[id]; !ok {
			return fmt.Errorf("%w: pending id %q is not among tool calls", interrupt.ErrInvalidArgument, id)
		}
		if _, dup := seen[id]; dup {
			return fmt.Errorf("%w: duplicate pending id %q", interrupt.ErrInvalidArgument, id)
		}
		seen[id] = struct{}{}
	}
	return nil
}

// interruptDescriptorFromRecord projects the wire-level summary from a
// full Record, preserving the original ToolCalls order. Pending here means
// "still awaiting a decision" — rec.Pending is the batch's fixed original
// set, so an ID that already has a committed rec.Decisions entry is
// excluded even though it stays in rec.Pending for the record's own
// lifetime bookkeeping.
func interruptDescriptorFromRecord(rec *interrupt.Record) *schema.InterruptDescriptor {
	stillPending := make(map[string]struct{}, len(rec.Pending))
	for _, id := range rec.Pending {
		if _, decided := rec.Decisions[id]; !decided {
			stillPending[id] = struct{}{}
		}
	}

	pending := make([]schema.ToolCall, 0, len(stillPending))
	for _, tc := range rec.ToolCalls {
		if _, ok := stillPending[tc.ID]; ok {
			pending = append(pending, tc)
		}
	}

	return &schema.InterruptDescriptor{InterruptID: rec.ID, Pending: pending}
}

// runParamsToEffective snapshots a resolved runParams for persistence.
func runParamsToEffective(p runParams) interrupt.EffectiveParams {
	return interrupt.EffectiveParams{
		Model:          p.model,
		Temperature:    p.temperature,
		MaxIterations:  p.maxIter,
		RunTokenBudget: p.runTokenBudget,
		MaxTokens:      p.maxTokens,
		ToolFilter:     p.toolFilter,
		StopSequences:  p.stopSeq,
	}
}

// effectiveParamsToRunParams reconstructs runParams from a persisted
// snapshot. Resume uses these instead of a.resolveRunParams(nil) so a
// config change on the resuming process cannot silently change the
// remaining budget, tool scope, model or stop sequences mid-run.
func effectiveParamsToRunParams(ep interrupt.EffectiveParams) runParams {
	return runParams{
		model:          ep.Model,
		temperature:    ep.Temperature,
		maxIter:        ep.MaxIterations,
		runTokenBudget: ep.RunTokenBudget,
		maxTokens:      ep.MaxTokens,
		toolFilter:     ep.ToolFilter,
		stopSeq:        ep.StopSequences,
	}
}

// generateLeaseOwner returns an opaque token identifying this
// ResumeInterrupt call as a lease holder. It need not be globally unique
// forever — only unlikely enough to collide within one lease TTL window —
// so a 12-byte random hex token is more than sufficient.
func generateLeaseOwner() string {
	var buf [12]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "owner-" + hex.EncodeToString([]byte(time.Now().Format("150405.000000")))
	}
	return hex.EncodeToString(buf[:])
}

// ResumeInterrupt addresses a persisted interrupt record by its exact ID,
// submits zero or more external decisions, and — once every pending tool
// call in the batch has a decision — resumes the ReAct loop from exactly
// that tool batch. It is the cross-process counterpart to a suspend
// produced by the InterruptPolicy choke point in runReactLoop.
//
// Omitting Decisions submits nothing and resumes on whatever is already
// committed. While any flagged call is still undecided that degenerates into
// a pure status probe — the still-pending set comes back and no tool or model
// call starts — and once every one is decided it retries the resume, which is
// how an attempt that failed after the human already paid for the decisions
// is picked up again, possibly by a process that never held them.
//
// Errors:
//   - ErrInterruptConfig when WithInterruptStore/WithInterruptPolicy are
//     not both configured.
//   - interrupt.ErrInvalidArgument for an empty InterruptID.
//   - interrupt.ErrNotFound / ErrUnknownVersion from the store.
//   - ErrInterruptAgentMismatch / schema.ErrProtocolMismatch when the
//     record does not belong to this Agent or protocol.
//   - interrupt.ErrUnknownToolCall / ErrDecisionConflict /
//     ErrAlreadyCompleted from SubmitDecisions.
//   - interrupt.ErrLeaseHeld when another resumer already holds a live
//     lease.
//   - ErrIncompatibleInterruptTools when a sibling call in the batch names
//     a tool this Agent's registry does not have.
//
// ResumeInterrupt does not run the agent middleware chain (WithMiddleware):
// like Resume(ctx, sessionID), it continues a run that already passed
// through it once. Run values start empty; input guards do not re-run
// (the original Run already vetted the input); external decisions and any
// freshly-executed sibling tool results still pass through tool-result
// guards; the final response still passes through output guards, message
// persistence and the terminal event path.
func (a *Agent) ResumeInterrupt(ctx context.Context, req schema.ResumeInterruptRequest) (*schema.RunResponse, error) {
	// Same preflight Run/Resume perform, and for the same reason — but it
	// matters more here: a resumer reached mid-way through this method has
	// already taken the lease and possibly executed sibling tools, side
	// effects an incompletely configured process must never pay for.
	if a.caller == nil {
		return nil, errors.New("vage: model caller is required")
	}
	if err := a.checkInterruptConfig(); err != nil {
		return nil, err
	}

	// A resume is a new local entry point: it gets its own empty Run-value
	// store, same as Run/RunStream/Resume. Nothing from the suspended
	// process's run values is — or could be — carried over; see
	// WithRunValues' scoping contract.
	ctx = schema.WithRunValues(ctx)

	if req.InterruptID == "" {
		return nil, fmt.Errorf("%w: interrupt id is empty", interrupt.ErrInvalidArgument)
	}

	rec, err := a.interruptStore.Get(ctx, req.InterruptID)
	if err != nil {
		return nil, err
	}
	if rec.AgentID != a.ID() {
		return nil, fmt.Errorf("%w: record agent %q, this agent %q", ErrInterruptAgentMismatch, rec.AgentID, a.ID())
	}
	if rec.Protocol != a.Protocol() {
		return nil, fmt.Errorf("%w: record protocol %q, this agent %q", schema.ErrProtocolMismatch, rec.Protocol, a.Protocol())
	}

	if len(req.Decisions) > 0 {
		updated, submitErr := a.interruptStore.SubmitDecisions(ctx, rec.ID, toStoreDecisions(req.Decisions))
		if updated != nil {
			rec = updated
			a.emitInterruptDecisionEvents(ctx, rec, req.Decisions)
		}
		if submitErr != nil {
			return nil, submitErr
		}
	}

	// A fresh generated-per-call token: ResumeInterrupt does not offer a
	// caller-supplied owner identity, because the lease exists to protect
	// against a crashed *attempt*, not to let a caller impersonate one.
	owner := generateLeaseOwner()

	leased, err := a.interruptStore.AcquireLease(ctx, rec.ID, owner, a.interruptLeaseTTL)
	if err != nil {
		if errors.Is(err, interrupt.ErrNotReady) {
			// Still missing decisions: report the current pending set
			// without starting any tool or model call.
			return &schema.RunResponse{
				SessionID:  rec.SessionID,
				StopReason: schema.StopReasonInterrupted,
				Interrupt:  interruptDescriptorFromRecord(rec),
			}, nil
		}
		return nil, err
	}
	rec = leased

	if err := a.validateInterruptToolCompatibility(rec); err != nil {
		_ = a.interruptStore.ReleaseLease(ctx, rec.ID, owner)
		return nil, err
	}

	return a.resumeFromInterrupt(ctx, rec, owner)
}

// emitInterruptDecisionEvents fires interrupt_decision_stored for each
// submitted decision that actually landed, in slice order, and stops at
// the first entry that was not committed (the rejected Nth of a prefix
// submit). This keeps the event count aligned with durable writes even
// when SubmitDecisions returns an error after committing a valid prefix.
func (a *Agent) emitInterruptDecisionEvents(ctx context.Context, rec *interrupt.Record, submitted []schema.InterruptDecision) {
	ready := rec.Status != interrupt.StatusPending
	for _, d := range submitted {
		existing, ok := rec.Decisions[d.ToolCallID]
		if !ok || existing.Content != d.Content || existing.IsError != d.IsError {
			return
		}
		a.dispatch(ctx, schema.NewEvent(schema.EventInterruptDecisionStored, a.ID(), rec.SessionID, schema.InterruptDecisionStoredData{
			InterruptID: rec.ID,
			ToolCallID:  d.ToolCallID,
			Ready:       ready,
		}))
	}
}

// validateInterruptToolCompatibility fails before anything executes when a
// non-pending (ordinary sibling) tool call in the batch names a tool this
// Agent's registry cannot recognize. Pending calls need no handler — the
// committed decision replaces execution — so only siblings are checked.
func (a *Agent) validateInterruptToolCompatibility(rec *interrupt.Record) error {
	pendingSet := make(map[string]struct{}, len(rec.Pending))
	for _, id := range rec.Pending {
		pendingSet[id] = struct{}{}
	}

	for _, tc := range rec.ToolCalls {
		if _, isPending := pendingSet[tc.ID]; isPending {
			continue
		}
		if a.toolRegistry == nil {
			return fmt.Errorf("%w: no tool registry configured", ErrIncompatibleInterruptTools)
		}
		if _, ok := a.toolRegistry.Get(tc.Name); !ok {
			return fmt.Errorf("%w: tool %q not registered", ErrIncompatibleInterruptTools, tc.Name)
		}
	}
	return nil
}

// toStoreDecisions converts the wire-level decisions to the persistence
// model. DecidedAt is deliberately left zero — the Store stamps it.
func toStoreDecisions(in []schema.InterruptDecision) []interrupt.Decision {
	out := make([]interrupt.Decision, len(in))
	for i, d := range in {
		out[i] = interrupt.Decision{ToolCallID: d.ToolCallID, Content: d.Content, IsError: d.IsError}
	}
	return out
}

// resumeFromInterrupt re-enters the ReAct loop at the suspended tool batch
// once the lease is held and tool compatibility is confirmed: it applies
// the committed decisions and executes the batch's ordinary sibling calls
// (both pass through the same tool-result guards), then either
// short-circuits via ReturnDirect or continues runReactLoop from
// rec.Iteration+1 — the same choke points a live Run would have hit.
//
// The old record is marked Completed once this call reaches a real
// terminal path or durably creates a follow-up interrupt (both are the
// only ways runReactLoop returns without an error); on any error the lease
// is released instead, leaving the record — and the decisions already
// paid for — resumable without re-asking the human.
func (a *Agent) resumeFromInterrupt(ctx context.Context, rec *interrupt.Record, owner string) (*schema.RunResponse, error) {
	agentID := a.ID()
	p := effectiveParamsToRunParams(rec.Params)

	// The resumed half continues the suspended Run's budget rather than
	// restarting it: same budget, same already-charged total.

	rc := &runContext{
		sessionID:  rec.SessionID,
		start:      time.Now(),
		tracker:    newBudgetTrackerAt(p.runTokenBudget, rec.TokensConsumed),
		totalUsage: rec.Usage,
		estimated:  rec.Estimated,
		br: buildResult{
			messages:        rec.Messages,
			sessionMsgCount: rec.SessionMsgCount,
		},
		// reqMsgs left nil: nothing new to promote as "request" — the
		// original user turn was already handled by the interrupted Run's
		// own (skipped) finalize; see finalizeRun's interrupted branch.
		reqMsgs:   nil,
		iteration: rec.Iteration,
	}

	a.dispatch(ctx, schema.NewEvent(schema.EventAgentStart, agentID, rc.sessionID, schema.AgentStartData{}))
	a.dispatch(ctx, schema.NewEvent(schema.EventInterruptResumed, agentID, rc.sessionID, schema.InterruptResumedData{
		InterruptID: rec.ID,
		SessionID:   rec.SessionID,
	}))

	toolMsgs, results, err := a.reconcileInterruptBatch(ctx, rc, agentID, rec)
	if err != nil {
		_ = a.interruptStore.ReleaseLease(ctx, rec.ID, owner)
		return nil, err
	}

	messages := make([]schema.Message, 0, len(rec.Messages)+len(toolMsgs))
	messages = append(messages, rec.Messages...)
	messages = append(messages, toolMsgs...)

	if result, ok := a.returnDirectResult(rec.ToolCalls, results); ok {
		directMsg := schema.NewTextMessage(a.Protocol(), schema.RoleAssistant, result.Text())
		rc.lastMsg = directMsg
		rc.reactRan = true
		rc.stopReason = schema.StopReasonComplete

		resp := a.finalizeRun(ctx, rc, a.draftResponse(rc))
		a.completeInterruptRecord(ctx, rec.ID, owner)
		return resp, nil
	}

	aiTools := a.prepareAITools(a.mergeSkillToolFilter(p.toolFilter, rc.sessionID))
	mode := &syncMode{a: a, ctx: ctx}

	stopReason, err := a.runReactLoop(ctx, rc, p, messages, aiTools, mode, rec.Iteration+1)
	if err != nil {
		_ = a.interruptStore.ReleaseLease(ctx, rec.ID, owner)
		return nil, err
	}

	rc.reactRan = true
	rc.stopReason = stopReason

	resp := a.finalizeRun(ctx, rc, a.draftResponse(rc))
	a.completeInterruptRecord(ctx, rec.ID, owner)
	return resp, nil
}

// completeInterruptRecord marks rec Completed and logs (but does not fail
// the already-delivered response on) a store error — the caller already
// has their answer; a failed Complete only risks the record sitting in
// Resuming until its lease expires and becomes reclaimable again.
func (a *Agent) completeInterruptRecord(ctx context.Context, id, owner string) {
	if err := a.interruptStore.Complete(ctx, id, owner); err != nil {
		slog.Warn("vage: complete interrupt record", "error", err, "interrupt_id", id)
	}
}

// reconcileInterruptBatch replays the suspended tool batch: pending calls
// resolve from their committed decisions (no handler runs, no
// tool_call_start/end events — consistent with none having been emitted at
// suspend time either), ordinary sibling calls execute exactly as
// executeToolBatch would in a live Run (with events, dispatched via hooks
// only — ResumeInterrupt is a synchronous entry point). Both kinds pass
// through the same tool-result guards. The returned messages/results are
// aligned with rec.ToolCalls, preserving the model's original order.
func (a *Agent) reconcileInterruptBatch(
	ctx context.Context,
	rc *runContext,
	agentID string,
	rec *interrupt.Record,
) ([]schema.Message, []schema.ToolResult, error) {
	pendingSet := make(map[string]struct{}, len(rec.Pending))
	for _, id := range rec.Pending {
		pendingSet[id] = struct{}{}
	}

	siblings := make([]schema.ToolCall, 0, len(rec.ToolCalls))
	for _, tc := range rec.ToolCalls {
		if _, isPending := pendingSet[tc.ID]; !isPending {
			siblings = append(siblings, tc)
		}
	}

	sink := func(ev schema.Event) error {
		a.dispatch(ctx, ev)
		return nil
	}

	siblingMsgs, siblingResults, err := a.executeToolBatch(ctx, rc, agentID, siblings, false, sink)
	if err != nil {
		return nil, nil, err
	}

	decisionMsgs := make(map[string]schema.Message, len(rec.Pending))
	decisionResults := make(map[string]schema.ToolResult, len(rec.Pending))
	for _, tc := range rec.ToolCalls {
		if _, isPending := pendingSet[tc.ID]; !isPending {
			continue
		}

		dec := rec.Decisions[tc.ID]

		var res schema.ToolResult
		if dec.IsError {
			res = schema.ErrorResult(tc.ID, dec.Content)
		} else {
			res = schema.TextResult(tc.ID, dec.Content)
		}

		guarded, guardEvt := a.runToolResultGuards(ctx, rc, tc, res)
		if guardEvt != nil {
			a.dispatch(ctx, *guardEvt)
		}

		decisionResults[tc.ID] = guarded
		decisionMsgs[tc.ID] = schema.NewToolResultMessage(a.Protocol(), guarded.ToolCallID, guarded.Text(), guarded.IsError)
	}

	toolMsgs := make([]schema.Message, 0, len(rec.ToolCalls))
	results := make([]schema.ToolResult, 0, len(rec.ToolCalls))
	siblingIdx := 0
	for _, tc := range rec.ToolCalls {
		if _, isPending := pendingSet[tc.ID]; isPending {
			toolMsgs = append(toolMsgs, decisionMsgs[tc.ID])
			results = append(results, decisionResults[tc.ID])
			continue
		}
		toolMsgs = append(toolMsgs, siblingMsgs[siblingIdx])
		results = append(results, siblingResults[siblingIdx])
		siblingIdx++
	}

	return toolMsgs, results, nil
}
