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
	"time"

	"github.com/vogo/aimodel"
	"github.com/vogo/vage/memory"
	"github.com/vogo/vage/schema"
)

// preflightRun performs the pre-context preparation shared by Run and
// RunStream: it validates the ChatCompleter, runs input guards (which may
// rewrite the request in place), and resolves the effective run parameters.
//
// It deliberately stops before building the context so callers can place
// their mode-specific AgentStart emission at the right point relative to
// EventContextBuilt: Run dispatches AgentStart before prepareContext, while
// RunStream builds the context up front and sends AgentStart inside the
// stream body.
func (a *Agent) preflightRun(ctx context.Context, req *schema.RunRequest) (runParams, error) {
	if a.chatCompleter == nil {
		return runParams{}, errors.New("vage: ChatCompleter is required")
	}

	if err := a.runInputGuards(ctx, req); err != nil {
		return runParams{}, err
	}

	return a.resolveRunParams(req.Options), nil
}

// prepareContext performs the context-building preparation shared by Run and
// RunStream: it builds the initial messages, injects active skill
// instructions, resolves the AI tool set (merging skill and request filters),
// and marks prompt-cache breakpoints when caching is enabled. The returned
// buildResult and tool slice feed directly into runReactLoop.
func (a *Agent) prepareContext(ctx context.Context, req *schema.RunRequest, p runParams) (buildResult, []aimodel.Tool, error) {
	br, err := a.buildInitialMessages(ctx, req)
	if err != nil {
		return buildResult{}, nil, err
	}

	// Inject skill instructions into the system prompt.
	a.injectSkillInstructions(&br, req.SessionID)

	aiTools := a.prepareAITools(a.mergeSkillToolFilter(p.toolFilter, req.SessionID))

	if a.promptCaching {
		markPromptCacheBreakpoints(br.messages, aiTools)
	}

	return br, aiTools, nil
}

// reactMode captures the sync/stream differences the shared ReAct loop
// funnels through. Everything else — iteration counting, pre/post budget
// checks, ChatRequest assembly, checkpoint writes, stop-reason detection and
// the tool batch choke point — lives in runReactLoop so the two execution
// modes cannot drift.
type reactMode interface {
	// emitIterationStart runs at the top of each iteration. The streaming
	// mode sends an EventIterationStart; the sync mode is a no-op.
	emitIterationStart(rc *runContext, iter int) error

	// executeTurn performs one LLM call for the current message set,
	// updating rc's usage and budget tracker as a side effect, and returns
	// the accumulated assistant message together with its finish reason.
	executeTurn(rc *runContext, chatReq *aimodel.ChatRequest) (aimodel.Message, aimodel.FinishReason, error)

	// toolBatchSink returns the parameters executeToolBatch needs: whether
	// to emit user-facing EventToolResult events (stream only) and the sink
	// events flow through (hook dispatch for sync, send for stream).
	toolBatchSink() (emitResultEvent bool, sink func(schema.Event) error)
}

// runReactLoop is the shared ReAct iteration skeleton for both the sync and
// stream paths. It runs iterations starting at startIter (0 for a fresh Run,
// cp.Iteration+1 for a resumed run), delegating the mode-specific work to
// mode, and returns the terminal stop reason once the loop exits. Callers map
// the stop reason to their own finalize path (finalizeRun / finalizeStream).
//
// A non-nil error aborts the loop; the caller propagates it verbatim (Run
// returns it as the second result, RunStream returns it from the stream body).
func (a *Agent) runReactLoop(
	ctx context.Context,
	rc *runContext,
	p runParams,
	messages []aimodel.Message,
	aiTools []aimodel.Tool,
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

		chatReq := &aimodel.ChatRequest{
			Model:       p.model,
			Messages:    messages,
			Temperature: p.temperature,
			MaxTokens:   p.maxTokens,
			Stop:        p.stopSeq,
			Tools:       aiTools,
		}

		assistantMsg, finishReason, err := mode.executeTurn(rc, chatReq)
		if err != nil {
			return "", err
		}

		rc.lastMsg = assistantMsg
		messages = append(messages, assistantMsg)

		if finishReason != aimodel.FinishReasonToolCalls || len(assistantMsg.ToolCalls) == 0 {
			a.saveIterationCheckpoint(ctx, rc, messages, true, schema.StopReasonComplete)
			return schema.StopReasonComplete, nil
		}

		// Post-call budget check before executing tool calls.
		if rc.tracker.Exhausted() {
			a.saveIterationCheckpoint(ctx, rc, messages, true, schema.StopReasonBudgetExhausted)
			return schema.StopReasonBudgetExhausted, nil
		}

		// Execute tool calls with bounded concurrency; events and messages
		// emerge in ToolCalls order.
		emitResultEvent, sink := mode.toolBatchSink()
		toolMsgs, err := a.executeToolBatch(ctx, rc, agentID, assistantMsg.ToolCalls, emitResultEvent, sink)
		if err != nil {
			return "", err
		}
		messages = append(messages, toolMsgs...)

		a.saveIterationCheckpoint(ctx, rc, messages, false, "")
	}

	// Max iterations exceeded.
	rc.iteration = p.maxIter - 1
	a.saveIterationCheckpoint(ctx, rc, messages, true, schema.StopReasonMaxIterations)
	return schema.StopReasonMaxIterations, nil
}

