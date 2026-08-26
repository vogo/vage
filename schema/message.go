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
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/vogo/aimodel/anthropic"
	"github.com/vogo/aimodel/openai"
)

// StopReason indicates why an agent run terminated.
type StopReason string

// StopReason constants.
const (
	StopReasonComplete        StopReason = "complete"
	StopReasonBudgetExhausted StopReason = "token_budget_exhausted"
	StopReasonMaxIterations   StopReason = "max_iterations_exceeded"
)

// Role names a chat participant. vage keeps its own role vocabulary because
// the vendors disagree: OpenAI has four roles on the message itself, while
// Anthropic has only user and assistant and expresses the other two
// structurally (system text is hoisted to a request field, tool results are
// user messages carrying tool_result blocks).
type Role string

// Role constants.
const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message is one chat message held in its originating vendor's native wire
// form. vage stores exactly what the vendor produced — OpenAI messages as
// openai.ChatCompletionMessage, Anthropic messages as
// anthropic.MessagesMessage — and never converts between them, so a message
// round-trips through persistence without passing through a lossy neutral
// representation.
//
// Protocol says which of the wire fields is populated; exactly one ever is.
// Callers that only need to read a message should use the accessors (Role,
// Text, ToolCalls, …) rather than reaching into the wire field, so they work
// across both protocols.
//
// Because the stored form is vendor-specific, a message recorded under one
// protocol cannot be replayed under another — see ErrProtocolMismatch.
type Message struct {
	// Protocol identifies which wire form this message holds.
	Protocol Protocol `json:"protocol"`

	// OpenAI holds the message when Protocol is ProtocolOpenAIChat or
	// ProtocolOpenAIResponses; nil otherwise.
	OpenAI *openai.ChatCompletionMessage `json:"openai,omitempty"`

	// Anthropic holds the message when Protocol is
	// ProtocolAnthropicMessages; nil otherwise.
	Anthropic *anthropic.MessagesMessage `json:"anthropic,omitempty"`

	// SystemText carries the text of a system message under
	// ProtocolAnthropicMessages, where the Messages API has no system role
	// and the text belongs to the request-level system field instead. It is
	// unused for the OpenAI protocols, which keep system messages inline.
	SystemText string `json:"system_text,omitempty"`

	AgentID   string         `json:"agent_id,omitempty"`
	Timestamp time.Time      `json:"timestamp"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// NewUserMessage creates a user message for the given protocol.
func NewUserMessage(proto Protocol, text string) Message {
	m := Message{Protocol: proto, Timestamp: time.Now()}

	if proto == ProtocolAnthropicMessages {
		m.Anthropic = newAnthropicMessage(anthropicRoleUser,
			anthropicBlock{Type: anthropicBlockText, Text: text})

		return m
	}

	m.OpenAI = &openai.ChatCompletionMessage{
		Role:    string(RoleUser),
		Content: openai.NewTextContent(text),
	}

	return m
}

// NewSystemMessage creates a system message for the given protocol. Under
// ProtocolAnthropicMessages the text is held in SystemText, since the Messages
// API carries system text on the request rather than as a message.
func NewSystemMessage(proto Protocol, text string) Message {
	m := Message{Protocol: proto, Timestamp: time.Now()}

	if proto == ProtocolAnthropicMessages {
		m.SystemText = text

		return m
	}

	m.OpenAI = &openai.ChatCompletionMessage{
		Role:    string(RoleSystem),
		Content: openai.NewTextContent(text),
	}

	return m
}

// NewTextMessage creates a plain text message for the given protocol and
// role. It is the general form behind NewUserMessage and NewSystemMessage,
// for callers whose role is configurable rather than fixed.
//
// RoleTool is not expressible this way, because a tool result must carry the
// id of the call it answers — use NewToolResultMessage for that.
func NewTextMessage(proto Protocol, role Role, text string) Message {
	switch role {
	case RoleSystem:
		return NewSystemMessage(proto, text)
	case RoleAssistant:
		m := Message{Protocol: proto, Timestamp: time.Now()}

		if proto == ProtocolAnthropicMessages {
			m.Anthropic = newAnthropicMessage(anthropicRoleAssistant,
				anthropicBlock{Type: anthropicBlockText, Text: text})

			return m
		}

		m.OpenAI = &openai.ChatCompletionMessage{
			Role:    string(RoleAssistant),
			Content: openai.NewTextContent(text),
		}

		return m
	default:
		return NewUserMessage(proto, text)
	}
}

// NewToolResultMessage creates the message that answers a tool call. OpenAI
// expresses it as a dedicated tool-role message; Anthropic as a user message
// carrying a tool_result block.
func NewToolResultMessage(proto Protocol, toolCallID, text string, isError bool) Message {
	m := Message{Protocol: proto, Timestamp: time.Now()}

	if proto == ProtocolAnthropicMessages {
		m.Anthropic = newAnthropicMessage(anthropicRoleUser, anthropicBlock{
			Type:      anthropicBlockToolResult,
			ToolUseID: toolCallID,
			Content:   json.RawMessage(mustEncodeString(text)),
			IsError:   isError,
		})

		return m
	}

	m.OpenAI = &openai.ChatCompletionMessage{
		Role:       string(RoleTool),
		Content:    openai.NewTextContent(text),
		ToolCallID: toolCallID,
	}

	return m
}

// NewAssistantTurn builds a complete assistant message from its parts. It is
// how a streamed turn — reassembled from deltas rather than received whole —
// re-enters the conversation in the vendor's own wire form, so the next
// iteration replays it exactly like a non-streamed turn.
func NewAssistantTurn(proto Protocol, text, thinking string, calls []ToolCall) Message {
	m := Message{Protocol: proto, Timestamp: time.Now()}

	if proto == ProtocolAnthropicMessages {
		// Anthropic orders blocks thinking → text → tool_use, and requires
		// the thinking block to come first when extended thinking is on.
		var blocks []anthropicBlock

		if thinking != "" {
			blocks = append(blocks, anthropicBlock{Type: anthropicBlockThinking, Thinking: thinking})
		}

		if text != "" {
			blocks = append(blocks, anthropicBlock{Type: anthropicBlockText, Text: text})
		}

		for _, call := range calls {
			blocks = append(blocks, anthropicBlock{
				Type:  anthropicBlockToolUse,
				ID:    call.ID,
				Name:  call.Name,
				Input: json.RawMessage(call.Arguments),
			})
		}

		m.Anthropic = newAnthropicMessage(anthropicRoleAssistant, blocks...)

		return m
	}

	wire := &openai.ChatCompletionMessage{
		Role:             string(RoleAssistant),
		Content:          openai.NewTextContent(text),
		ReasoningContent: thinking,
	}

	for i, call := range calls {
		wire.ToolCalls = append(wire.ToolCalls, openai.ChatCompletionToolCall{
			Index: i,
			ID:    call.ID,
			Type:  openai.ToolTypeFunction,
			Function: openai.ChatCompletionFunctionCall{
				Name:      call.Name,
				Arguments: call.Arguments,
			},
		})
	}

	m.OpenAI = wire

	return m
}

// NewOpenAIMessage wraps a native OpenAI message produced by the model.
func NewOpenAIMessage(proto Protocol, msg openai.ChatCompletionMessage, agentID string) Message {
	return Message{
		Protocol:  proto,
		OpenAI:    &msg,
		AgentID:   agentID,
		Timestamp: time.Now(),
	}
}

// NewAnthropicMessage wraps a native Anthropic message produced by the model.
func NewAnthropicMessage(msg anthropic.MessagesMessage, agentID string) Message {
	return Message{
		Protocol:  ProtocolAnthropicMessages,
		Anthropic: &msg,
		AgentID:   agentID,
		Timestamp: time.Now(),
	}
}

// mustEncodeString marshals s as a JSON string. Encoding a string cannot fail,
// so a failure would be a programming error; the empty-string literal keeps
// the wire payload well-formed regardless.
func mustEncodeString(s string) string {
	raw, err := json.Marshal(s)
	if err != nil {
		return `""`
	}

	return string(raw)
}

// Role reports the message role in vage's vocabulary, normalizing across the
// two wire forms: an Anthropic user message carrying only tool_result blocks
// reads back as RoleTool, and a message held as SystemText reads as
// RoleSystem.
func (m Message) Role() Role {
	if m.Protocol == ProtocolAnthropicMessages {
		if m.Anthropic == nil {
			return RoleSystem
		}

		if m.isAnthropicToolResult() {
			return RoleTool
		}

		if m.Anthropic.Role == anthropicRoleAssistant {
			return RoleAssistant
		}

		return RoleUser
	}

	if m.OpenAI == nil {
		return ""
	}

	return Role(m.OpenAI.Role)
}

// isAnthropicToolResult reports whether the message consists of tool_result
// blocks, which is how the Messages API expresses a tool response.
func (m Message) isAnthropicToolResult() bool {
	blocks := decodeAnthropicBlocks(m.Anthropic.Content)
	if len(blocks) == 0 {
		return false
	}

	for _, block := range blocks {
		if block.Type != anthropicBlockToolResult {
			return false
		}
	}

	return true
}

// Text returns the message's textual content, concatenating text parts when
// the content is structured. Non-text payloads (tool calls, thinking) are
// excluded; read them with ToolCalls and Thinking.
func (m Message) Text() string {
	if m.Protocol == ProtocolAnthropicMessages {
		if m.SystemText != "" {
			return m.SystemText
		}

		if m.Anthropic == nil {
			return ""
		}

		var b strings.Builder

		for _, block := range decodeAnthropicBlocks(m.Anthropic.Content) {
			switch block.Type {
			case anthropicBlockText:
				b.WriteString(block.Text)
			case anthropicBlockToolResult:
				b.WriteString(anthropicToolResultText(block.Content))
			}
		}

		return b.String()
	}

	if m.OpenAI == nil {
		return ""
	}

	return m.OpenAI.Content.Text()
}

// Thinking returns the model's reasoning text, or empty when the message
// carries none.
func (m Message) Thinking() string {
	if m.Protocol == ProtocolAnthropicMessages {
		if m.Anthropic == nil {
			return ""
		}

		var b strings.Builder

		for _, block := range decodeAnthropicBlocks(m.Anthropic.Content) {
			if block.Type == anthropicBlockThinking {
				b.WriteString(block.Thinking)
			}
		}

		return b.String()
	}

	if m.OpenAI == nil {
		return ""
	}

	return m.OpenAI.ReasoningContent
}

// ToolCalls returns the tool invocations the model requested, in wire order.
func (m Message) ToolCalls() []ToolCall {
	if m.Protocol == ProtocolAnthropicMessages {
		if m.Anthropic == nil {
			return nil
		}

		var calls []ToolCall

		for _, block := range decodeAnthropicBlocks(m.Anthropic.Content) {
			if block.Type != anthropicBlockToolUse {
				continue
			}

			args := string(block.Input)
			if args == "" {
				args = "{}"
			}

			calls = append(calls, ToolCall{ID: block.ID, Name: block.Name, Arguments: args})
		}

		return calls
	}

	if m.OpenAI == nil {
		return nil
	}

	var calls []ToolCall

	for _, call := range m.OpenAI.ToolCalls {
		args := call.Function.Arguments
		if args == "" {
			args = "{}"
		}

		calls = append(calls, ToolCall{ID: call.ID, Name: call.Function.Name, Arguments: args})
	}

	return calls
}

// ToolCallID returns the identifier of the tool call this message answers, or
// empty when the message is not a tool result.
func (m Message) ToolCallID() string {
	if m.Protocol == ProtocolAnthropicMessages {
		if m.Anthropic == nil {
			return ""
		}

		for _, block := range decodeAnthropicBlocks(m.Anthropic.Content) {
			if block.Type == anthropicBlockToolResult {
				return block.ToolUseID
			}
		}

		return ""
	}

	if m.OpenAI == nil {
		return ""
	}

	return m.OpenAI.ToolCallID
}

// SetText replaces the message's textual content, preserving its protocol and
// every non-text payload it carries (tool calls, thinking, tool-result
// correlation). Guards use it to rewrite content in place.
func (m *Message) SetText(text string) {
	if m.Protocol == ProtocolAnthropicMessages {
		if m.SystemText != "" || m.Anthropic == nil {
			m.SystemText = text

			return
		}

		blocks := decodeAnthropicBlocks(m.Anthropic.Content)
		rewritten := make([]anthropicBlock, 0, len(blocks)+1)
		replaced := false

		// Only the first textual block is rewritten; every other block is
		// carried through untouched, so a multi-block message (merged tool
		// results, thinking, tool calls) keeps everything the rewrite does not
		// address.
		for _, block := range blocks {
			if !replaced {
				switch block.Type {
				case anthropicBlockText:
					block.Text = text
					replaced = true
				case anthropicBlockToolResult:
					block.Content = json.RawMessage(mustEncodeString(text))
					replaced = true
				}
			}

			rewritten = append(rewritten, block)
		}

		if !replaced {
			rewritten = append(rewritten, anthropicBlock{Type: anthropicBlockText, Text: text})
		}

		m.Anthropic.Content = encodeAnthropicBlocks(rewritten)

		return
	}

	if m.OpenAI == nil {
		m.OpenAI = &openai.ChatCompletionMessage{Role: string(RoleUser)}
	}

	m.OpenAI.Content = openai.NewTextContent(text)
}

// ProtocolOf reports the protocol a message sequence is recorded under,
// reading it from the first message. It is how components that synthesize
// messages into an existing conversation (summarizers, compactors, elision
// placeholders) learn which wire form to build.
//
// An empty sequence yields ProtocolOpenAIChat, the default vage configures.
func ProtocolOf(msgs []Message) Protocol {
	for _, m := range msgs {
		if m.Protocol != "" {
			return m.Protocol
		}
	}

	return ProtocolOpenAIChat
}

// RequireProtocol reports an error when the message was not recorded under
// want. vage stores native vendor wire forms, so replaying a message under a
// different protocol is a mismatch rather than a conversion.
func (m Message) RequireProtocol(want Protocol) error {
	if m.Protocol != want {
		return fmt.Errorf("%w: message is %q, caller is %q", ErrProtocolMismatch, m.Protocol, want)
	}

	return nil
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
	Usage      *Usage         `json:"usage,omitempty"`
	Duration   int64          `json:"duration_ms,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
	StopReason StopReason     `json:"stop_reason,omitempty"`
}
