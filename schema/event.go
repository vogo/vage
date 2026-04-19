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

import "time"

// EventType constants for streaming events.
const (
	EventAgentStart     = "agent_start"
	EventTextDelta      = "text_delta"
	EventToolCallStart  = "tool_call_start"
	EventToolCallEnd    = "tool_call_end"
	EventToolResult     = "tool_result"
	EventIterationStart = "iteration_start"
	EventAgentEnd       = "agent_end"
	EventError          = "error"

	// LLM call events (emitted by largemodel metrics middleware).
	EventLLMCallStart = "llm_call_start"
	EventLLMCallEnd   = "llm_call_end"
	EventLLMCallError = "llm_call_error"

	EventTokenBudgetExhausted = "token_budget_exhausted"

	// Orchestration lifecycle events.
	EventPhaseStart    = "phase_start"
	EventPhaseEnd      = "phase_end"
	EventSubAgentStart = "sub_agent_start"
	EventSubAgentEnd   = "sub_agent_end"

	// Skill lifecycle events.
	EventSkillDiscover     = "skill_discover"
	EventSkillActivate     = "skill_activate"
	EventSkillDeactivate   = "skill_deactivate"
	EventSkillResourceLoad = "skill_resource_load"

	// Interaction events.
	EventPendingInteraction = "pending_interaction"

	// Guard events (emitted when a guard check produces a material outcome
	// such as log/rewrite/block/error; silent passes produce no event).
	EventGuardCheck = "guard_check"
)

// EventData is a sealed interface for event payloads.
// Only types within this package may implement it.
type EventData interface {
	eventData() // unexported marker prevents external implementations
}

// Event represents an agent lifecycle event emitted during streaming.
type Event struct {
	Type      string    `json:"type"`
	AgentID   string    `json:"agent_id,omitempty"`
	SessionID string    `json:"session_id,omitempty"`
	Timestamp time.Time `json:"timestamp"`
	Data      EventData `json:"data,omitempty"`
	ParentID  string    `json:"parent_id,omitempty"`
}

// AgentStartData carries information when the agent begins.
type AgentStartData struct{}

func (AgentStartData) eventData() {}

// TextDeltaData carries a text chunk from the LLM.
type TextDeltaData struct {
	Delta string `json:"delta"`
}

func (TextDeltaData) eventData() {}

// ToolCallStartData carries the start of a tool invocation.
type ToolCallStartData struct {
	ToolCallID string `json:"tool_call_id"`
	ToolName   string `json:"tool_name"`
	Arguments  string `json:"arguments"`
}

func (ToolCallStartData) eventData() {}

// ToolCallEndData carries the end of a tool invocation with duration.
type ToolCallEndData struct {
	ToolCallID string `json:"tool_call_id"`
	ToolName   string `json:"tool_name"`
	Duration   int64  `json:"duration_ms"`
}

func (ToolCallEndData) eventData() {}

// ToolResultData carries the result of a tool execution.
type ToolResultData struct {
	ToolCallID string     `json:"tool_call_id"`
	ToolName   string     `json:"tool_name"`
	Result     ToolResult `json:"result"`
}

func (ToolResultData) eventData() {}

// IterationStartData carries metadata about a new ReAct loop iteration.
type IterationStartData struct {
	Iteration int `json:"iteration"`
}

func (IterationStartData) eventData() {}

// AgentEndData carries summary information when the agent finishes.
type AgentEndData struct {
	Duration   int64      `json:"duration_ms"`
	Message    string     `json:"message,omitempty"`
	StopReason StopReason `json:"stop_reason,omitempty"`
}

// TokenBudgetExhaustedData carries information when the token budget is exhausted.
type TokenBudgetExhaustedData struct {
	Budget     int  `json:"budget"`
	Used       int  `json:"used"`
	Iterations int  `json:"iterations"`
	Estimated  bool `json:"estimated,omitempty"`
}

func (TokenBudgetExhaustedData) eventData() {}

func (AgentEndData) eventData() {}

// ErrorData carries error information.
type ErrorData struct {
	Message string `json:"message"`
}

func (ErrorData) eventData() {}

// LLMCallStartData carries information when an LLM call begins.
type LLMCallStartData struct {
	Model    string `json:"model"`
	Messages int    `json:"messages"`
	Tools    int    `json:"tools"`
	Stream   bool   `json:"stream"`
}

func (LLMCallStartData) eventData() {}

