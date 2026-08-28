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

package anthropics

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/vogo/aimodel/anthropic"
	"github.com/vogo/vage/schema"
)

type anthropicBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

// DecodeAnthropicMessage derives canonical semantics from an Anthropic
// Messages payload and retains the native payload for lossless replay.
func DecodeAnthropicMessage(payload json.RawMessage, agentID string) (schema.Message, error) {
	var wire anthropic.MessagesMessage
	if err := json.Unmarshal(payload, &wire); err != nil {
		return schema.Message{}, fmt.Errorf("decode anthropic message: %w", err)
	}

	var role schema.Role
	switch wire.Role {
	case "assistant":
		role = schema.RoleAssistant
	case "user":
		role = schema.RoleUser
	default:
		return schema.Message{}, fmt.Errorf("decode anthropic message: unsupported role %q", wire.Role)
	}

	blocks, err := decodeAnthropicBlocks(wire.Content)
	if err != nil {
		return schema.Message{}, fmt.Errorf("decode anthropic message content: %w", err)
	}
	parts := make([]schema.MessagePart, 0, len(blocks))
	allToolResults := len(blocks) > 0
	for _, block := range blocks {
		switch block.Type {
		case anthropic.ContentBlockTypeText:
			parts = append(parts, schema.MessagePart{Type: schema.MessagePartText, Text: block.Text})
			allToolResults = false
		case anthropic.ContentBlockTypeThinking:
			parts = append(parts, schema.MessagePart{
				Type:     schema.MessagePartThinking,
				Thinking: block.Thinking,
			})
			allToolResults = false
		case anthropic.ContentBlockTypeToolUse:
			args := string(block.Input)
			if args == "" {
				args = "{}"
			}
			parts = append(parts, schema.MessagePart{
				Type: schema.MessagePartToolCall,
				ToolCall: &schema.ToolCall{
					ID:        block.ID,
					Name:      block.Name,
					Arguments: args,
				},
			})
			allToolResults = false
		case anthropic.ContentBlockTypeToolResult:
			text, textErr := anthropicToolResultText(block.Content)
			if textErr != nil {
				return schema.Message{}, textErr
			}
			parts = append(parts, schema.MessagePart{
				Type:       schema.MessagePartToolResult,
				ToolCallID: block.ToolUseID,
				Text:       text,
				IsError:    block.IsError,
			})
		default:
			allToolResults = false
		}
	}
	if allToolResults {
		role = schema.RoleTool
	}

	msg := schema.NewMessageWithOrigin(
		schema.ProtocolAnthropicMessages, role, parts, payload, agentID,
	)
	if err := msg.Validate(); err != nil {
		return schema.Message{}, fmt.Errorf("decode anthropic message canonical form: %w", err)
	}
	return msg, nil
}

