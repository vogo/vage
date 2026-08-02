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

package largemodel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/vogo/aimodel/provider/anthropic"
	"github.com/vogo/vage/schema"
)

// defaultAnthropicMaxTokens is sent when a request sets no cap. Anthropic
// requires max_tokens on every Messages call, so a default is mandatory
// rather than merely convenient.
const defaultAnthropicMaxTokens = 4096

// anthropicBlockToolUse is the content-block type carrying a tool call.
// Anthropic expresses tool calls as content blocks rather than as a dedicated
// message field.
const anthropicBlockToolUse = "tool_use"

// Anthropic stream event names.
const (
	anthropicEventContentBlockStart = "content_block_start"
	anthropicEventContentBlockDelta = "content_block_delta"
	anthropicEventMessageDelta      = "message_delta"
	anthropicEventMessageStart      = "message_start"
	anthropicEventError             = "error"
)

// Anthropic stop reasons that map onto vage's cross-vendor finish reasons.
// Values outside this set (refusal, pause_turn, …) pass through verbatim.
const (
	anthropicStopEndTurn   = "end_turn"
	anthropicStopMaxTokens = "max_tokens"
	anthropicStopToolUse   = "tool_use"
	anthropicStopSequence  = "stop_sequence"
)

// anthropicMessagesCaller calls Anthropic's Messages API through the native
// aimodel client. Anthropic differs structurally from OpenAI in three ways
// this type absorbs: system text belongs to a request field rather than a
// message, tool calls and results are content blocks rather than message
// fields, and max_tokens is mandatory.
type anthropicMessagesCaller struct {
	client *anthropic.Client
}

// NewAnthropicMessagesCaller builds a Caller over Anthropic's Messages API.
// baseURL may be empty to use Anthropic's own endpoint. The API key is
// required and validated here so misconfiguration surfaces before any call.
func NewAnthropicMessagesCaller(apiKey, baseURL string, opts ...anthropic.ClientOption) (Caller, error) {
	if apiKey == "" {
		return nil, ErrNoAPIKey
	}

	if baseURL != "" {
		opts = append([]anthropic.ClientOption{anthropic.WithBaseURL(baseURL)}, opts...)
	}

	return &anthropicMessagesCaller{client: anthropic.NewClient(apiKey, opts...)}, nil
}

// Protocol implements Caller.
func (c *anthropicMessagesCaller) Protocol() schema.Protocol {
	return schema.ProtocolAnthropicMessages
}

// Call implements Caller.
func (c *anthropicMessagesCaller) Call(ctx context.Context, req *Request) (*Response, error) {
	wire, err := c.buildRequest(req)
	if err != nil {
		return nil, err
	}

	resp, err := c.client.Messages(ctx, wire)
	if err != nil {
		return nil, normalizeAnthropicError(err)
	}

	if len(resp.Content) == 0 && resp.StopReason == "" {
		return nil, ErrEmptyResponse
	}

	// Re-encode the response blocks as a request-shaped assistant message so
	// the turn can be replayed verbatim on the next iteration.
	blocks := make([]json.RawMessage, 0, len(resp.Content))
	for _, block := range resp.Content {
		blocks = append(blocks, block.Raw)
	}

	content, err := json.Marshal(blocks)
	if err != nil {
		return nil, fmt.Errorf("vage: encode anthropic response content: %w", err)
	}

	return &Response{
		ID:    resp.ID,
		Model: resp.Model,
		Message: schema.NewAnthropicMessage(anthropic.MessagesMessage{
			Role:    resp.Role,
			Content: content,
		}, ""),
		FinishReason: anthropicFinishReason(resp.StopReason),
		Usage:        anthropicUsage(&resp.Usage),
	}, nil
}

