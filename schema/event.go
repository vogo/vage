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
	Duration int64  `json:"duration_ms"`
	Message  string `json:"message,omitempty"`
}

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