// EncodeAnthropicMessage reuses an unmodified native payload when available;
// otherwise it validates and encodes the canonical message.
func EncodeAnthropicMessage(msg schema.Message) (anthropic.MessagesMessage, error) {
	if err := msg.RequireProtocol(schema.ProtocolAnthropicMessages); err != nil {
		return anthropic.MessagesMessage{}, err
	}
	if err := msg.Validate(); err != nil {
		return anthropic.MessagesMessage{}, err
	}
	if origin := msg.Origin(); len(origin) > 0 {
		var native anthropic.MessagesMessage
		if err := json.Unmarshal(origin, &native); err != nil {
			return native, fmt.Errorf("decode anthropic replay payload: %w", err)
		}
		return native, nil
	}
	out := anthropic.MessagesMessage{}
	switch msg.Role() {
	case schema.RoleAssistant:
		out.Role = "assistant"
	case schema.RoleUser, schema.RoleTool:
		out.Role = "user"
	case schema.RoleSystem:
		return out, fmt.Errorf("anthropic: system message must be hoisted before conversion")
	}

	blocks := make([]anthropic.ContentBlock, 0, len(msg.Parts()))
	for i, part := range msg.Parts() {
		switch part.Type {
		case schema.MessagePartThinking:
			if msg.Role() != schema.RoleAssistant {
				return out, invalidAnthropicPart(i, part, msg.Role())
			}
			blocks = append(blocks, anthropic.ContentBlock{
				Type:     anthropic.ContentBlockTypeThinking,
				Thinking: part.Thinking,
			})
		case schema.MessagePartText:
			if msg.Role() == schema.RoleTool {
				return out, invalidAnthropicPart(i, part, msg.Role())
			}
			blocks = append(blocks, anthropic.ContentBlock{
				Type: anthropic.ContentBlockTypeText,
				Text: part.Text,
			})
		case schema.MessagePartToolCall:
			if msg.Role() != schema.RoleAssistant {
				return out, invalidAnthropicPart(i, part, msg.Role())
			}
			args := part.ToolCall.Arguments
			if args == "" {
				args = "{}"
			}
			blocks = append(blocks, anthropic.ContentBlock{
				Type:  anthropic.ContentBlockTypeToolUse,
				ID:    part.ToolCall.ID,
				Name:  part.ToolCall.Name,
				Input: json.RawMessage(args),
			})
		case schema.MessagePartToolResult:
			if msg.Role() != schema.RoleTool && msg.Role() != schema.RoleUser {
				return out, invalidAnthropicPart(i, part, msg.Role())
			}
			blocks = append(blocks, anthropic.ContentBlock{
				Type:          anthropic.ContentBlockTypeToolResult,
				ToolUseID:     part.ToolCallID,
				ResultContent: part.Text,
				IsError:       part.IsError,
			})
		case schema.MessagePartImage:
			if msg.Role() != schema.RoleUser {
				return out, invalidAnthropicPart(i, part, msg.Role())
			}
			source := anthropic.ContentSource{}
			switch {
			case part.URL != "":
				source.Type = anthropic.ContentSourceTypeURL
				source.URL = part.URL
			case len(part.Data) > 0:
				source.Type = anthropic.ContentSourceTypeBase64
				source.MediaType = part.MimeType
				source.Data = base64.StdEncoding.EncodeToString(part.Data)
			}
			blocks = append(blocks, anthropic.ContentBlock{
				Type:   anthropic.ContentBlockTypeImage,
				Source: &source,
			})
		case schema.MessagePartFile:
			if msg.Role() != schema.RoleUser {
				return out, invalidAnthropicPart(i, part, msg.Role())
			}
			switch {
			case part.FileID != "":
				return out, fmt.Errorf("anthropic: message part %d file_id input is not supported", i)
			case part.URL != "":
				blocks = append(blocks, anthropic.ContentBlock{
					Type:   anthropic.ContentBlockTypeDocument,
					Source: &anthropic.ContentSource{Type: anthropic.ContentSourceTypeURL, URL: part.URL},
				})
			case len(part.Data) > 0:
				// Document blocks have no wire field for a filename; Anthropic
				// drops it. This is the one documented degradation (see
				// doc/domains/capability/model) — the file's bytes and MIME type
				// still reach the model intact.
				blocks = append(blocks, anthropic.ContentBlock{
					Type: anthropic.ContentBlockTypeDocument,
					Source: &anthropic.ContentSource{
						Type:      anthropic.ContentSourceTypeBase64,
						MediaType: part.MimeType,
						Data:      base64.StdEncoding.EncodeToString(part.Data),
					},
				})
			}
		}
	}
	if len(blocks) == 0 {
		blocks = append(blocks, anthropic.ContentBlock{
			Type: anthropic.ContentBlockTypeText,
			Text: msg.Text(),
		})
	}

	raw, err := json.Marshal(blocks)
	if err != nil {
		return out, fmt.Errorf("encode anthropic message content: %w", err)
	}
	out.Content = raw
	return out, nil
}

func invalidAnthropicPart(index int, part schema.MessagePart, role schema.Role) error {
	return fmt.Errorf("anthropic: message part %d type %q is invalid for role %q", index, part.Type, role)
}

// MergeAnthropicToolResults combines consecutive tool-result-only messages
// into the single user turn required by the Anthropic Messages API.
func MergeAnthropicToolResults(msgs []anthropic.MessagesMessage) ([]anthropic.MessagesMessage, error) {
	merged := make([]anthropic.MessagesMessage, 0, len(msgs))
	inRun := false
	for _, msg := range msgs {
		resultOnly, err := anthropicToolResultOnly(msg)
		if err != nil {
			return nil, err
		}
		if !resultOnly {
			merged = append(merged, msg)
			inRun = false
			continue
		}
		if !inRun {
			merged = append(merged, msg)
			inRun = true
			continue
		}

		last := &merged[len(merged)-1]
		lastBlocks, err := decodeAnthropicBlocks(last.Content)
		if err != nil {
			return nil, err
		}
		blocks, err := decodeAnthropicBlocks(msg.Content)
		if err != nil {
			return nil, err
		}
		raw, err := json.Marshal(append(lastBlocks, blocks...))
		if err != nil {
			return nil, err
		}
		last.Content = raw
	}
	return merged, nil
}

func anthropicToolResultOnly(msg anthropic.MessagesMessage) (bool, error) {
	if msg.Role != "user" {
		return false, nil
	}
	blocks, err := decodeAnthropicBlocks(msg.Content)
	if err != nil || len(blocks) == 0 {
		return false, err
	}
	for _, block := range blocks {
		if block.Type != anthropic.ContentBlockTypeToolResult {
			return false, nil
		}
	}
	return true, nil
}

func decodeAnthropicBlocks(raw json.RawMessage) ([]anthropicBlock, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil, nil
	}
	if trimmed[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return nil, err
		}
		return []anthropicBlock{{Type: anthropic.ContentBlockTypeText, Text: text}}, nil
	}
	var blocks []anthropicBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, err
	}
	return blocks, nil
}

func anthropicToolResultText(raw json.RawMessage) (string, error) {
	blocks, err := decodeAnthropicBlocks(raw)
	if err != nil {
		return "", fmt.Errorf("decode anthropic tool result content: %w", err)
	}
	var b strings.Builder
	for _, block := range blocks {
		if block.Type == anthropic.ContentBlockTypeText {
			b.WriteString(block.Text)
		}
	}
	return b.String(), nil
}
