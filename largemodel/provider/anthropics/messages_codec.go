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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/vogo/aimodel/anthropic"
	"github.com/vogo/vage/largemodel/internal/modelcore"
	"github.com/vogo/vage/schema"
)

// defaultMaxTokens is sent when a request sets no cap. Anthropic requires
// max_tokens on every Messages call, so a default is mandatory rather than
// merely convenient.
const defaultMaxTokens = 4096

// statusOverloaded is Anthropic's 529, returned when the API is temporarily
// over capacity. It has no net/http constant because it is outside the
// registered status codes.
const statusOverloaded = 529

// MessagesCodec speaks Anthropic's Messages protocol on behalf of the
// largemodel facade. Anthropic differs structurally from OpenAI in ways this
// type absorbs so that nothing above it has to know them: system text belongs
// to a request field rather than a message, tool calls and results are content
// blocks rather than message fields, max_tokens is mandatory, and the stream
// names a block's type once and then addresses it by index alone.
type MessagesCodec struct {
	client Messenger
}

// NewMessagesCodec binds the codec to a backend.
func NewMessagesCodec(client Messenger) *MessagesCodec { return &MessagesCodec{client: client} }

// Protocol implements modelcore.Codec.
func (c *MessagesCodec) Protocol() schema.Protocol { return schema.ProtocolAnthropicMessages }

// Call implements modelcore.Codec.
func (c *MessagesCodec) Call(ctx context.Context, req *modelcore.Request) (*modelcore.Result, error) {
	wire, err := buildMessagesRequest(req)
	if err != nil {
		return nil, err
	}

	resp, err := c.client.Messages(ctx, wire)
	if err != nil {
		return nil, normalizeHTTPError(err)
	}

	if len(resp.Content) == 0 && resp.StopReason == "" {
		return nil, modelcore.ErrEmptyResponse
	}

	msg, err := decodeMessagesResponse(resp)
	if err != nil {
		return nil, err
	}

	return &modelcore.Result{
		ID:           resp.ID,
		Model:        resp.Model,
		Message:      msg,
		FinishReason: messagesFinishReason(resp.StopReason),
		Usage:        messagesUsage(&resp.Usage),
	}, nil
}

// CallStream implements modelcore.Codec.
func (c *MessagesCodec) CallStream(ctx context.Context, req *modelcore.Request) (*modelcore.Stream, error) {
	wire, err := buildMessagesRequest(req)
	if err != nil {
		return nil, err
	}

	stream, err := c.client.MessagesStream(ctx, wire)
	if err != nil {
		return nil, normalizeHTTPError(err)
	}

	// Anthropic identifies a content block by its position in the message,
	// and streams the block's type only in content_block_start. Later deltas
	// carry the index alone, so the decoder remembers which indices opened as
	// tool_use blocks in order to route their partial JSON correctly.
	decoder := &messagesStreamDecoder{
		stream:    stream,
		toolIndex: map[int]int{},
	}

	return &modelcore.Stream{Recv: decoder.next, Close: stream.Close}, nil
}

// decodeMessagesResponse re-encodes the response blocks as a request-shaped
// assistant message so the turn can be replayed verbatim on the next
// iteration, then decodes it through the message codec.
func decodeMessagesResponse(resp *anthropic.MessagesResponse) (schema.Message, error) {
	blocks := make([]json.RawMessage, 0, len(resp.Content))
	for _, block := range resp.Content {
		blocks = append(blocks, block.Raw)
	}

	content, err := json.Marshal(blocks)
	if err != nil {
		return schema.Message{}, fmt.Errorf("vage: encode anthropic response content: %w", err)
	}

	wirePayload, err := json.Marshal(anthropic.MessagesMessage{
		Role:    resp.Role,
		Content: content,
	})
	if err != nil {
		return schema.Message{}, fmt.Errorf("vage: encode anthropic response message wire: %w", err)
	}

	msg, err := DecodeAnthropicMessage(wirePayload, "")
	if err != nil {
		return schema.Message{}, fmt.Errorf("vage: decode anthropic response message: %w", err)
	}

	return msg, nil
}

