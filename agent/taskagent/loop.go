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
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"time"

	"github.com/vogo/vage/largemodel"
	"github.com/vogo/vage/memory"
	"github.com/vogo/vage/schema"
)

// preflightEntry performs universal and policy-gated preparation shared by
// all run-class entries. Caller validation and interrupt-config checks always
// run; input guards and run-parameter resolution run only when the entry
// policy and request shape allow it.
//
// It deliberately stops before building the context so callers can place
// their mode-specific AgentStart emission at the right point relative to
// EventContextBuilt: Run dispatches AgentStart before prepareContext, while
// RunStream builds the context up front and sends AgentStart inside the
// stream body. Resume-only steps (checkpoint load, interrupt store access,
// lease acquisition) stay in each entry after this returns.
func (a *Agent) preflightEntry(ctx context.Context, policy entryPolicy, req *schema.RunRequest) (runParams, error) {
	if a.caller == nil {
		return runParams{}, errors.New("vage: model caller is required")
	}

	if err := a.checkInterruptConfig(); err != nil {
		return runParams{}, err
	}

	if policy.inputGuards && req != nil {
		if err := a.runInputGuards(ctx, req); err != nil {
			return runParams{}, err
		}
	}

	if req != nil {
		return a.resolveRunParams(req.Options), nil
	}

	return runParams{}, nil
}

// prepareContext performs the context-building preparation shared by Run and
// RunStream: it builds the initial messages, injects active skill
// instructions, resolves the AI tool set (merging skill and request filters),
// and marks prompt-cache breakpoints when caching is enabled. The returned
// buildResult and tool slice feed directly into runReactLoop.
func (a *Agent) prepareContext(ctx context.Context, req *schema.RunRequest, p runParams) (buildResult, []schema.ToolDef, error) {
	br, err := a.buildInitialMessages(ctx, req)
	if err != nil {
		return buildResult{}, nil, err
	}

	// Inject skill instructions into the system prompt.
	a.injectSkillInstructions(&br, req.SessionID)

	aiTools := a.prepareAITools(a.mergeSkillToolFilter(p.toolFilter, req.SessionID))

	return br, aiTools, nil
}

// reactMode captures the sync/stream differences the shared ReAct loop
// funnels through. Everything else — iteration counting, pre/post budget
// checks, Request assembly, checkpoint writes, stop-reason detection and
// the tool batch choke point — lives in runReactLoop so the two execution
// modes cannot drift.
type reactMode interface {
	// emitIterationStart runs at the top of each iteration. The streaming
	// mode sends an EventIterationStart; the sync mode is a no-op.
	emitIterationStart(rc *runContext, iter int) error

	// executeTurn performs one LLM call for the current message set,
	// updating rc's usage and budget tracker as a side effect, and returns
	// the accumulated assistant message together with its finish reason.
	executeTurn(rc *runContext, chatReq *largemodel.Request) (schema.Message, largemodel.FinishReason, error)

	// toolBatchSink returns the parameters executeToolBatch needs: whether
	// to emit user-facing EventToolResult events (stream only) and the sink
	// events flow through (hook dispatch for sync, send for stream).
	toolBatchSink() (emitResultEvent bool, sink func(schema.Event) error)
}

