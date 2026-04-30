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
	"log/slog"
	"time"

	"github.com/vogo/aimodel"
	"github.com/vogo/vage/checkpoint"
	"github.com/vogo/vage/schema"
)

// ErrEmptyLLMResponse is returned by Resume when the chat completion
// returns no choices. Mirrors the corresponding error path in Run
// (which returns a plain string error) but is exported so callers can
// distinguish it from generic transport failures.
var ErrEmptyLLMResponse = errors.New("vage: empty response from LLM")

// saveIterationCheckpoint persists a snapshot of the ReAct loop state at
// the end of an iteration. final == false snapshots are written after a
// tool batch completes; final == true snapshots accompany every Run
// terminator (Complete / BudgetExhausted / MaxIterations).
//
// The function is a no-op when no IterationStore is configured. Save
// failures are logged as warnings and dropped — checkpointing is best
// effort and must never break the live ReAct loop. On success, the
// helper emits an EventCheckpointWritten hook event so trace consumers
// can correlate the iteration with the persisted snapshot.
func (a *Agent) saveIterationCheckpoint(
	ctx context.Context,
	rc *runContext,
	messages []aimodel.Message,
	final bool,
	stopReason schema.StopReason,
) {
	if a.iterationStore == nil {
		return
	}

	cp := &checkpoint.Checkpoint{
		SessionID:       rc.sessionID,
		AgentID:         a.ID(),
		Iteration:       rc.iteration,
		Final:           final,
		Messages:        cloneMessagesForCheckpoint(messages),
		SessionMsgCount: rc.br.sessionMsgCount,
		Usage:           rc.totalUsage,
		Estimated:       rc.estimated,
	}
	if final {
		cp.StopReason = stopReason
	}

	if err := a.iterationStore.Save(ctx, cp); err != nil {
		slog.Warn("vage: save iteration checkpoint",
			"error", err,
			"session_id", rc.sessionID,
			"iteration", rc.iteration,
			"final", final)
		return
	}

	a.dispatch(ctx, schema.NewEvent(schema.EventCheckpointWritten, a.ID(), rc.sessionID,
		schema.CheckpointWrittenData{
			CheckpointID:  cp.ID,
			Sequence:      cp.Sequence,
			Iteration:     cp.Iteration,
			Final:         cp.Final,
			StopReason:    cp.StopReason,
			MessagesCount: len(cp.Messages),
			TotalTokens:   cp.Usage.TotalTokens,
		}))
}

// cloneMessagesForCheckpoint copies the top-level slice; aimodel.Message
// internals are immutable post-creation by TaskAgent convention.
func cloneMessagesForCheckpoint(in []aimodel.Message) []aimodel.Message {
	if len(in) == 0 {
		return nil
	}
	out := make([]aimodel.Message, len(in))
	copy(out, in)
	return out
}

// Resume re-enters the ReAct loop for sessionID using the latest stored
// IterationStore checkpoint.
//
// Errors:
//   - checkpoint.ErrInvalidArgument when no IterationStore is configured
//     or when the latest checkpoint references a different agent.
//   - checkpoint.ErrCheckpointNotFound when the session has no checkpoints.
//   - checkpoint.ErrAlreadyFinal when the latest checkpoint is Final ==
//     true. Callers can distinguish this from a successful resume to
//     decide whether to surface the stored response (via Load).
//
// Resume bypasses input guards (the original Run already vetted the
// input). Output guards run on the resumed final response. Tool result
// guards continue to run on every fresh tool execution.
func (a *Agent) Resume(ctx context.Context, sessionID string) (*schema.RunResponse, error) {
	if a.chatCompleter == nil {
		return nil, errors.New("vage: ChatCompleter is required")
	}
	if a.iterationStore == nil {
		return nil, fmt.Errorf("%w: no IterationStore configured", checkpoint.ErrInvalidArgument)
	}
	if sessionID == "" {
		return nil, fmt.Errorf("%w: session id is empty", checkpoint.ErrInvalidArgument)
	}

	cp, err := a.iterationStore.Load(ctx, sessionID, "")
	if err != nil {
		return nil, err
	}
	if cp.AgentID != "" && cp.AgentID != a.ID() {
		return nil, fmt.Errorf("%w: checkpoint agent %q does not match this agent %q",
			checkpoint.ErrInvalidArgument, cp.AgentID, a.ID())
	}
	if cp.Final {
		return nil, checkpoint.ErrAlreadyFinal
	}

	p := a.resolveRunParams(nil)
	agentID := a.ID()

	rc := &runContext{
		sessionID:  sessionID,
		start:      time.Now(),
		tracker:    newBudgetTracker(p.runTokenBudget),
		totalUsage: cp.Usage,
		estimated:  cp.Estimated,
		br: buildResult{
			messages:        cp.Messages,
			sessionMsgCount: cp.SessionMsgCount,
		},
		// reqMsgs left nil: the original user input was already promoted
		// to working memory in the (eventually called) finalize of the
		// resumed Run; storeAndPromoteMessages skips the request loop
		// when reqMsgs is empty.
		reqMsgs: nil,
	}

	a.dispatch(ctx, schema.NewEvent(schema.EventAgentStart, agentID, rc.sessionID, schema.AgentStartData{}))

	messages := cp.Messages
	aiTools := a.prepareAITools(a.mergeSkillToolFilter(p.toolFilter, rc.sessionID))
	if a.promptCaching {
		markPromptCacheBreakpoints(messages, aiTools)
	}

	startIter := cp.Iteration + 1
	return a.runResumeLoop(ctx, rc, p, messages, aiTools, startIter)
}

