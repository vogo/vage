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
	"strings"

	"github.com/vogo/aimodel/anthropic"
)

// Anthropic content block types vage reads and writes. Anthropic carries tool
// calls, tool results and thinking as content blocks rather than as dedicated
// message fields, so the accessors below decode them out of the block array.
const (
	anthropicBlockText       = "text"
	anthropicBlockThinking   = "thinking"
	anthropicBlockToolUse    = "tool_use"
	anthropicBlockToolResult = "tool_result"
)

// Anthropic message roles. The Messages API has no "system" or "tool" role:
// system text is hoisted to the request-level system field and tool results
// travel as user messages carrying tool_result blocks.
const (
	anthropicRoleUser      = "user"
	anthropicRoleAssistant = "assistant"
)

// ToolCall is a model-requested tool invocation in vage's own vocabulary. The
// vendors disagree on shape — OpenAI nests name and a JSON-string argument
// list under a function object, Anthropic emits a tool_use block with the
// arguments already decoded — so vage reads both into this one struct rather
// than exposing either wire form to callers.
type ToolCall struct {
	// ID correlates the call with the tool result that answers it
	// (OpenAI tool_call.id, Anthropic tool_use.id).
	ID string `json:"id"`

	// Name is the tool to invoke.
	Name string `json:"name"`

	// Arguments is the raw JSON argument object. OpenAI delivers it as a
	// JSON string, Anthropic as a JSON value; both land here as the encoded
	// object so tool dispatch can unmarshal it uniformly.
	Arguments string `json:"arguments"`
}

// anthropicBlock is the subset of an Anthropic content block vage reads. It
// mirrors the wire shape closely enough to decode both request-side blocks
// (which this package writes) and response-side blocks (which the model
// produces), while keeping unknown fields out of the way.
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

// decodeAnthropicBlocks reads an Anthropic message body into blocks. The wire
// format is polymorphic: a bare JSON string is shorthand for a single text
// block, anything else is a block array. A body that parses as neither yields
// no blocks rather than an error, so a malformed or future payload degrades to
// "no readable content" instead of breaking the whole conversation.
func decodeAnthropicBlocks(raw json.RawMessage) []anthropicBlock {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil
	}

	if trimmed[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return nil
		}

		return []anthropicBlock{{Type: anthropicBlockText, Text: text}}
	}

	var blocks []anthropicBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil
	}

	return blocks
}

// encodeAnthropicBlocks marshals blocks into an Anthropic message body.
func encodeAnthropicBlocks(blocks []anthropicBlock) json.RawMessage {
	raw, err := json.Marshal(blocks)
	if err != nil {
		// The block set is built from vage-controlled values, so a failure
		// here means a programming error rather than bad input; fall back to
		// an empty array so the message stays well-formed on the wire.
		return json.RawMessage("[]")
	}

	return raw
}

// newAnthropicMessage builds an Anthropic wire message from blocks.
func newAnthropicMessage(role string, blocks ...anthropicBlock) *anthropic.MessagesMessage {
	return &anthropic.MessagesMessage{
		Role:    role,
		Content: encodeAnthropicBlocks(blocks),
	}
}

// MergeAnthropicToolResults collapses each run of consecutive user messages
// that carry nothing but tool_result blocks into a single message, keeping the
// blocks in their original order.
//
// vage records one message per tool result, so a guard or an editor can
// address each result on its own. The Messages API, however, requires every
// tool_result answering one assistant turn to travel in the single user
// message that immediately follows it — a second tool_result message would be
// rejected because the message before it holds no tool_use blocks. Parallel
// tool calls therefore have to be joined on the way out, which is what this
// does.
//
// Messages that mix a tool_result with anything else are left alone: they are
// not something vage builds, and merging them could reorder content the model
// produced.
func MergeAnthropicToolResults(msgs []anthropic.MessagesMessage) []anthropic.MessagesMessage {
	merged := make([]anthropic.MessagesMessage, 0, len(msgs))
	inRun := false

	for _, msg := range msgs {
		if !anthropicToolResultOnly(msg) {
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
		blocks := append(decodeAnthropicBlocks(last.Content), decodeAnthropicBlocks(msg.Content)...)
		last.Content = encodeAnthropicBlocks(blocks)
	}

	return merged
}

// anthropicToolResultOnly reports whether msg is a user message whose content
// is one or more tool_result blocks and nothing else.
func anthropicToolResultOnly(msg anthropic.MessagesMessage) bool {
	if msg.Role != anthropicRoleUser {
		return false
	}

	blocks := decodeAnthropicBlocks(msg.Content)
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

// anthropicToolResultText renders the content of a tool_result block as text.
// The field is polymorphic — a bare string for simple results, an array of
// blocks for structured ones — so both forms collapse to their text.
func anthropicToolResultText(raw json.RawMessage) string {
	blocks := decodeAnthropicBlocks(raw)

	var b strings.Builder

	for _, block := range blocks {
		if block.Type == anthropicBlockText {
			b.WriteString(block.Text)
		}
	}

	return b.String()
}