// runReactLoop is the shared ReAct iteration skeleton for both the sync and
// stream paths. It runs iterations starting at startIter (0 for a fresh Run,
// cp.Iteration+1 for a resumed run), delegating the mode-specific work to
// mode, and returns the terminal stop reason once the loop exits. Callers map
// the stop reason into rc so the shared terminal path can read it.
//
// A non-nil error aborts the loop; the caller propagates it verbatim (Run
// returns it as the second result, RunStream returns it from the stream body).
func (a *Agent) runReactLoop(
	ctx context.Context,
	rc *runContext,
	p runParams,
	messages []schema.Message,
	aiTools []schema.ToolDef,
	mode reactMode,
	startIter int,
) (schema.StopReason, error) {
	agentID := a.ID()

	for iter := startIter; iter < p.maxIter; iter++ {
		rc.iteration = iter

		// Pre-call budget check.
		if rc.tracker.Exhausted() {
			a.saveIterationCheckpoint(ctx, rc, messages, true, schema.StopReasonBudgetExhausted)
			return schema.StopReasonBudgetExhausted, nil
		}

		if err := mode.emitIterationStart(rc, iter); err != nil {
			return "", err
		}

		chatReq := &largemodel.Request{
			Model:         p.model,
			Messages:      messages,
			Temperature:   p.temperature,
			MaxTokens:     p.maxTokens,
			Stop:          p.stopSeq,
			Tools:         aiTools,
			PromptCaching: a.promptCaching,
		}

		assistantMsg, finishReason, err := mode.executeTurn(rc, chatReq)
		if err != nil {
			return "", err
		}

		rc.lastMsg = assistantMsg
		messages = append(messages, assistantMsg)

		if finishReason != largemodel.FinishReasonToolCalls || len(assistantMsg.ToolCalls()) == 0 {
			a.saveIterationCheckpoint(ctx, rc, messages, true, schema.StopReasonComplete)
			return schema.StopReasonComplete, nil
		}

		// Post-call budget check before executing tool calls.
		if rc.tracker.Exhausted() {
			a.saveIterationCheckpoint(ctx, rc, messages, true, schema.StopReasonBudgetExhausted)
			return schema.StopReasonBudgetExhausted, nil
		}

		// Interrupt choke point: a configured InterruptPolicy gets the
		// batch before any handler runs. A hit suspends here — no
		// checkpoint write (interrupt.Store, not IterationStore, is the
		// resumable state) and no tool dispatch — and returns only after
		// the record is durably persisted; a persistence failure is a hard
		// Run error, never a synthesised stop reason. See
		// agent/taskagent/interrupt.go.
		if a.interruptPolicy != nil {
			desc, interrupted, err := a.maybeInterrupt(ctx, rc, p, messages, assistantMsg.ToolCalls())
			if err != nil {
				return "", err
			}
			if interrupted {
				rc.interruptDesc = desc
				return schema.StopReasonInterrupted, nil
			}
		}

		// Execute tool calls with bounded concurrency; events and messages
		// emerge in ToolCalls order.
		emitResultEvent, sink := mode.toolBatchSink()
		toolMsgs, results, err := a.executeToolBatch(ctx, rc, agentID, assistantMsg.ToolCalls(), emitResultEvent, sink)
		if err != nil {
			return "", err
		}
		messages = append(messages, toolMsgs...)

		// A successful ReturnDirect tool short-circuits the ReAct loop after
		// the batch: its guard-passed result becomes the final assistant
		// message and no further model round happens. Selection follows the
		// model's ToolCalls order — the first configured-and-successful call
		// wins, the batch's completion order does not participate. Failures
		// never short-circuit, and unconfigured tools keep the existing
		// behaviour.
		if result, ok := a.returnDirectResult(assistantMsg.ToolCalls(), results); ok {
			directMsg := schema.NewTextMessage(a.Protocol(), schema.RoleAssistant, result.Text())
			rc.lastMsg = directMsg
			messages = append(messages, directMsg)

			// Final terminal checkpoint straight away: this turn already
			// carries the complete batch plus the direct-return message, so
			// no non-terminal snapshot worth resuming is left behind.
			a.saveIterationCheckpoint(ctx, rc, messages, true, schema.StopReasonComplete)
			return schema.StopReasonComplete, nil
		}

		a.saveIterationCheckpoint(ctx, rc, messages, false, "")
	}

	// Max iterations exceeded.
	rc.iteration = p.maxIter - 1
	a.saveIterationCheckpoint(ctx, rc, messages, true, schema.StopReasonMaxIterations)
	return schema.StopReasonMaxIterations, nil
}

// returnDirectResult selects the first tool call, in the model's ToolCalls
// order, whose name is configured for direct return and whose guard-passed
// result succeeded. results is the parallel slice executeToolBatch produced,
// aligned with toolCalls by index. The second result is false when no call in
// the batch qualifies — the loop then continues normally with the batch's
// messages. An empty configuration short-circuits immediately so the default
// ReAct path pays nothing.
func (a *Agent) returnDirectResult(toolCalls []schema.ToolCall, results []schema.ToolResult) (schema.ToolResult, bool) {
	if len(a.returnDirectTools) == 0 {
		return schema.ToolResult{}, false
	}

	for i, tc := range toolCalls {
		if _, ok := a.returnDirectTools[tc.Name]; !ok {
			continue
		}

		if i >= len(results) || results[i].IsError {
			continue
		}

		return results[i], true
	}

	return schema.ToolResult{}, false
}

// syncMode is the non-streaming reactMode. It calls Caller.Call, reads the
// authoritative Usage from the response, emits no IterationStart / TextDelta /
// ToolResult events, and routes tool events through hook dispatch only.
type syncMode struct {
	a   *Agent
	ctx context.Context
}