// syncMode is the non-streaming reactMode. It calls ChatCompletion, reads the
// authoritative Usage from the response, emits no IterationStart / TextDelta /
// ToolResult events, and routes tool events through hook dispatch only.
type syncMode struct {
	a   *Agent
	ctx context.Context
}

func (m *syncMode) emitIterationStart(_ *runContext, _ int) error { return nil }

func (m *syncMode) executeTurn(rc *runContext, chatReq *aimodel.ChatRequest) (aimodel.Message, aimodel.FinishReason, error) {
	resp, err := m.a.chatCompleter.ChatCompletion(m.ctx, chatReq)
	if err != nil {
		return aimodel.Message{}, "", fmt.Errorf("vage: chat completion: %w", err)
	}

	rc.totalUsage.Add(&resp.Usage)
	rc.tracker.Add(resp.Usage.TotalTokens)

	if len(resp.Choices) == 0 {
		return aimodel.Message{}, "", ErrEmptyLLMResponse
	}

	choice := resp.Choices[0]
	return choice.Message, choice.FinishReason, nil
}

func (m *syncMode) toolBatchSink() (bool, func(schema.Event) error) {
	return false, func(ev schema.Event) error {
		m.a.dispatch(m.ctx, ev)
		return nil
	}
}

// streamMode is the streaming reactMode. It calls ChatCompletionStream,
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

func (m *streamMode) executeTurn(rc *runContext, chatReq *aimodel.ChatRequest) (aimodel.Message, aimodel.FinishReason, error) {
	stream, err := m.a.chatCompleter.ChatCompletionStream(m.ctx, chatReq)
	if err != nil {
		return aimodel.Message{}, "", fmt.Errorf("vage: chat completion stream: %w", err)
	}

	var accumulated aimodel.Message
	accumulated.Role = aimodel.RoleAssistant
	var finishReason aimodel.FinishReason
	var streamBytes int

	for {
		chunk, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			_ = stream.Close()
			return aimodel.Message{}, "", fmt.Errorf("vage: stream recv: %w", recvErr)
		}

		if len(chunk.Choices) == 0 {
			continue
		}

		choice := chunk.Choices[0]
		delta := &choice.Delta

		// Emit text delta if present.
		if text := delta.Content.Text(); text != "" {
			streamBytes += len(text)
			if err := m.send(schema.NewEvent(schema.EventTextDelta, m.agentID, rc.sessionID, schema.TextDeltaData{Delta: text})); err != nil {
				_ = stream.Close()
				return aimodel.Message{}, "", err
			}
		}

		accumulated.AppendDelta(delta)

		if choice.FinishReason != nil {
			finishReason = aimodel.FinishReason(*choice.FinishReason)
		}
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

	return accumulated, finishReason, nil
}

func (m *streamMode) toolBatchSink() (bool, func(schema.Event) error) {
	return true, m.send
}

// buildResponseMsgs builds the response message slice from the last assistant message.
// For partial results (budget/iterations), it includes messages with tool calls.
// For normal completion, it always includes the message.
func (a *Agent) buildResponseMsgs(lastMsg aimodel.Message, partial bool) []schema.Message {
	if partial {
		if lastMsg.Content.Text() != "" || len(lastMsg.ToolCalls) > 0 {
			return []schema.Message{schema.NewAssistantMessage(lastMsg, a.ID())}
		}
		return []schema.Message{}
	}
	return []schema.Message{schema.NewAssistantMessage(lastMsg, a.ID())}
}

