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

// InterruptDescriptor is the wire summary of a persisted interrupt returned
// on RunResponse.Interrupt when StopReason == StopReasonInterrupted. It
// deliberately exposes only what a caller needs to persist an interrupt_id
// and present the still-pending tool calls to a human — the full resumable
// state (message continuation, decided-vs-pending set, effective run
// parameters) lives server-side in an interrupt.Store record and is never
// echoed on the wire.
type InterruptDescriptor struct {
	// InterruptID addresses the persisted record for ResumeInterruptRequest.
	InterruptID string `json:"interrupt_id"`

	// Pending lists the tool calls still awaiting an external decision, in
	// the original assistant ToolCalls order.
	Pending []ToolCall `json:"pending"`
}

// InterruptDecision is one external decision for a pending tool call. It
// substitutes for that call's tool result and, like every other tool
// result, is not permitted to change the tool name, arguments, agent or
// session identity — those stay bound to the persisted record.
type InterruptDecision struct {
	ToolCallID string `json:"tool_call_id"`
	Content    string `json:"content"`
	IsError    bool   `json:"is_error,omitempty"`
}

// ResumeInterruptRequest is the input to TaskAgent.ResumeInterrupt. It
// addresses a persisted interrupt by exact ID — never "the latest one for
// this session" — and carries zero or more decisions. Decisions commit in
// slice order; if one is rejected, the valid prefix remains committed.
//
// Decisions may be omitted, which submits nothing and resumes on what is
// already committed. As long as some flagged call is still undecided that is
// a pure status probe — it returns the remaining pending set without starting
// any tool or model call — and once every one has a decision it retries the
// resume, so an attempt that failed after the human already decided can be
// picked up without re-asking them.
type ResumeInterruptRequest struct {
	InterruptID string              `json:"interrupt_id"`
	Decisions   []InterruptDecision `json:"decisions,omitempty"`
}

// IsInterrupted reports whether this response suspended for an external
// decision instead of finishing, and therefore is not a consumable result.
//
// Either signal alone is enough to reject: a well-formed TaskAgent sets both
// StopReason and Interrupt, so an Agent or middleware that produces only one
// of them must not slip past a caller's guard by omitting the other.
//
// Parent layers (agent-as-tool, RouterAgent, WorkflowAgent, orchestrate
// runners) call this before reading a sub-Agent response: nested
// human-in-the-loop has no resume path, so a suspended response has to become
// a visible error rather than a half-written answer. A nil receiver is not
// interrupted.
func (r *RunResponse) IsInterrupted() bool {
	if r == nil {
		return false
	}

	return r.StopReason == StopReasonInterrupted || r.Interrupt != nil
}