// CallStream implements Caller.
func (c *anthropicMessagesCaller) CallStream(ctx context.Context, req *Request) (*Stream, error) {
	wire, err := c.buildRequest(req)
	if err != nil {
		return nil, err
	}

	stream, err := c.client.MessagesStream(ctx, wire)
	if err != nil {
		return nil, normalizeAnthropicError(err)
	}

	// Anthropic identifies a content block by its position in the message,
	// and streams the block's type only in content_block_start. Later deltas
	// carry the index alone, so the decoder remembers which indices opened as
	// tool_use blocks in order to route their partial JSON correctly.
	decoder := &anthropicStreamDecoder{
		stream:    stream,
		toolIndex: map[int]int{},
	}

	return NewStream(decoder.next, stream.Close), nil
}

// buildRequest translates a vage request into Anthropic's native request,
// hoisting system text to the request-level field and rejecting messages
// recorded under another protocol.
func (c *anthropicMessagesCaller) buildRequest(req *Request) (*anthropic.MessagesRequest, error) {
	maxTokens := defaultAnthropicMaxTokens
	if req.MaxTokens != nil && *req.MaxTokens > 0 {
		maxTokens = *req.MaxTokens
	}

	wire := &anthropic.MessagesRequest{
		Model:         req.Model,
		MaxTokens:     maxTokens,
		Temperature:   req.Temperature,
		StopSequences: req.Stop,
	}

	var systemParts []string

	for i := range req.Messages {
		msg := req.Messages[i]

		if err := msg.RequireProtocol(schema.ProtocolAnthropicMessages); err != nil {
			return nil, err
		}

		// System text has no message slot in the Messages API; collect it for
		// the request-level system field instead.
		if msg.Role() == schema.RoleSystem {
			if text := msg.Text(); text != "" {
				systemParts = append(systemParts, text)
			}

			continue
		}

		if msg.Anthropic == nil {
			return nil, fmt.Errorf("vage: anthropic message %d has no wire payload", i)
		}

		wire.Messages = append(wire.Messages, *msg.Anthropic)
	}

	if len(systemParts) > 0 {
		system, err := anthropicSystemField(strings.Join(systemParts, "\n\n"), req.PromptCaching)
		if err != nil {
			return nil, err
		}

		wire.System = system
	}

	if err := c.buildTools(req, wire); err != nil {
		return nil, err
	}

	return wire, nil
}

// buildTools maps vage tool definitions onto Anthropic tools, marking the last
// one as a cache breakpoint when prompt caching is on so the whole tool block
// is cached across ReAct iterations.
func (c *anthropicMessagesCaller) buildTools(req *Request, wire *anthropic.MessagesRequest) error {
	for _, def := range req.Tools {
		wire.Tools = append(wire.Tools, anthropic.MessagesTool{
			Name:        def.Name,
			Description: def.Description,
			InputSchema: def.Parameters,
		})

		if def.ForceUse && wire.ToolChoice == nil {
			wire.ToolChoice = &anthropic.ToolChoice{Type: "tool", Name: def.Name}
		}
	}

	if req.PromptCaching && len(wire.Tools) > 0 {
		wire.Tools[len(wire.Tools)-1].CacheControl = &anthropic.CacheControl{Type: "ephemeral"}
	}

	return nil
}

// anthropicSystemField encodes the system prompt. With prompt caching on it is
// sent as a block array so it can carry a cache breakpoint; otherwise a plain
// string suffices.
func anthropicSystemField(text string, promptCaching bool) (json.RawMessage, error) {
	if !promptCaching {
		raw, err := json.Marshal(text)
		if err != nil {
			return nil, fmt.Errorf("vage: encode anthropic system prompt: %w", err)
		}

		return raw, nil
	}

	raw, err := json.Marshal([]anthropic.ContentBlock{{
		Type:         "text",
		Text:         text,
		CacheControl: &anthropic.CacheControl{Type: "ephemeral"},
	}})
	if err != nil {
		return nil, fmt.Errorf("vage: encode anthropic system prompt: %w", err)
	}

	return raw, nil
}