// finalizeRun is the unified termination path for Run(). It runs output guards,
// stores messages, dispatches events, and builds the RunResponse.
func (a *Agent) finalizeRun(ctx context.Context, rc *runContext, stopReason schema.StopReason) *schema.RunResponse {
	partial := stopReason != schema.StopReasonComplete
	respMsgs := a.buildResponseMsgs(rc.lastMsg, partial)

	// Run output guards. For partial results, log warnings instead of returning errors.
	guardedMsgs, err := a.runOutputGuards(ctx, rc.sessionID, respMsgs)
	if err != nil {
		if partial {
			slog.Warn("vage: output guard on partial result", "error", err, "stop_reason", stopReason)
		}
		// For normal completion, we still use the unguarded messages rather than failing.
	} else {
		respMsgs = guardedMsgs
	}

	a.storeAndPromoteMessages(ctx, rc.sessionID, rc.reqMsgs, respMsgs, rc.br.sessionMsgCount)

	// Emit budget exhaustion event if applicable.
	if stopReason == schema.StopReasonBudgetExhausted {
		a.dispatch(ctx, schema.NewEvent(schema.EventTokenBudgetExhausted, a.ID(), rc.sessionID,
			schema.TokenBudgetExhaustedData{
				Budget:     rc.tracker.Budget(),
				Used:       rc.tracker.Consumed(),
				Iterations: rc.iteration + 1,
				Estimated:  rc.estimated,
			}))
	}

	msg := ""
	if len(respMsgs) > 0 {
		msg = respMsgs[0].Content.Text()
	}

	duration := time.Since(rc.start).Milliseconds()

	a.dispatch(ctx, schema.NewEvent(schema.EventAgentEnd, a.ID(), rc.sessionID, schema.AgentEndData{
		Duration:   duration,
		Message:    msg,
		StopReason: stopReason,
	}))

	return &schema.RunResponse{
		Messages:   respMsgs,
		SessionID:  rc.sessionID,
		Usage:      &rc.totalUsage,
		Duration:   duration,
		StopReason: stopReason,
	}
}

// finalizeStream is the unified termination path for RunStream(). It runs output guards,
// stores messages, dispatches events via send, and returns nil for clean stream close.
func (a *Agent) finalizeStream(
	ctx context.Context,
	send func(schema.Event) error,
	rc *runContext,
	req *schema.RunRequest,
	stopReason schema.StopReason,
) error {
	partial := stopReason != schema.StopReasonComplete
	respMsgs := a.buildResponseMsgs(rc.lastMsg, partial)

	// Run output guards. For partial results, log warnings instead of returning errors.
	guardedMsgs, err := a.runOutputGuards(ctx, rc.sessionID, respMsgs)
	if err != nil {
		if partial {
			slog.Warn("vage: output guard on partial stream result", "error", err, "stop_reason", stopReason)
		} else {
			return err
		}
	} else {
		respMsgs = guardedMsgs
	}

	a.storeAndPromoteMessages(ctx, rc.sessionID, req.Messages, respMsgs, rc.br.sessionMsgCount)

	// Emit budget exhaustion event if applicable.
	if stopReason == schema.StopReasonBudgetExhausted {
		if err := send(schema.NewEvent(schema.EventTokenBudgetExhausted, a.ID(), rc.sessionID,
			schema.TokenBudgetExhaustedData{
				Budget:     rc.tracker.Budget(),
				Used:       rc.tracker.Consumed(),
				Iterations: rc.iteration + 1,
				Estimated:  rc.estimated,
			})); err != nil {
			return err
		}
	}

	msg := ""
	if len(respMsgs) > 0 {
		msg = respMsgs[0].Content.Text()
	}

	return send(schema.NewEvent(schema.EventAgentEnd, a.ID(), rc.sessionID, schema.AgentEndData{
		Duration:   time.Since(rc.start).Milliseconds(),
		Message:    msg,
		StopReason: stopReason,
	}))
}

// storeAndPromoteMessages stores request and response messages in working memory
// and promotes them to session memory. sessionMsgCount is the original session
// message count (pre-compression), used as key offset to avoid collisions.
func (a *Agent) storeAndPromoteMessages(ctx context.Context, sessionID string, reqMsgs, respMsgs []schema.Message, sessionMsgCount int) {
	if a.memoryManager == nil {
		return
	}

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

	if err := a.memoryManager.PromoteToSession(ctx, working); err != nil {
		slog.Warn("vage: promote to session", "error", err)
	}
}

// buildSend builds a send function with the middleware chain and hook dispatch applied.
func (a *Agent) buildSend(ctx context.Context, raw func(schema.Event) error) func(schema.Event) error {
	send := raw
	// Apply middlewares in reverse order so the first middleware is outermost.
	for i := len(a.middlewares) - 1; i >= 0; i-- {
		send = a.middlewares[i](send)
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