// buildMessagesRequest translates a canonical request into Anthropic's native
// request, hoisting system text to the request-level field and rejecting
// messages recorded under another protocol. Both the streaming and the
// non-streaming path go through it, and it fails before any network I/O.
func buildMessagesRequest(req *modelcore.Request) (*anthropic.MessagesRequest, error) {
	maxTokens := defaultMaxTokens
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

		native, err := EncodeAnthropicMessage(msg)
		if err != nil {
			return nil, fmt.Errorf("vage: anthropic message %d encode: %w", i, err)
		}

		wire.Messages = append(wire.Messages, native)
	}

	// Parallel tool calls produce one tool_result message each, but the
	// Messages API accepts them only as a single user message following the
	// assistant turn that requested them.
	merged, err := MergeAnthropicToolResults(wire.Messages)
	if err != nil {
		return nil, fmt.Errorf("vage: merge anthropic tool results: %w", err)
	}

	wire.Messages = merged

	if len(systemParts) > 0 {
		system, err := messagesSystemField(strings.Join(systemParts, "\n\n"), req.PromptCaching)
		if err != nil {
			return nil, err
		}

		wire.System = system
	}

	buildMessagesTools(req, wire)

	if req.ResponseSchema != nil {
		if wire.OutputConfig == nil {
			wire.OutputConfig = &anthropic.OutputConfig{}
		}

		wire.OutputConfig.Format = &anthropic.OutputFormat{
			Type:   "json_schema",
			Schema: req.ResponseSchema,
		}
	}

	return wire, nil
}

// buildMessagesTools maps vage tool definitions onto Anthropic tools, marking
// the last one as a cache breakpoint when prompt caching is on so the whole
// tool block is cached across ReAct iterations.
func buildMessagesTools(req *modelcore.Request, wire *anthropic.MessagesRequest) {
	for _, def := range req.Tools {
		wire.Tools = append(wire.Tools, anthropic.MessagesTool{
			Name:        def.Name,
			Description: def.Description,
			InputSchema: def.Parameters,
		})

		if def.ForceUse && wire.ToolChoice == nil {
			wire.ToolChoice = &anthropic.ToolChoice{Type: anthropic.ToolChoiceTypeTool, Name: def.Name}
		}
	}

	if req.PromptCaching && len(wire.Tools) > 0 {
		wire.Tools[len(wire.Tools)-1].CacheControl = &anthropic.CacheControl{
			Type: anthropic.CacheControlTypeEphemeral,
		}
	}
}

// messagesSystemField encodes the system prompt. With prompt caching on it is
// sent as a block array so it can carry a cache breakpoint; otherwise a plain
// string suffices.
func messagesSystemField(text string, promptCaching bool) (json.RawMessage, error) {
	if !promptCaching {
		raw, err := json.Marshal(text)
		if err != nil {
			return nil, fmt.Errorf("vage: encode anthropic system prompt: %w", err)
		}

		return raw, nil
	}

	raw, err := json.Marshal([]anthropic.ContentBlock{{
		Type:         anthropic.ContentBlockTypeText,
		Text:         text,
		CacheControl: &anthropic.CacheControl{Type: anthropic.CacheControlTypeEphemeral},
	}})
	if err != nil {
		return nil, fmt.Errorf("vage: encode anthropic system prompt: %w", err)
	}

	return raw, nil
}

// messagesFinishReason maps an Anthropic stop reason onto vage's vocabulary.
// Reasons with no cross-vendor equivalent (refusal, pause_turn, …) pass
// through verbatim so callers can recognize them.
func messagesFinishReason(reason string) string {
	switch reason {
	case anthropic.StopReasonEndTurn, anthropic.StopReasonStopSequence:
		return finishReasonStop
	case anthropic.StopReasonMaxTokens:
		return finishReasonLength
	case anthropic.StopReasonToolUse:
		return finishReasonToolCalls
	default:
		return reason
	}
}