// LLMCallEndData carries information when an LLM call completes.
type LLMCallEndData struct {
	Model            string `json:"model"`
	Duration         int64  `json:"duration_ms"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	TotalTokens      int    `json:"total_tokens"`
	CacheReadTokens  int    `json:"cache_read_tokens,omitempty"`
	Stream           bool   `json:"stream"`
}

func (LLMCallEndData) eventData() {}

// LLMCallErrorData carries information when an LLM call fails.
type LLMCallErrorData struct {
	Model    string `json:"model"`
	Duration int64  `json:"duration_ms"`
	Error    string `json:"error"`
	Stream   bool   `json:"stream"`
}

func (LLMCallErrorData) eventData() {}

// PhaseStartData carries information when an orchestration phase begins.
type PhaseStartData struct {
	Phase      string `json:"phase"`       // e.g. "explore", "plan", "dispatch"
	PhaseIndex int    `json:"phase_index"` // 1-based index
	TotalPhase int    `json:"total_phase"` // total number of phases
}

func (PhaseStartData) eventData() {}

// PhaseEndData carries information when an orchestration phase completes.
type PhaseEndData struct {
	Phase            string `json:"phase"`
	Duration         int64  `json:"duration_ms"`
	Summary          string `json:"summary,omitempty"`           // optional phase summary (e.g., plan overview)
	ToolCalls        int    `json:"tool_calls,omitempty"`        // total tool calls in the phase
	PromptTokens     int    `json:"prompt_tokens,omitempty"`     // total prompt tokens in the phase
	CompletionTokens int    `json:"completion_tokens,omitempty"` // total completion tokens in the phase
	CacheReadTokens  int    `json:"cache_read_tokens,omitempty"` // total cache-read tokens in the phase
}

func (PhaseEndData) eventData() {}

// SubAgentStartData carries information when a sub-agent begins execution.
type SubAgentStartData struct {
	AgentName   string `json:"agent_name"`
	StepID      string `json:"step_id,omitempty"`     // for plan mode
	Description string `json:"description,omitempty"` // step description
	StepIndex   int    `json:"step_index,omitempty"`  // 1-based step index
	TotalSteps  int    `json:"total_steps,omitempty"` // total steps in plan
}

func (SubAgentStartData) eventData() {}

// SubAgentEndData carries information when a sub-agent finishes execution.
type SubAgentEndData struct {
	AgentName        string `json:"agent_name"`
	StepID           string `json:"step_id,omitempty"`
	Duration         int64  `json:"duration_ms"`
	ToolCalls        int    `json:"tool_calls"`
	TokensUsed       int    `json:"tokens_used"`                 // kept for backward compat (prompt + completion)
	PromptTokens     int    `json:"prompt_tokens,omitempty"`     // prompt tokens used by this sub-agent
	CompletionTokens int    `json:"completion_tokens,omitempty"` // completion tokens used by this sub-agent
	CacheReadTokens  int    `json:"cache_read_tokens,omitempty"` // cache-read tokens used by this sub-agent
}

func (SubAgentEndData) eventData() {}

// SkillDiscoverData carries information about skill discovery.
type SkillDiscoverData struct {
	Directory string `json:"directory"`
	Count     int    `json:"count"`
}

func (SkillDiscoverData) eventData() {}

// SkillActivateData carries information when a skill is activated.
type SkillActivateData struct {
	SkillName string `json:"skill_name"`
	SessionID string `json:"session_id"`
}

func (SkillActivateData) eventData() {}

// SkillDeactivateData carries information when a skill is deactivated.
type SkillDeactivateData struct {
	SkillName string `json:"skill_name"`
	SessionID string `json:"session_id"`
}

func (SkillDeactivateData) eventData() {}

// SkillResourceLoadData carries information when a skill resource is loaded.
type SkillResourceLoadData struct {
	SkillName    string `json:"skill_name"`
	ResourceType string `json:"resource_type"`
	ResourceName string `json:"resource_name"`
}

func (SkillResourceLoadData) eventData() {}

// GuardCheckData carries the outcome of a guard check with a material effect
// (log / rewrite / block / error). Silent passes do not emit this event.
type GuardCheckData struct {
	GuardName  string   `json:"guard_name"`
	ToolCallID string   `json:"tool_call_id,omitempty"`
	ToolName   string   `json:"tool_name,omitempty"`
	Action     string   `json:"action"`              // "log" | "rewrite" | "block" | "error"
	RuleHits   []string `json:"rule_hits,omitempty"` // matched rule names
	Severity   string   `json:"severity,omitempty"`  // max severity among hits
	Snippet    string   `json:"snippet,omitempty"`   // leading chars of scanned content
}

func (GuardCheckData) eventData() {}

// PendingInteractionData carries information about a pending user interaction.
type PendingInteractionData struct {
	InteractionID  string `json:"interaction_id"`
	Question       string `json:"question"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

func (PendingInteractionData) eventData() {}

// NewEvent creates an Event with the given type, agent ID, session ID, and data.
func NewEvent(eventType, agentID, sessionID string, data EventData) Event {
	return Event{
		Type:      eventType,
		AgentID:   agentID,
		SessionID: sessionID,
		Timestamp: time.Now(),
		Data:      data,
	}
}