// runResumeLoop is the ReAct loop body shared with Run, starting at a
// resumed iteration index. It mirrors the structure of Run's main loop
// but begins at startIter instead of 0; checkpoint write points are
// identical so a resumed run leaves the same audit trail as a fresh run.
//
// Errors from chat completion or empty-choices responses propagate up
// to the Resume caller — same contract as Run.
func (a *Agent) runResumeLoop(
	ctx context.Context,
	rc *runContext,
	p runParams,
	messages []aimodel.Message,
	aiTools []aimodel.Tool,
	startIter int,
) (*schema.RunResponse, error) {
	agentID := a.ID()

	for iter := startIter; iter < p.maxIter; iter++ {
		rc.iteration = iter

		if rc.tracker.Exhausted() {
			a.saveIterationCheckpoint(ctx, rc, messages, true, schema.StopReasonBudgetExhausted)
			return a.finalizeRun(ctx, rc, schema.StopReasonBudgetExhausted), nil
		}

		chatReq := &aimodel.ChatRequest{
			Model:       p.model,
			Messages:    messages,
			Temperature: p.temperature,
			MaxTokens:   p.maxTokens,
			Stop:        p.stopSeq,
			Tools:       aiTools,
		}

		resp, err := a.chatCompleter.ChatCompletion(ctx, chatReq)
		if err != nil {
			return nil, fmt.Errorf("vage: chat completion: %w", err)
		}

		rc.totalUsage.Add(&resp.Usage)
		rc.tracker.Add(resp.Usage.TotalTokens)

		if len(resp.Choices) == 0 {
			return nil, ErrEmptyLLMResponse
		}

		choice := resp.Choices[0]
		assistantMsg := choice.Message
		rc.lastMsg = assistantMsg
		messages = append(messages, assistantMsg)

		if choice.FinishReason != aimodel.FinishReasonToolCalls || len(assistantMsg.ToolCalls) == 0 {
			a.saveIterationCheckpoint(ctx, rc, messages, true, schema.StopReasonComplete)
			return a.finalizeRun(ctx, rc, schema.StopReasonComplete), nil
		}

		if rc.tracker.Exhausted() {
			a.saveIterationCheckpoint(ctx, rc, messages, true, schema.StopReasonBudgetExhausted)
			return a.finalizeRun(ctx, rc, schema.StopReasonBudgetExhausted), nil
		}

		sink := func(ev schema.Event) error {
			a.dispatch(ctx, ev)
			return nil
		}
		toolMsgs, _ := a.executeToolBatch(ctx, rc, agentID, assistantMsg.ToolCalls, false, sink)
		messages = append(messages, toolMsgs...)

		a.saveIterationCheckpoint(ctx, rc, messages, false, "")
	}

	rc.iteration = p.maxIter - 1
	a.saveIterationCheckpoint(ctx, rc, messages, true, schema.StopReasonMaxIterations)
	return a.finalizeRun(ctx, rc, schema.StopReasonMaxIterations), nil
}