// anthropicFinishReason maps an Anthropic stop reason onto vage's vocabulary.
// Reasons with no cross-vendor equivalent (refusal, pause_turn, …) pass
// through verbatim so callers can recognize them.
func anthropicFinishReason(reason string) FinishReason {
	switch reason {
	case anthropicStopEndTurn, anthropicStopSequence:
		return FinishReasonStop
	case anthropicStopMaxTokens:
		return FinishReasonLength
	case anthropicStopToolUse:
		return FinishReasonToolCalls
	case "":
		return ""
	default:
		return FinishReason(reason)
	}
}

// anthropicUsage normalizes Anthropic's usage accounting. Anthropic reports
// input tokens exclusive of cached tokens, so the prompt total is the sum of
// fresh, cache-write and cache-read tokens — that is what OpenAI's
// prompt_tokens already means, and what vage's budgets assume.
func anthropicUsage(u *anthropic.MessagesUsage) schema.Usage {
	if u == nil {
		return schema.Usage{}
	}

	prompt := u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens

	usage := schema.Usage{
		PromptTokens:     prompt,
		CompletionTokens: u.OutputTokens,
		TotalTokens:      prompt + u.OutputTokens,
		CacheReadTokens:  u.CacheReadInputTokens,
		CacheWriteTokens: u.CacheCreationInputTokens,
		ServiceTier:      u.ServiceTier,
	}

	if u.OutputTokensDetails != nil {
		usage.ReasoningTokens = u.OutputTokensDetails.ThinkingTokens
	}

	return usage
}

// anthropicStreamDecoder turns Anthropic's block-oriented SSE events into
// vage chunks. Anthropic streams a block's type once, in content_block_start,
// and identifies later deltas by index alone; the decoder therefore tracks
// which indices are tool_use blocks so their partial JSON reaches the right
// tool call.
type anthropicStreamDecoder struct {
	stream *anthropic.MessageStream

	// toolIndex maps a content-block index to its tool-call ordinal, so
	// parallel tool calls accumulate independently.
	toolIndex map[int]int

	// toolCount is the number of tool_use blocks opened so far, and thus the
	// ordinal assigned to the next one.
	toolCount int

	// startUsage retains the input and cache counts from message_start.
	// Anthropic splits usage across the stream: message_start reports the
	// prompt side, and the terminal message_delta typically reports only
	// output_tokens. The two are merged so the final usage is complete.
	startUsage anthropic.MessagesUsage
}

// next returns the next chunk, skipping events that carry no information for
// the caller (ping, message_stop, and unmodelled future event types).
func (d *anthropicStreamDecoder) next() (*Chunk, error) {
	for {
		event, err := d.stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil, io.EOF
			}

			return nil, normalizeAnthropicError(err)
		}

		chunk, ok := d.translate(event)
		if !ok {
			continue
		}

		return chunk, nil
	}
}

// translate converts one event into a chunk, reporting false when the event
// carries nothing the caller needs.
func (d *anthropicStreamDecoder) translate(event *anthropic.StreamEvent) (*Chunk, bool) {
	switch event.Type {
	case anthropicEventContentBlockStart:
		return d.translateBlockStart(event)
	case anthropicEventContentBlockDelta:
		return d.translateBlockDelta(event)
	case anthropicEventMessageDelta:
		return d.translateMessageDelta(event)
	case anthropicEventMessageStart:
		return d.translateMessageStart(event)
	case anthropicEventError:
		return nil, false
	default:
		return nil, false
	}
}

// translateBlockStart records a newly opened block. A tool_use block opens a
// tool call, carrying its id and name; other block types start no chunk.
func (d *anthropicStreamDecoder) translateBlockStart(event *anthropic.StreamEvent) (*Chunk, bool) {
	start := event.ContentBlockStart
	if start == nil || start.ContentBlock.Type != anthropicBlockToolUse {
		return nil, false
	}

	ordinal, seen := d.toolIndex[start.Index]
	if !seen {
		ordinal = d.toolCount
		d.toolIndex[start.Index] = ordinal
		d.toolCount++
	}

	return &Chunk{ToolCallDeltas: []ToolCallDelta{{
		Index: ordinal,
		ID:    start.ContentBlock.ID,
		Name:  start.ContentBlock.Name,
	}}}, true
}

