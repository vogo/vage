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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/vogo/aimodel/openai"
	"github.com/vogo/vage/largemodel/internal/modelcore"
	"github.com/vogo/vage/schema"
)

// ChatCodec speaks OpenAI's Chat Completions protocol on behalf of the
// largemodel facade: it assembles the native request, normalizes the native
// response, decodes the native stream and classifies native failures. Every
// decision that depends on the Chat Completions wire shape is made here, so
// adding or changing a vendor field has exactly one landing site.
//
// It also serves OpenAI-compatible endpoints, which speak the same protocol at
// a different base URL. The backend is any ChatCompleter — a single-endpoint
// *openai.Client, a routed ComposeClient, or a test double — and the codec
// neither retries nor routes around it.
type ChatCodec struct {
	client ChatCompleter
}

// NewChatCodec binds the codec to a backend.
func NewChatCodec(client ChatCompleter) *ChatCodec { return &ChatCodec{client: client} }

// Protocol implements modelcore.Codec.
func (c *ChatCodec) Protocol() schema.Protocol { return schema.ProtocolOpenAIChat }

// NativeStructuredOutput implements modelcore.Codec.
func (c *ChatCodec) NativeStructuredOutput() bool { return true }

// Call implements modelcore.Codec.
func (c *ChatCodec) Call(ctx context.Context, req *modelcore.Request) (*modelcore.Result, error) {
	wire, err := buildChatRequest(req)
	if err != nil {
		return nil, err
	}

	resp, err := c.client.ChatCompletions(ctx, wire)
	if err != nil {
		return nil, normalizeHTTPError(err)
	}

	if len(resp.Choices) == 0 {
		return nil, modelcore.ErrEmptyResponse
	}

	choice := resp.Choices[0]

	// Re-encoding the choice message and decoding it back through the message
	// codec is what keeps the assistant turn replayable verbatim: the origin
	// payload is the vendor's own wire form, not a re-rendering of it.
	wirePayload, err := json.Marshal(choice.Message)
	if err != nil {
		return nil, fmt.Errorf("vage: encode openai response message: %w", err)
	}

	msg, err := DecodeOpenAIMessage(wirePayload, "")
	if err != nil {
		return nil, fmt.Errorf("vage: decode openai response message: %w", err)
	}

	out := &modelcore.Result{
		ID:      resp.ID,
		Model:   resp.Model,
		Message: msg,
		Usage:   chatUsage(resp.Usage, resp.ServiceTier),
	}

	// OpenAI's finish reasons already are vage's vocabulary, so an unknown one
	// passes through rather than being folded into a neighbouring meaning.
	if choice.FinishReason != nil {
		out.FinishReason = *choice.FinishReason
	}

	return out, nil
}

// CallStream implements modelcore.Codec.
func (c *ChatCodec) CallStream(ctx context.Context, req *modelcore.Request) (*modelcore.Stream, error) {
	wire, err := buildChatRequest(req)
	if err != nil {
		return nil, err
	}

	stream, err := c.client.ChatCompletionsStream(ctx, wire)
	if err != nil {
		return nil, normalizeHTTPError(err)
	}

	return &modelcore.Stream{
		Recv: func() (*modelcore.Chunk, error) {
			chunk, recvErr := stream.Recv()
			if recvErr != nil {
				if errors.Is(recvErr, io.EOF) {
					return nil, io.EOF
				}

				return nil, normalizeHTTPError(recvErr)
			}

			return chatChunk(chunk), nil
		},
		Close: stream.Close,
	}, nil
}

// buildChatRequest translates a canonical request into OpenAI's native
// request. Both the streaming and the non-streaming path go through it, so the
// two never drift; it rejects messages recorded under another protocol rather
// than silently reinterpreting a wire form the vendor never produced, and it
// fails before any network I/O.
func buildChatRequest(req *modelcore.Request) (*openai.ChatCompletionRequest, error) {
	wire := &openai.ChatCompletionRequest{
		Model:       req.Model,
		Temperature: req.Temperature,
		// OpenAI deprecated max_tokens and reasoning models reject it, so
		// vage only ever sets max_completion_tokens.
		MaxCompletionTokens: req.MaxTokens,
		Stop:                req.Stop,
		TopP:                req.TopP,
		Seed:                req.Seed,
		FrequencyPenalty:    req.FrequencyPenalty,
		PresencePenalty:     req.PresencePenalty,
	}

	for i := range req.Messages {
		msg := req.Messages[i]

		if err := msg.RequireProtocol(schema.ProtocolOpenAIChat); err != nil {
			return nil, err
		}

		native, err := EncodeOpenAIMessage(msg)
		if err != nil {
			return nil, fmt.Errorf("vage: openai message %d encode: %w", i, err)
		}

		wire.Messages = append(wire.Messages, native)
	}

	for _, def := range req.Tools {
		wire.Tools = append(wire.Tools, openai.ChatCompletionTool{
			Type: openai.ToolTypeFunction,
			Function: openai.ChatCompletionFunction{
				Name:        def.Name,
				Description: def.Description,
				Parameters:  def.Parameters,
			},
		})
	}

	if choice, err := chatToolChoice(req); err != nil {
		return nil, err
	} else if choice != nil {
		wire.ToolChoice = choice
	}

	if req.ResponseSchema != nil {
		wire.ResponseFormat = chatResponseFormat(req.ResponseSchema)
	}

	extra, err := extraBodyFromRequest(req)
	if err != nil {
		return nil, err
	}

	wire.ExtraBody = extra

	return wire, nil
}