func (m *syncMode) emitIterationStart(_ *runContext, _ int) error { return nil }

func (m *syncMode) executeTurn(rc *runContext, chatReq *largemodel.Request) (schema.Message, largemodel.FinishReason, error) {
	resp, err := m.a.caller.Call(m.ctx, chatReq)
	if err != nil {
		return schema.Message{}, "", fmt.Errorf("vage: chat completion: %w", err)
	}

	rc.totalUsage.Add(&resp.Usage)
	rc.tracker.Add(resp.Usage.TotalTokens)

	return resp.Message, resp.FinishReason, nil
}

func (m *syncMode) toolBatchSink() (bool, func(schema.Event) error) {
	return false, func(ev schema.Event) error {
		m.a.dispatch(m.ctx, ev)
		return nil
	}
}

// streamMode is the streaming reactMode. It calls Caller.CallStream,
// accumulates the assistant message chunk by chunk while emitting TextDelta,
// sends an IterationStart per iteration, prefers the stream Usage (falling
// back to byte-based token estimation when absent), and forwards tool results
// as EventToolResult.
type streamMode struct {
	a       *Agent
	ctx     context.Context
	agentID string
	send    func(schema.Event) error
}

func (m *streamMode) emitIterationStart(rc *runContext, iter int) error {
	return m.send(schema.NewEvent(schema.EventIterationStart, m.agentID, rc.sessionID, schema.IterationStartData{
		Iteration: iter,
	}))
}

func (m *streamMode) executeTurn(rc *runContext, chatReq *largemodel.Request) (schema.Message, largemodel.FinishReason, error) {
	stream, err := m.a.caller.CallStream(m.ctx, chatReq)
	if err != nil {
		return schema.Message{}, "", fmt.Errorf("vage: chat completion stream: %w", err)
	}

	var (
		acc         largemodel.StreamAccumulator
		streamBytes int
	)

	for {
		chunk, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}

		if recvErr != nil {
			_ = stream.Close()
			return schema.Message{}, "", fmt.Errorf("vage: stream recv: %w", recvErr)
		}

		// Emit text delta if present.
		if text := chunk.TextDelta; text != "" {
			streamBytes += len(text)

			if err := m.send(schema.NewEvent(schema.EventTextDelta, m.agentID, rc.sessionID, schema.TextDeltaData{Delta: text})); err != nil {
				_ = stream.Close()
				return schema.Message{}, "", err
			}
		}

		acc.Add(chunk)
	}

	// Read actual usage from stream before closing (populated from final chunk).
	streamUsage := stream.Usage()

	_ = stream.Close()

	if streamUsage != nil {
		rc.totalUsage.Add(streamUsage)
		rc.tracker.Add(streamUsage.TotalTokens)

		// Emit through the stream pipeline so downstream consumers
		// (CLI, phase trackers) can observe token usage.
		_ = m.send(schema.NewEvent(schema.EventLLMCallEnd, m.agentID, rc.sessionID, schema.LLMCallEndData{
			Model:            chatReq.Model,
			PromptTokens:     streamUsage.PromptTokens,
			CompletionTokens: streamUsage.CompletionTokens,
			TotalTokens:      streamUsage.TotalTokens,
			CacheReadTokens:  streamUsage.CacheReadTokens,
			Stream:           true,
		}))
	} else {
		// Estimate token usage from stream bytes (4 bytes per token heuristic).
		estimatedTokens := (streamBytes + 3) / 4
		if estimatedTokens < 1 && streamBytes > 0 {
			estimatedTokens = 1
		}

		rc.tracker.Add(estimatedTokens)
	}

	return acc.AssistantMessage(m.a.Protocol()), acc.FinishReason(), nil
}

func (m *streamMode) toolBatchSink() (bool, func(schema.Event) error) {
	return true, m.send
}

// withAgentID stamps the agent id onto a message produced by this agent.
func withAgentID(msg schema.Message, agentID string) schema.Message {
	msg.AgentID = agentID

	return msg
}

// buildResponseMsgs builds the response message slice from the last assistant message.
// For partial results (budget/iterations), it includes messages with tool calls.
// For normal completion, it always includes the message.
func (a *Agent) buildResponseMsgs(lastMsg schema.Message, partial bool) []schema.Message {
	if partial {
		if lastMsg.Text() != "" || len(lastMsg.ToolCalls()) > 0 {
			return []schema.Message{withAgentID(lastMsg, a.ID())}
		}
		return []schema.Message{}
	}
	return []schema.Message{withAgentID(lastMsg, a.ID())}
}

