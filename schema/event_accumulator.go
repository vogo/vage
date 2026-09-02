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

package schema

import (
	"errors"
	"strings"
)

// EventAccumulator folds a run's event stream back into the shape a
// non-streaming caller wants: the assembled text, the tool calls and results
// that got there, and the run's terminal accounting. It is the event-level
// counterpart of largemodel.StreamAccumulator, which does the same job one
// layer down for provider chunks.
//
// It accumulates every event handed to Add, without filtering on AgentID or
// ParentID. A stream carrying more than one agent's events — a merged stream,
// or a run that relays subagent events onto the parent stream — will therefore
// interleave their text. Callers that want a single agent's view must filter
// before calling Add; picking that policy is not the accumulator's job.
//
// The zero value is ready to use. It is not safe for concurrent use.
type EventAccumulator struct {
	text         strings.Builder
	toolCalls    []ToolCallStartData
	toolResults  []ToolResultData
	iterations   int
	usage        Usage
	sawUsage     bool
	finalMessage string
	sawAgentEnd  bool
	stopReason   StopReason
	duration     int64
	sessionID    string
	errs         []error
}

// Add folds one event into the accumulated run. Event types the accumulator
// has no use for are ignored, so it stays correct as new event types are
// introduced.
//
// Payloads are matched by value, the form every event this framework emits
// takes. EventData's marker has a value receiver, so a pointer to a payload
// also satisfies the interface and would fall through here as unrecognised —
// as it would at every other consumer that type-switches on Event.Data. Emit
// payloads by value.
func (a *EventAccumulator) Add(e Event) {
	if a.sessionID == "" {
		a.sessionID = e.SessionID
	}

	switch data := e.Data.(type) {
	case TextDeltaData:
		a.text.WriteString(data.Delta)
	case ToolCallStartData:
		a.toolCalls = append(a.toolCalls, data)
	case ToolResultData:
		a.toolResults = append(a.toolResults, data)
	case IterationStartData:
		a.iterations++
	case AgentEndData:
		a.sawAgentEnd = true
		a.finalMessage = data.Message

		if data.StopReason != "" {
			a.stopReason = data.StopReason
		}

		a.duration = data.Duration
	case LLMCallEndData:
		a.sawUsage = true
		a.usage.Add(&Usage{
			PromptTokens:     data.PromptTokens,
			CompletionTokens: data.CompletionTokens,
			TotalTokens:      data.TotalTokens,
			CacheReadTokens:  data.CacheReadTokens,
			CacheWriteTokens: data.CacheWriteTokens,
			ReasoningTokens:  data.ReasoningTokens,
		})
	case ErrorData:
		a.errs = append(a.errs, errors.New(data.Message))
	}
}

// Text returns every text delta seen so far, concatenated in arrival order.
//
// This is what the stream showed live, which is not always the run's answer:
// output guards and Agent middleware rewrite the terminal message after the
// deltas have gone out, and a run whose agent is not a StreamAgent produces no
// deltas at all. Message is the authoritative answer; Text is the live trace.
func (a *EventAccumulator) Text() string { return a.text.String() }

// Message returns the terminal message reported by AgentEnd, and reports
// whether an AgentEnd event arrived at all.
//
// AgentEnd carries the message after output guards and Agent middleware have
// had their say, which makes it the run's single source of truth for the
// answer (agent-core AC-11) — the deltas already on the wire are not replayed
// when it differs. A run wrapped by agent.RunToStream (RouterAgent,
// WorkflowAgent, any non-streaming agent) emits nothing but AgentStart and
// AgentEnd, so this is the only place its answer appears.
//
// The bool distinguishes "the run finished and said the message is empty" from
// "no AgentEnd arrived" — a stream that errored, was cancelled, or was
// abandoned early. On a stream carrying several AgentEnd events the last one
// wins, matching how StopReason and Duration are folded.
func (a *EventAccumulator) Message() (string, bool) { return a.finalMessage, a.sawAgentEnd }

// ToolCalls returns the tool invocations the run started, in arrival order.
// A call whose result never arrived still appears here.
func (a *EventAccumulator) ToolCalls() []ToolCallStartData { return a.toolCalls }

// ToolResults returns the tool results the run produced, in arrival order.
func (a *EventAccumulator) ToolResults() []ToolResultData { return a.toolResults }

// Iterations counts the ReAct iterations the run entered — one per
// IterationStart event, so a run that stopped mid-iteration still counts it.
func (a *EventAccumulator) Iterations() int { return a.iterations }

// StopReason returns the terminal stop reason reported by AgentEnd, or the
// empty string when the run produced no AgentEnd event (it errored, was
// cancelled, or the stream was abandoned early).
func (a *EventAccumulator) StopReason() StopReason { return a.stopReason }

// Duration returns the run duration in milliseconds as reported by AgentEnd,
// or zero when no AgentEnd event arrived.
func (a *EventAccumulator) Duration() int64 { return a.duration }

// SessionID returns the session id carried by the first event that named one.
func (a *EventAccumulator) SessionID() string { return a.sessionID }

// Usage returns the token usage summed over the run's LLM calls, or nil when
// the stream carried no LLMCallEnd events. Those events come from the
// largemodel metrics middleware, so a caller that did not install it gets nil
// here rather than a zeroed Usage that would read as "the run spent nothing".
func (a *EventAccumulator) Usage() *Usage {
	if !a.sawUsage {
		return nil
	}

	usage := a.usage

	return &usage
}

// Err joins the messages of every Error event seen. It reports what the run
// announced on its own stream, which is separate from the error Recv returns
// when the stream itself fails — a caller that wants both must check each.
func (a *EventAccumulator) Err() error { return errors.Join(a.errs...) }

// Response rebuilds the RunResponse a non-streaming Run would have returned,
// with the run's answer as a single assistant message under proto.
//
// The answer is AgentEnd's terminal message whenever an AgentEnd event
// arrived, including when that message is empty — a run that finished and
// reported no text did not produce text, and the deltas that preceded a
// guard or middleware rewrite are not it. Only a stream that never reached
// AgentEnd falls back to the accumulated deltas, as a best effort at what an
// interrupted run had produced so far.
//
// It is a presentation-level reconstruction, not a byte-exact replay: the
// event stream does not carry provider-native message payloads, per-message
// tool_call structure, or the interrupt descriptor, so Messages holds at most
// one assistant text message and Interrupt is always nil. Read
// ToolCalls/ToolResults for the tool activity.
func (a *EventAccumulator) Response(proto Protocol) *RunResponse {
	resp := &RunResponse{
		SessionID:  a.sessionID,
		Usage:      a.Usage(),
		Duration:   a.duration,
		StopReason: a.stopReason,
	}

	text := a.text.String()
	if a.sawAgentEnd {
		text = a.finalMessage
	}

	if text != "" {
		resp.Messages = []Message{NewTextMessage(proto, RoleAssistant, text)}
	}

	return resp
}