// The cross-vendor finish reasons this codec maps onto. They are the wire
// spelling of largemodel's FinishReason constants; the neutral bridge carries
// a plain string, so the codec states the spelling it produces.
const (
	finishReasonStop      = "stop"
	finishReasonLength    = "length"
	finishReasonToolCalls = "tool_calls"
)

// messagesUsage normalizes Anthropic's usage accounting. Anthropic reports
// input tokens exclusive of cached tokens, so the prompt total is the sum of
// fresh, cache-write and cache-read tokens — that is what OpenAI's
// prompt_tokens already means, and what vage's budgets assume.
func messagesUsage(u *anthropic.MessagesUsage) schema.Usage {
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

// messagesStreamDecoder turns Anthropic's block-oriented SSE events into
// neutral chunks. Anthropic streams a block's type once, in
// content_block_start, and identifies later deltas by index alone; the decoder
// therefore tracks which indices are tool_use blocks so their partial JSON
// reaches the right tool call.
type messagesStreamDecoder struct {
	stream *anthropic.MessageStream

	// toolIndex maps a content-block index to its tool-call ordinal, so
	// parallel tool calls accumulate independently.
	toolIndex map[int]int

	// toolCount is the number of tool_use blocks opened so far, and thus the
	// ordinal assigned to the next one.
	toolCount int
}

// next returns the next chunk, skipping events that carry no information for
// the caller (ping, message_stop, and unmodelled future event types).
func (d *messagesStreamDecoder) next() (*modelcore.Chunk, error) {
	for {
		event, err := d.stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil, io.EOF
			}

			return nil, normalizeHTTPError(err)
		}

		// An error event is delivered as an ordinary event rather than as a
		// transport error, so the stream has to be failed here. Skipping it
		// would let a truncated turn pass for a complete one, and the
		// governance middlewares would never see the failure.
		if event.Type == anthropic.StreamEventTypeError {
			return nil, messagesStreamError(event.Error)
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
func (d *messagesStreamDecoder) translate(event *anthropic.StreamEvent) (*modelcore.Chunk, bool) {
	switch event.Type {
	case anthropic.StreamEventTypeContentBlockStart:
		return d.translateBlockStart(event)
	case anthropic.StreamEventTypeContentBlockDelta:
		return d.translateBlockDelta(event)
	case anthropic.StreamEventTypeMessageDelta:
		return d.translateMessageDelta(event)
	default:
		return nil, false
	}
}

// translateBlockStart records a newly opened block. A tool_use block opens a
// tool call, carrying its id and name; other block types start no chunk.
func (d *messagesStreamDecoder) translateBlockStart(event *anthropic.StreamEvent) (*modelcore.Chunk, bool) {
	start := event.ContentBlockStart
	if start == nil || start.ContentBlock.Type != anthropic.ContentBlockTypeToolUse {
		return nil, false
	}

	ordinal, seen := d.toolIndex[start.Index]
	if !seen {
		ordinal = d.toolCount
		d.toolIndex[start.Index] = ordinal
		d.toolCount++
	}

	return &modelcore.Chunk{ToolCallDeltas: []modelcore.ToolCallDelta{{
		Index: ordinal,
		ID:    start.ContentBlock.ID,
		Name:  start.ContentBlock.Name,
	}}}, true
}

// translateBlockDelta converts a delta into text, thinking, or a fragment of a
// tool call's arguments, depending on the type of the block it belongs to.
func (d *messagesStreamDecoder) translateBlockDelta(event *anthropic.StreamEvent) (*modelcore.Chunk, bool) {
	delta := event.ContentBlockDelta
	if delta == nil {
		return nil, false
	}

	if partial := delta.Delta.PartialJSON; partial != "" {
		ordinal, ok := d.toolIndex[delta.Index]
		if !ok {
			return nil, false
		}

		return &modelcore.Chunk{ToolCallDeltas: []modelcore.ToolCallDelta{{
			Index:          ordinal,
			ArgumentsDelta: partial,
		}}}, true
	}

	if delta.Delta.Text != "" {
		return &modelcore.Chunk{TextDelta: delta.Delta.Text}, true
	}

	if delta.Delta.Thinking != "" {
		return &modelcore.Chunk{ThinkingDelta: delta.Delta.Thinking}, true
	}

	return nil, false
}

// translateMessageDelta carries the terminal stop reason and the final usage.
// The event's usage typically reports output_tokens alone, so it is merged
// over the prompt-side counts captured at message_start.
func (d *messagesStreamDecoder) translateMessageDelta(event *anthropic.StreamEvent) (*modelcore.Chunk, bool) {
	md := event.MessageDelta
	if md == nil {
		return nil, false
	}

	chunk := &modelcore.Chunk{FinishReason: messagesFinishReason(md.Delta.StopReason)}

	// The stream folds message_start's prompt-side counts together with this
	// event's output counts as it decodes, so its snapshot is already the
	// complete accounting for the turn. Reporting it only here, on the
	// terminal event, is what keeps the prompt tokens from being billed twice.
	if md.Usage != nil {
		if merged := d.stream.Usage(); merged != nil {
			usage := messagesUsage(merged)
			chunk.Usage = &usage
		}
	}

	if chunk.FinishReason == "" && chunk.Usage == nil {
		return nil, false
	}

	return chunk, true
}

// messagesStreamError converts a mid-stream error event into a classified
// failure. The event carries no HTTP status of its own — the response headers
// were already 200 by the time it arrived — so the status is derived from
// Anthropic's error type, which is what overflow detection and other
// governance middlewares judge on.
func messagesStreamError(resp *anthropic.MessagesErrorResponse) error {
	if resp == nil {
		return &modelcore.APIError{
			StatusCode: http.StatusInternalServerError,
			Message:    "anthropic stream reported an error event without detail",
		}
	}

	return &modelcore.APIError{
		StatusCode: messagesErrorStatus(resp.Error.Type),
		Type:       resp.Error.Type,
		Message:    resp.Error.Message,
	}
}

// messagesErrorStatus maps an Anthropic error type onto the HTTP status the
// same failure carries when it arrives as a non-2xx response, so a mid-stream
// failure is judged exactly like one that happened before the stream opened.
func messagesErrorStatus(errType string) int {
	switch errType {
	case "invalid_request_error":
		return http.StatusBadRequest
	case "authentication_error":
		return http.StatusUnauthorized
	case "permission_error":
		return http.StatusForbidden
	case "not_found_error":
		return http.StatusNotFound
	case "request_too_large":
		return http.StatusRequestEntityTooLarge
	case "rate_limit_error":
		return http.StatusTooManyRequests
	case "timeout_error":
		return http.StatusRequestTimeout
	case "overloaded_error":
		return statusOverloaded
	default:
		// api_error and anything Anthropic adds later: treat as a server-side
		// fault, which is both true of api_error and the safer default.
		return http.StatusInternalServerError
	}
}

// normalizeHTTPError classifies a native Anthropic failure so the governance
// middlewares upstream can judge it uniformly. Anything that is not an
// Anthropic HTTP error is returned untouched.
func normalizeHTTPError(err error) error {
	var httpErr *anthropic.HTTPError
	if !errors.As(err, &httpErr) {
		return err
	}

	return &modelcore.APIError{
		StatusCode: httpErr.Status,
		Type:       httpErr.Type,
		Message:    httpErr.Message,
		Err:        err,
	}
}

var _ modelcore.Codec = (*MessagesCodec)(nil)
