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

package openais

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/vogo/aimodel/openai"
	"github.com/vogo/vage/schema"
)

// DecodeOpenAIMessage derives canonical semantics from an OpenAI Chat
// Completions message and retains the native payload for lossless replay.
func DecodeOpenAIMessage(payload json.RawMessage, agentID string) (schema.Message, error) {
	var wire openai.ChatCompletionMessage
	if err := json.Unmarshal(payload, &wire); err != nil {
		return schema.Message{}, fmt.Errorf("decode openai message: %w", err)
	}

	role := schema.Role(wire.Role)
	var parts []schema.MessagePart
	if text := wire.Content.Text(); text != "" && role != schema.RoleTool {
		parts = append(parts, schema.MessagePart{Type: schema.MessagePartText, Text: text})
	}
	if wire.ReasoningContent != "" {
		parts = append(parts, schema.MessagePart{
			Type:     schema.MessagePartThinking,
			Thinking: wire.ReasoningContent,
		})
	}
	for _, call := range wire.ToolCalls {
		args := call.Function.Arguments
		if args == "" {
			args = "{}"
		}
		parts = append(parts, schema.MessagePart{
			Type: schema.MessagePartToolCall,
			ToolCall: &schema.ToolCall{
				ID:        call.ID,
				Name:      call.Function.Name,
				Arguments: args,
			},
		})
	}
	if role == schema.RoleTool {
		parts = append(parts, schema.MessagePart{
			Type:       schema.MessagePartToolResult,
			ToolCallID: wire.ToolCallID,
			Text:       wire.Content.Text(),
		})
	}

	msg := schema.NewMessageWithOrigin(
		schema.ProtocolOpenAIChat, role, parts, payload, agentID,
	)
	if err := msg.Validate(); err != nil {
		return schema.Message{}, fmt.Errorf("decode openai message canonical form: %w", err)
	}
	return msg, nil
}

// EncodeOpenAIMessage reuses an unmodified native payload when available;
// otherwise it validates and encodes the canonical message.
func EncodeOpenAIMessage(msg schema.Message) (openai.ChatCompletionMessage, error) {
	if err := msg.RequireProtocol(schema.ProtocolOpenAIChat); err != nil {
		return openai.ChatCompletionMessage{}, err
	}
	if err := msg.Validate(); err != nil {
		return openai.ChatCompletionMessage{}, err
	}
	if origin := msg.Origin(); len(origin) > 0 {
		var native openai.ChatCompletionMessage
		if err := json.Unmarshal(origin, &native); err != nil {
			return native, fmt.Errorf("decode openai replay payload: %w", err)
		}
		return native, nil
	}
	if err := validateOpenAIParts(msg); err != nil {
		return openai.ChatCompletionMessage{}, err
	}

	out := openai.ChatCompletionMessage{Role: string(msg.Role())}
	if hasMediaParts(msg) {
		parts, err := encodeOpenAIContentParts(msg)
		if err != nil {
			return openai.ChatCompletionMessage{}, err
		}
		out.Content = openai.NewPartsContent(parts...)
	} else {
		out.Content = openai.NewTextContent(msg.Text())
	}
	if msg.Role() == schema.RoleAssistant {
		out.ReasoningContent = msg.Thinking()
		for i, call := range msg.ToolCalls() {
			args := call.Arguments
			if args == "" {
				args = "{}"
			}
			out.ToolCalls = append(out.ToolCalls, openai.ChatCompletionToolCall{
				Index: i,
				ID:    call.ID,
				Type:  openai.ToolTypeFunction,
				Function: openai.ChatCompletionFunctionCall{
					Name:      call.Name,
					Arguments: args,
				},
			})
		}
	}
	if msg.Role() == schema.RoleTool {
		out.ToolCallID = msg.ToolCallID()
	}
	return out, nil
}

func validateOpenAIParts(msg schema.Message) error {
	toolResults := 0
	for i, part := range msg.Parts() {
		allowed := false
		switch msg.Role() {
		case schema.RoleSystem:
			allowed = part.Type == schema.MessagePartText
		case schema.RoleUser:
			allowed = part.Type == schema.MessagePartText ||
				part.Type == schema.MessagePartImage ||
				part.Type == schema.MessagePartFile
		case schema.RoleAssistant:
			allowed = part.Type == schema.MessagePartText ||
				part.Type == schema.MessagePartThinking ||
				part.Type == schema.MessagePartToolCall
		case schema.RoleTool:
			allowed = part.Type == schema.MessagePartToolResult
			if allowed {
				toolResults++
			}
		}
		if !allowed {
			return fmt.Errorf("openai: message part %d type %q is invalid for role %q", i, part.Type, msg.Role())
		}
	}
	if msg.Role() == schema.RoleTool && toolResults != 1 {
		return fmt.Errorf("openai: tool message requires exactly one tool_result part")
	}
	return nil
}

// hasMediaParts reports whether msg carries any image or file part. Media-free
// messages keep encoding to the scalar Content form so unrelated requests do
// not change shape.
func hasMediaParts(msg schema.Message) bool {
	for _, part := range msg.Parts() {
		if part.Type == schema.MessagePartImage || part.Type == schema.MessagePartFile {
			return true
		}
	}
	return false
}

// encodeOpenAIContentParts renders canonical text/image/file parts into Chat
// Completions' structured content array, preserving canonical order. It fails
// closed on combinations Chat Completions cannot express (a file URL, or
// inline file data without a filename) rather than sending a request the
// backend would reject or silently truncate.
func encodeOpenAIContentParts(msg schema.Message) ([]openai.ChatCompletionContentPart, error) {
	parts := msg.Parts()
	out := make([]openai.ChatCompletionContentPart, 0, len(parts))
	for i, part := range parts {
		switch part.Type {
		case schema.MessagePartText:
			out = append(out, openai.ChatCompletionContentPart{
				Type: openai.ContentPartTypeText,
				Text: part.Text,
			})
		case schema.MessagePartImage:
			url := part.URL
			if len(part.Data) > 0 {
				url = dataURI(part.MimeType, part.Data)
			}
			out = append(out, openai.ChatCompletionContentPart{
				Type:     openai.ContentPartTypeImageURL,
				ImageURL: &openai.ImageURL{URL: url},
			})
		case schema.MessagePartFile:
			switch {
			case part.FileID != "":
				out = append(out, openai.ChatCompletionContentPart{
					Type: openai.ContentPartTypeFile,
					File: &openai.InputFile{FileID: part.FileID},
				})
			case len(part.Data) > 0:
				if part.Filename == "" {
					return nil, fmt.Errorf("openai: message part %d inline file requires filename", i)
				}
				out = append(out, openai.ChatCompletionContentPart{
					Type: openai.ContentPartTypeFile,
					File: &openai.InputFile{
						FileData: dataURI(part.MimeType, part.Data),
						Filename: part.Filename,
					},
				})
			default:
				return nil, fmt.Errorf("openai: message part %d file url input is not supported", i)
			}
		default:
			return nil, fmt.Errorf("openai: message part %d type %q cannot be encoded as content", i, part.Type)
		}
	}
	return out, nil
}

// dataURI assembles the data: URI OpenAI expects for inline image_url.url and
// file.file_data. Callers provide raw bytes; the codec owns base64 assembly.
func dataURI(mimeType string, data []byte) string {
	return fmt.Sprintf("data:%s;base64,%s", mimeType, base64.StdEncoding.EncodeToString(data))
}