// draftResponse assembles the RunResponse the Agent middleware chain sees.
// It is deliberately a draft: output guards, memory persistence and the
// terminal events all run afterwards on whatever the chain returns, so a
// middleware's rewrite is subject to the same guard and persistence rules as
// a plain ReAct answer.
func (a *Agent) draftResponse(rc *runContext) *schema.RunResponse {
	partial := rc.stopReason != schema.StopReasonComplete

	resp := &schema.RunResponse{
		Messages:   a.buildResponseMsgs(rc.lastMsg, partial),
		SessionID:  rc.sessionID,
		Usage:      &rc.totalUsage,
		Duration:   time.Since(rc.start).Milliseconds(),
		StopReason: rc.stopReason,
	}

	// Interrupt is stamped here — the only place a StopReasonInterrupted
	// draft is ever produced — and only after maybeInterrupt has already
	// confirmed the record persisted. A middleware can still rewrite
	// StopReason/Interrupt afterward like any other draft field (AC-12);
	// what this guarantees is that the framework itself never reports an
	// interrupt that was not durably created.
	if rc.stopReason == schema.StopReasonInterrupted {
		resp.Interrupt = rc.interruptDesc
	}

	return resp
}

// partialResult reports whether the terminal path should treat the response as
// an interrupted result, which only downgrades output-guard failures to
// warnings. It reads the loop's own outcome rather than resp.StopReason: a
// middleware may rewrite the reported stop reason, and guard strictness is not
// a middleware's to relax.
func (rc *runContext) partialResult() bool {
	return rc.reactRan && rc.stopReason != schema.StopReasonComplete
}

// budgetExhausted reports whether the ReAct loop really ran out of budget.
// Same reasoning as partialResult: the event carries the tracker's own
// numbers, so it must not fire for a synthesised stop reason.
func (rc *runContext) budgetExhausted() bool {
	return rc.reactRan && rc.stopReason == schema.StopReasonBudgetExhausted
}

// budgetExhaustedEvent builds the token-budget event for the terminal path.
func (a *Agent) budgetExhaustedEvent(rc *runContext) schema.Event {
	return schema.NewEvent(schema.EventTokenBudgetExhausted, a.ID(), rc.sessionID,
		schema.TokenBudgetExhaustedData{
			Budget:     rc.tracker.Budget(),
			Used:       rc.tracker.Consumed(),
			Iterations: rc.iteration + 1,
			Estimated:  rc.estimated,
		})
}

// terminalMessage returns the text AgentEnd reports, read from the final
// (post-guard) response so the event and the response can never disagree.
func terminalMessage(resp *schema.RunResponse) string {
	if len(resp.Messages) == 0 {
		return ""
	}

	return resp.Messages[0].Text()
}

// finalizeRun is the unified termination path for Run() and Resume(). It runs
// output guards over the response the middleware chain produced, stores
// messages, dispatches events, and stamps the framework-owned response fields.
func (a *Agent) finalizeRun(ctx context.Context, rc *runContext, resp *schema.RunResponse) *schema.RunResponse {
	// Run output guards. For partial results, log warnings instead of returning errors.
	guardedMsgs, err := a.runOutputGuards(ctx, rc.sessionID, resp.Messages)
	if err != nil {
		if rc.partialResult() {
			slog.Warn("vage: output guard on partial result", "error", err, "stop_reason", rc.stopReason)
		}
		// For normal completion, we still use the unguarded messages rather than failing.
	} else {
		resp.Messages = guardedMsgs
	}

	// An interrupted Run leaves the assistant/tool-call pair open — the
	// matching tool results do not exist yet — so the response messages
	// (which include that open pair, for the caller's visibility) are
	// withheld from session memory here. The request messages are still
	// promoted normally: maybeInterrupt already reserved the key range
	// after them (see interrupt.go), so Resume's own finalize picks up
	// writing exactly where this leaves off, with no duplicate keys and
	// no dangling tool call ever visible in memory.
	a.storeAndPromoteMessages(ctx, rc.sessionID, rc.reqMsgs, a.interruptSafeRespMsgs(rc, resp), rc.br.sessionMsgCount)

	// Emit budget exhaustion event if applicable.
	if rc.budgetExhausted() {
		a.dispatch(ctx, a.budgetExhaustedEvent(rc))
	}

	duration := time.Since(rc.start).Milliseconds()

	a.dispatch(ctx, schema.NewEvent(schema.EventAgentEnd, a.ID(), rc.sessionID, schema.AgentEndData{
		Duration:   duration,
		Message:    terminalMessage(resp),
		StopReason: resp.StopReason,
	}))

	// SessionID and duration are framework invariants. A middleware owns the
	// messages, metadata, usage and stop reason it reports; it does not get to
	// claim the run belonged to another session or took another amount of time.
	resp.SessionID = rc.sessionID
	resp.Duration = duration

	return resp
}