// responseSchemaFormatName is the fixed name vage sends in every OpenAI
// json_schema response_format, so the same ResponseSchema always produces the
// same wire shape and identical requests keep hitting prompt cache.
const responseSchemaFormatName = "vage_response_schema"

// chatResponseFormat builds Chat Completions' response_format for a
// caller-supplied JSON Schema: strict json_schema mode under a stable name.
// The schema is passed through unmodified.
func chatResponseFormat(respSchema any) map[string]any {
	return map[string]any{
		"type": "json_schema",
		"json_schema": map[string]any{
			"name":   responseSchemaFormatName,
			"schema": respSchema,
			"strict": true,
		},
	}
}

// chatToolChoice maps the canonical tool-choice, preferring Request.ToolChoice
// over ToolDef.ForceUse when the former is set.
func chatToolChoice(req *modelcore.Request) (any, error) {
	if req.ToolChoice != nil {
		switch req.ToolChoice.Mode {
		case "auto":
			return "auto", nil
		case "none":
			return "none", nil
		case "required":
			return "required", nil
		case "named":
			return map[string]any{
				"type":     openai.ToolTypeFunction,
				"function": map[string]any{"name": req.ToolChoice.Name},
			}, nil
		default:
			return nil, fmt.Errorf("vage: invalid tool_choice mode %q", req.ToolChoice.Mode)
		}
	}

	return chatForcedToolChoice(req.Tools), nil
}

// chatForcedToolChoice returns OpenAI's tool_choice value when a tool is marked
// ForceUse, or nil to leave the choice to the model.
func chatForcedToolChoice(defs []schema.ToolDef) any {
	for _, def := range defs {
		if def.ForceUse {
			return map[string]any{
				"type":     openai.ToolTypeFunction,
				"function": map[string]any{"name": def.Name},
			}
		}
	}

	return nil
}

// chatUsage normalizes OpenAI's usage accounting, pulling the cache and
// reasoning subtotals out of the detail objects.
func chatUsage(u *openai.ChatCompletionUsage, serviceTier string) schema.Usage {
	if u == nil {
		return schema.Usage{ServiceTier: serviceTier}
	}

	usage := schema.Usage{
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
		ServiceTier:      serviceTier,
	}

	if u.PromptTokensDetails != nil {
		usage.CacheReadTokens = u.PromptTokensDetails.CachedTokens
	}

	if u.CompletionTokensDetails != nil {
		usage.ReasoningTokens = u.CompletionTokensDetails.ReasoningTokens
	}

	return usage
}

// chatChunk normalizes one streaming chunk. A chunk may carry fragments of
// several parallel tool calls at once, so every fragment is forwarded and the
// accumulator upstream merges them by index.
func chatChunk(chunk *openai.ChatCompletionChunk) *modelcore.Chunk {
	out := &modelcore.Chunk{}

	if chunk.Usage != nil {
		usage := chatUsage(chunk.Usage, chunk.ServiceTier)
		out.Usage = &usage
	}

	if len(chunk.Choices) == 0 {
		return out
	}

	choice := chunk.Choices[0]
	out.TextDelta = choice.Delta.Content.Text()
	out.ThinkingDelta = choice.Delta.ReasoningContent

	if choice.FinishReason != nil {
		out.FinishReason = *choice.FinishReason
	}

	for _, call := range choice.Delta.ToolCalls {
		out.ToolCallDeltas = append(out.ToolCallDeltas, modelcore.ToolCallDelta{
			Index:          call.Index,
			ID:             call.ID,
			Name:           call.Function.Name,
			ArgumentsDelta: call.Function.Arguments,
		})
	}

	return out
}

// normalizeHTTPError classifies a native OpenAI failure so the governance
// middlewares upstream can judge it without knowing which vendor produced it.
// Anything that is not an OpenAI HTTP error — a transport failure, a cancelled
// context — is returned untouched rather than dressed up as an API error.
func normalizeHTTPError(err error) error {
	var httpErr *openai.HTTPError
	if !errors.As(err, &httpErr) {
		return err
	}

	return &modelcore.APIError{
		StatusCode: httpErr.Status,
		Code:       httpErr.Code,
		Type:       httpErr.Type,
		Message:    httpErr.Message,
		Err:        err,
	}
}

var _ modelcore.Codec = (*ChatCodec)(nil)
