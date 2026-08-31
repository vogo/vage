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
)

// StopReason indicates why an agent run terminated.
type StopReason string

// StopReason constants.
const (
	StopReasonComplete        StopReason = "complete"
	StopReasonBudgetExhausted StopReason = "token_budget_exhausted"
	StopReasonMaxIterations   StopReason = "max_iterations_exceeded"

	// StopReasonInterrupted marks a Run that suspended before executing a
	// tool batch a policy flagged for external decision. It means "this call
	// ended, the logical Run has not terminated" — distinct from the other
	// three, which are true terminators. See vage/interrupt and
	// TaskAgent.ResumeInterrupt.
	StopReasonInterrupted StopReason = "interrupted"
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

// MessagePartType names a canonical content part kind.
type MessagePartType string

// Message part kind constants.
const (
	MessagePartText       MessagePartType = "text"
	MessagePartThinking   MessagePartType = "thinking"
	MessagePartToolCall   MessagePartType = "tool_call"
	MessagePartToolResult MessagePartType = "tool_result"
	// MessagePartImage carries image input. It is a canonical, provider-neutral
	// content kind — vendor wire shapes (image_url, base64 source, …) are the
	// codec's concern, not this package's.
	MessagePartImage MessagePartType = "image"
	// MessagePartFile carries document/file input. Same neutrality rule as
	// MessagePartImage.
	MessagePartFile MessagePartType = "file"
)

// MessagePart is a provider-neutral piece of message content.
type MessagePart struct {
	Type MessagePartType `json:"type"`

	Text string `json:"text,omitempty"`

	// Thinking carries reasoning text when the provider surfaces it.
	Thinking string `json:"thinking,omitempty"`

	// ToolCall carries a requested tool invocation.
	ToolCall *ToolCall `json:"tool_call,omitempty"`

	// ToolResult fields correlate and carry a tool result payload.
	ToolCallID string `json:"tool_call_id,omitempty"`
	IsError    bool   `json:"is_error,omitempty"`

	// URL is a remote source for an image or file part. Exactly one of URL,
	// Data or (file-only) FileID must be set; vage never fetches it.
	URL string `json:"url,omitempty"`
	// Data is the raw bytes of an inline image or file part. A codec, not the
	// caller, assembles base64 or a data URI from it. Required MimeType
	// accompanies it.
	Data []byte `json:"data,omitempty"`
	// MimeType names Data's media type. Required whenever Data is set and
	// rejected on a URL or FileID source, which carry no wire field for it;
	// for an image part it must be an "image/*" type.
	MimeType string `json:"mime_type,omitempty"`
	// FileID references a provider-hosted file (file part only, e.g. an
	// OpenAI Files API upload). vage does not manage file lifecycle.
	FileID string `json:"file_id,omitempty"`
	// Filename names an inline file part. OpenAI requires it alongside Data;
	// Anthropic has no wire field for it and drops it (documented
	// degradation). It is rejected on a URL or FileID source, where no
	// provider has a field to carry it.
	Filename string `json:"filename,omitempty"`
}

// Message is one provider-neutral conversation message.
//
// Its private role and parts are the canonical source of truth. An optional
// provider-native origin is retained only while the canonical state remains
// unmodified, allowing a same-protocol caller to replay it losslessly.
type Message struct {
	protocol Protocol
	role     Role
	parts    []MessagePart
	// origin is a replay cache for an unmodified provider response. Every
	// canonical mutation clears it, keeping role/parts as the single source
	// of truth while retaining lossless same-protocol replay when possible.
	origin json.RawMessage

	AgentID   string         `json:"agent_id,omitempty"`
	Timestamp time.Time      `json:"timestamp"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

type messageJSON struct {
	Protocol  Protocol        `json:"protocol"`
	Role      Role            `json:"role,omitempty"`
	Parts     []MessagePart   `json:"parts,omitempty"`
	Origin    json.RawMessage `json:"origin,omitempty"`
	AgentID   string          `json:"agent_id,omitempty"`
	Timestamp time.Time       `json:"timestamp"`
	Metadata  map[string]any  `json:"metadata,omitempty"`
}

// NewMessage creates a canonical message without a provider replay payload.
func NewMessage(proto Protocol, role Role, parts []MessagePart) Message {
	return Message{
		protocol:  proto,
		role:      role,
		parts:     cloneMessageParts(parts),
		Timestamp: time.Now(),
	}
}

// NewMessageWithOrigin creates a canonical message backed by an unmodified
// provider-native payload. Provider codecs are responsible for deriving role
// and parts from origin before calling this constructor.
func NewMessageWithOrigin(
	proto Protocol,
	role Role,
	parts []MessagePart,
	origin json.RawMessage,
	agentID string,
) Message {
	return Message{
		protocol:  proto,
		role:      role,
		parts:     cloneMessageParts(parts),
		origin:    cloneRawMessage(origin),
		AgentID:   agentID,
		Timestamp: time.Now(),
	}
}

// NewUserMessage creates a user message for the given protocol.
func NewUserMessage(proto Protocol, text string) Message {
	return NewMessage(proto, RoleUser, []MessagePart{{Type: MessagePartText, Text: text}})
}

// NewUserMessageWithParts creates a user message from mixed canonical parts
// — text, image and file, in the caller's order — for the given protocol.
// It is NewUserMessage's general form for multimodal input; image and file
// parts are only valid on RoleUser, which this constructor fixes. It does
// not validate part contents (a missing or duplicate media source, a bad
// MIME type, …); call Validate before handing the message to a provider
// codec.
func NewUserMessageWithParts(proto Protocol, parts []MessagePart) Message {
	return NewMessage(proto, RoleUser, parts)
}

// NewSystemMessage creates a system message for the given protocol. Under
// ProtocolAnthropicMessages the text is carried on the request-level system
// field rather than in the message list.
func NewSystemMessage(proto Protocol, text string) Message {
	return NewMessage(proto, RoleSystem, []MessagePart{{Type: MessagePartText, Text: text}})
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
		return NewMessage(proto, RoleAssistant, []MessagePart{{Type: MessagePartText, Text: text}})
	default:
		return NewUserMessage(proto, text)
	}
}

// NewToolResultMessage creates the message that answers a tool call. OpenAI
// expresses it as a dedicated tool-role message; Anthropic as a user message
// carrying a tool_result block.
func NewToolResultMessage(proto Protocol, toolCallID, text string, isError bool) Message {
	return NewMessage(proto, RoleTool, []MessagePart{{
		Type:       MessagePartToolResult,
		ToolCallID: toolCallID,
		Text:       text,
		IsError:    isError,
	}})
}

// NewAssistantTurn builds a complete assistant message from its parts. It is
// how a streamed turn — reassembled from deltas rather than received whole —
// re-enters the canonical conversation before a provider codec serializes the
// next iteration.
func NewAssistantTurn(proto Protocol, text, thinking string, calls []ToolCall) Message {
	var parts []MessagePart
	if thinking != "" {
		parts = append(parts, MessagePart{Type: MessagePartThinking, Thinking: thinking})
	}
	if text != "" {
		parts = append(parts, MessagePart{Type: MessagePartText, Text: text})
	}
	for i := range calls {
		call := calls[i]
		c := call
		parts = append(parts, MessagePart{Type: MessagePartToolCall, ToolCall: &c})
	}

	return NewMessage(proto, RoleAssistant, parts)
}

// MarshalJSON persists the canonical state and includes origin only while the
// message is still eligible for provider-native replay.
func (m Message) MarshalJSON() ([]byte, error) {
	return json.Marshal(messageJSON{
		Protocol:  m.protocol,
		Role:      m.role,
		Parts:     m.parts,
		Origin:    m.origin,
		AgentID:   m.AgentID,
		Timestamp: m.Timestamp,
		Metadata:  m.Metadata,
	})
}

// UnmarshalJSON restores the persisted canonical state. Provider payloads do
// not need decoding here because role and parts are always persisted beside
// origin.
func (m *Message) UnmarshalJSON(data []byte) error {
	var raw messageJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*m = Message{
		protocol:  raw.Protocol,
		role:      raw.Role,
		parts:     cloneMessageParts(raw.Parts),
		origin:    cloneRawMessage(raw.Origin),
		AgentID:   raw.AgentID,
		Timestamp: raw.Timestamp,
		Metadata:  raw.Metadata,
	}
	return nil
}

// Protocol reports which model protocol records the message.
func (m Message) Protocol() Protocol { return m.protocol }

// Role reports the provider-neutral conversation role.
func (m Message) Role() Role { return m.role }

// Parts returns a deep copy of the canonical content parts.
func (m Message) Parts() []MessagePart { return cloneMessageParts(m.parts) }

// Origin returns a copy of the provider-native replay payload. An empty result
// means the canonical message must be encoded by a provider codec.
func (m Message) Origin() json.RawMessage { return cloneRawMessage(m.origin) }

// Text returns the message's textual content, concatenating text parts when
// the content is structured. Non-text payloads (tool calls, thinking) are
// excluded; read them with ToolCalls and Thinking.
func (m Message) Text() string {
	var b strings.Builder
	for _, part := range m.parts {
		if part.Type == MessagePartText || part.Type == MessagePartToolResult {
			b.WriteString(part.Text)
		}
	}

	return b.String()
}

// Thinking returns the model's reasoning text, or empty when the message
// carries none.
func (m Message) Thinking() string {
	var b strings.Builder
	for _, part := range m.parts {
		if part.Type == MessagePartThinking {
			b.WriteString(part.Thinking)
		}
	}

	return b.String()
}

// ToolCalls returns the tool invocations the model requested, in wire order.
func (m Message) ToolCalls() []ToolCall {
	var calls []ToolCall
	for _, part := range m.parts {
		if part.Type == MessagePartToolCall && part.ToolCall != nil {
			calls = append(calls, *part.ToolCall)
		}
	}

	return calls
}

// ToolCallID returns the identifier of the tool call this message answers, or
// empty when the message is not a tool result.
func (m Message) ToolCallID() string {
	for _, part := range m.parts {
		if part.Type == MessagePartToolResult {
			return part.ToolCallID
		}
	}

	return ""
}

// SetText replaces the message's textual content, preserving its protocol and
// every canonical non-text payload it carries (tool calls, thinking and
// tool-result correlation). Any provider-native replay payload is invalidated.
func (m *Message) SetText(text string) {
	m.invalidateOrigin()
	m.parts = cloneMessageParts(m.parts)

	if len(m.parts) > 0 {
		replaced := false
		for i := range m.parts {
			switch m.parts[i].Type {
			case MessagePartText:
				m.parts[i].Text = text
				replaced = true
			case MessagePartToolResult:
				m.parts[i].Text = text
				replaced = true
			}
			if replaced {
				break
			}
		}
		if !replaced {
			m.parts = append(m.parts, MessagePart{Type: MessagePartText, Text: text})
		}
		return
	}

	m.parts = []MessagePart{{Type: MessagePartText, Text: text}}
	if m.role == "" {
		m.role = RoleUser
	}
}

// SetRole replaces the canonical role and invalidates native replay.
func (m *Message) SetRole(role Role) {
	m.invalidateOrigin()
	m.role = role
}

// ReplaceParts replaces the canonical content and invalidates native replay.
func (m *Message) ReplaceParts(parts []MessagePart) {
	m.invalidateOrigin()
	m.parts = cloneMessageParts(parts)
}

// AppendPart appends canonical content and invalidates native replay.
func (m *Message) AppendPart(part MessagePart) {
	m.invalidateOrigin()
	parts := cloneMessageParts(m.parts)
	m.parts = append(parts, cloneMessagePart(part))
}

func (m *Message) invalidateOrigin() { m.origin = nil }

// Validate checks the canonical message invariants shared by provider codecs.
// Part structure is checked against the messagePartRules table rather than
// per-kind branches here; see message_part_rule.go.
func (m Message) Validate() error {
	if err := m.protocol.Validate(); err != nil {
		return err
	}
	if roleBits[m.role] == 0 {
		return fmt.Errorf("vage: invalid message role %q", m.role)
	}
	for i, part := range m.parts {
		rule, ok := messagePartRules[part.Type]
		if !ok {
			return fmt.Errorf("vage: message part %d has unsupported type %q", i, part.Type)
		}
		if err := rule.validate(part.Type, m.role, part); err != nil {
			return fmt.Errorf("vage: message part %d %w", i, err)
		}
	}
	// A tool message is the answer to a call, so the correlation must be
	// present somewhere in it. This is a message-level rule, not a part-level
	// one, which is why the rule table cannot express it.
	if m.role == RoleTool {
		hasResult := false
		for _, part := range m.parts {
			if part.Type == MessagePartToolResult {
				hasResult = true
				break
			}
		}
		if !hasResult {
			return fmt.Errorf("vage: tool message requires a tool_result part")
		}
	}

	return nil
}

// ProtocolOf reports the protocol a message sequence is recorded under,
// reading it from the first message. It is how components that synthesize
// messages into an existing conversation (summarizers, compactors, elision
// placeholders) learn which wire form to build.
//
// An empty sequence yields ProtocolOpenAIChat, the default vage configures.
func ProtocolOf(msgs []Message) Protocol {
	for _, m := range msgs {
		if m.protocol != "" {
			return m.protocol
		}
	}

	return ProtocolOpenAIChat
}

// RequireProtocol reports an error when the message was not recorded under
// want. vage does not implicitly translate persisted conversations between
// provider protocols.
func (m Message) RequireProtocol(want Protocol) error {
	if m.protocol != want {
		return fmt.Errorf("%w: message is %q, caller is %q", ErrProtocolMismatch, m.protocol, want)
	}

	return nil
}

func cloneMessageParts(parts []MessagePart) []MessagePart {
	if parts == nil {
		return nil
	}
	out := make([]MessagePart, len(parts))
	for i, part := range parts {
		out[i] = cloneMessagePart(part)
	}
	return out
}

func cloneMessagePart(part MessagePart) MessagePart {
	out := part
	if part.ToolCall != nil {
		call := *part.ToolCall
		out.ToolCall = &call
	}
	if part.Data != nil {
		out.Data = append([]byte(nil), part.Data...)
	}
	return out
}

func cloneRawMessage(raw json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), raw...)
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

// Text returns the tool result's textual content: the first content part
// whose Type is "text", or the empty string when the result carries none.
//
// It reports only the first text part, even when the part is empty and later
// parts are not, so callers see exactly what the framework sends to the model.
// This deliberately differs from Message.Text, which concatenates every text
// part. IsError does not change the rule. Results carrying multiple text parts
// or non-text payloads (json, image, file) must be read from Content directly;
// Text does not claim to be a complete textual rendering of the result.
func (r ToolResult) Text() string {
	for _, part := range r.Content {
		if part.Type == "text" {
			return part.Text
		}
	}

	return ""
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

	// Interrupt is populated when StopReason == StopReasonInterrupted. It
	// carries just enough for the caller to persist an interrupt_id and
	// present the pending tool calls to a human decision-maker — the full
	// resumable state stays server-side in the interrupt.Store record.
	Interrupt *InterruptDescriptor `json:"interrupt,omitempty"`
}