// finalizeStream is the unified termination path for RunStream(). It mirrors
// finalizeRun over the stream's send function and returns nil for a clean
// stream close.
func (a *Agent) finalizeStream(
	ctx context.Context,
	send func(schema.Event) error,
	rc *runContext,
	resp *schema.RunResponse,
) error {
	// Run output guards. For partial results, log warnings instead of returning errors.
	guardedMsgs, err := a.runOutputGuards(ctx, rc.sessionID, resp.Messages)
	if err != nil {
		if rc.partialResult() {
			slog.Warn("vage: output guard on partial stream result", "error", err, "stop_reason", rc.stopReason)
		} else {
			return err
		}
	} else {
		resp.Messages = guardedMsgs
	}

	// See finalizeRun for why an interrupted Run withholds resp.Messages.
	a.storeAndPromoteMessages(ctx, rc.sessionID, rc.reqMsgs, a.interruptSafeRespMsgs(rc, resp), rc.br.sessionMsgCount)

	// Emit budget exhaustion event if applicable.
	if rc.budgetExhausted() {
		if err := send(a.budgetExhaustedEvent(rc)); err != nil {
			return err
		}
	}

	return send(schema.NewEvent(schema.EventAgentEnd, a.ID(), rc.sessionID, schema.AgentEndData{
		Duration:   time.Since(rc.start).Milliseconds(),
		Message:    terminalMessage(resp),
		StopReason: resp.StopReason,
	}))
}

// interruptSafeRespMsgs returns resp.Messages unless the loop actually
// suspended (rc.stopReason, not the possibly middleware-rewritten
// resp.StopReason — same reasoning as partialResult/budgetExhausted), in
// which case it returns nil so storeAndPromoteMessages never persists the
// still-open assistant/tool-call pair.
func (a *Agent) interruptSafeRespMsgs(rc *runContext, resp *schema.RunResponse) []schema.Message {
	if rc.stopReason == schema.StopReasonInterrupted {
		return nil
	}
	return resp.Messages
}

// storeAndPromoteMessages stores request and response messages in working memory
// and promotes them to session memory. sessionMsgCount is the original session
// message count (pre-compression), used as key offset to avoid collisions.
// An empty sessionID skips session writes entirely (Run still proceeds).
func (a *Agent) storeAndPromoteMessages(ctx context.Context, sessionID string, reqMsgs, respMsgs []schema.Message, sessionMsgCount int) {
	if a.memoryManager == nil || sessionID == "" {
		return
	}

	scoped := a.memoryManager.ForSession(a.ID(), sessionID)
	working := memory.NewWorkingMemory(a.ID(), sessionID)

	idx := sessionMsgCount

	for _, msg := range reqMsgs {
		key := fmt.Sprintf("msg:%06d", idx)
		if err := working.Set(ctx, key, msg, 0); err != nil {
			slog.Warn("vage: store request message", "error", err)
		}

		idx++
	}

	for _, msg := range respMsgs {
		key := fmt.Sprintf("msg:%06d", idx)
		if err := working.Set(ctx, key, msg, 0); err != nil {
			slog.Warn("vage: store response message", "error", err)
		}

		idx++
	}

	if err := scoped.PromoteToSession(ctx, working); err != nil {
		slog.Warn("vage: promote to session", "error", err)
	}
}

// buildSend builds a send function with the middleware chain and hook dispatch applied.
func (a *Agent) buildSend(ctx context.Context, raw func(schema.Event) error) func(schema.Event) error {
	send := raw
	// Apply middlewares in reverse order so the first middleware is outermost.
	for _, v := range slices.Backward(a.streamMiddleware) {
		send = v(send)
	}

	next := send
	send = func(e schema.Event) error {
		// Skip hook dispatch for LLM lifecycle events — MetricsMiddleware
		// already dispatches these directly to hooks to avoid double-counting.
		if e.Type != schema.EventLLMCallEnd {
			a.hookManager.Dispatch(ctx, e)
		}

		return next(e)
	}

	return send
}
