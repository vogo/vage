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
	"time"

	"github.com/vogo/aimodel"
)

// StopReason indicates why an agent run terminated.
type StopReason string

// StopReason constants.
const (
	StopReasonComplete        StopReason = "complete"
	StopReasonBudgetExhausted StopReason = "token_budget_exhausted"
	StopReasonMaxIterations   StopReason = "max_iterations_exceeded"
)

// Message wraps aimodel.Message with agent-specific metadata.
type Message struct {
	aimodel.Message
	AgentID   string         `json:"agent_id,omitempty"`
	Timestamp time.Time      `json:"timestamp"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// NewUserMessage creates a user message with the given text.
func NewUserMessage(text string) Message {
	return Message{
		Message:   aimodel.Message{Role: aimodel.RoleUser, Content: aimodel.NewTextContent(text)},
		Timestamp: time.Now(),
	}
}

// NewAssistantMessage wraps an aimodel.Message as an agent assistant message.
func NewAssistantMessage(msg aimodel.Message, agentID string) Message {
	return Message{
		Message:   msg,
		AgentID:   agentID,
		Timestamp: time.Now(),
	}
}

// ToAIModelMessages converts a slice of Message to []aimodel.Message.
func ToAIModelMessages(msgs []Message) []aimodel.Message {
	out := make([]aimodel.Message, len(msgs))
	for i, m := range msgs {
		out[i] = m.Message
	}
	return out
}

// FromAIModelMessage converts an aimodel.Message to a Message.
func FromAIModelMessage(msg aimodel.Message) Message {
	return Message{
		Message:   msg,
		Timestamp: time.Now(),
	}
}

// ContentPart represents a piece of content in a tool result.
type ContentPart struct {
	Type     string `json:"type"` // text, json, image, file
	Text     string `json:"text,omitempty"`
	Data     []byte `json:"data,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
	URL      string `json:"url,omitempty"`
}

// ToolResult represents the result of a tool execution.
type ToolResult struct {
	ToolCallID string        `json:"tool_call_id"`
	Content    []ContentPart `json:"content"`
	IsError    bool          `json:"is_error,omitempty"`
}

// TextResult creates a successful text tool result.
func TextResult(toolCallID, text string) ToolResult {
	return ToolResult{
		ToolCallID: toolCallID,
		Content:    []ContentPart{{Type: "text", Text: text}},
	}
}

// ErrorResult creates an error tool result.
func ErrorResult(toolCallID, errMsg string) ToolResult {
	return ToolResult{
		ToolCallID: toolCallID,
		Content:    []ContentPart{{Type: "text", Text: errMsg}},
		IsError:    true,
	}
}

// RunOptions holds optional overrides for a single Run call.
type RunOptions struct {
	Model          string   `json:"model,omitempty"`
	Temperature    *float64 `json:"temperature,omitempty"`
	MaxIterations  int      `json:"max_iterations,omitempty"`
	MaxTokens      int      `json:"max_tokens,omitempty"`
	RunTokenBudget int      `json:"run_token_budget,omitempty"`
	Tools          []string `json:"tools,omitempty"`
	StopSequences  []string `json:"stop_sequences,omitempty"`
}

// RunRequest is the input to Agent.Run.
type RunRequest struct {
	Messages  []Message      `json:"messages"`
	SessionID string         `json:"session_id,omitempty"`
	Options   *RunOptions    `json:"options,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// RunResponse is the output of Agent.Run.
type RunResponse struct {
	Messages   []Message      `json:"messages"`
	SessionID  string         `json:"session_id,omitempty"`
	Usage      *aimodel.Usage `json:"usage,omitempty"`
	Duration   int64          `json:"duration_ms,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
	StopReason StopReason     `json:"stop_reason,omitempty"`
}