// translateBlockDelta converts a delta into text, thinking, or a fragment of a
// tool call's arguments, depending on the type of the block it belongs to.
func (d *anthropicStreamDecoder) translateBlockDelta(event *anthropic.StreamEvent) (*Chunk, bool) {
	delta := event.ContentBlockDelta
	if delta == nil {
		return nil, false
	}

	if partial := delta.Delta.PartialJSON; partial != "" {
		ordinal, ok := d.toolIndex[delta.Index]
		if !ok {
			return nil, false
		}

		return &Chunk{ToolCallDeltas: []ToolCallDelta{{
			Index:          ordinal,
			ArgumentsDelta: partial,
		}}}, true
	}

	if delta.Delta.Text != "" {
		return &Chunk{TextDelta: delta.Delta.Text}, true
	}

	if delta.Delta.Thinking != "" {
		return &Chunk{ThinkingDelta: delta.Delta.Thinking}, true
	}

	return nil, false
}

// translateMessageDelta carries the terminal stop reason and the final usage.
// The event's usage typically reports output_tokens alone, so it is merged
// over the prompt-side counts captured at message_start.
func (d *anthropicStreamDecoder) translateMessageDelta(event *anthropic.StreamEvent) (*Chunk, bool) {
	md := event.MessageDelta
	if md == nil {
		return nil, false
	}

	chunk := &Chunk{FinishReason: anthropicFinishReason(md.Delta.StopReason)}

	if md.Usage != nil {
		merged := d.startUsage
		mergeAnthropicUsage(&merged, md.Usage)

		usage := anthropicUsage(&merged)
		chunk.Usage = &usage
	}

	if chunk.FinishReason == "" && chunk.Usage == nil {
		return nil, false
	}

	return chunk, true
}

// translateMessageStart records the prompt-side usage for later merging. It
// starts no chunk of its own: reporting usage here as well as on the terminal
// event would bill the prompt tokens twice.
func (d *anthropicStreamDecoder) translateMessageStart(event *anthropic.StreamEvent) (*Chunk, bool) {
	if event.MessageStart == nil {
		return nil, false
	}

	d.startUsage = event.MessageStart.Message.Usage

	return nil, false
}

// mergeAnthropicUsage overlays the counts a later event reports onto the
// baseline from message_start. Only non-zero fields overwrite, because the
// terminal event omits the prompt-side counts rather than zeroing them.
func mergeAnthropicUsage(base, next *anthropic.MessagesUsage) {
	if next.InputTokens != 0 {
		base.InputTokens = next.InputTokens
	}

	if next.OutputTokens != 0 {
		base.OutputTokens = next.OutputTokens
	}

	if next.CacheCreationInputTokens != 0 {
		base.CacheCreationInputTokens = next.CacheCreationInputTokens
	}

	if next.CacheReadInputTokens != 0 {
		base.CacheReadInputTokens = next.CacheReadInputTokens
	}

	if next.OutputTokensDetails != nil {
		base.OutputTokensDetails = next.OutputTokensDetails
	}

	if next.ServiceTier != "" {
		base.ServiceTier = next.ServiceTier
	}
}

// normalizeAnthropicError converts a native Anthropic failure into vage's
// APIError so the governance middlewares can judge it uniformly.
func normalizeAnthropicError(err error) error {
	var httpErr *anthropic.HTTPError
	if !errors.As(err, &httpErr) {
		return err
	}

	return &APIError{
		StatusCode: httpErr.StatusCode,
		Type:       httpErr.Type,
		Message:    httpErr.Message,
		Err:        err,
	}
}
