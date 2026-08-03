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
	"errors"
	"fmt"
	"io"

	"github.com/vogo/aimodel/provider/openai"
	"github.com/vogo/vage/schema"
)

// ErrEmptyResponse reports a successful vendor response that carried no
// choices, leaving nothing for the agent to act on.
var ErrEmptyResponse = errors.New("vage: empty response from model")

// openAIChatCaller calls OpenAI's Chat Completions API through the native
// aimodel client, translating between vage's envelopes and OpenAI's wire
// types. It also serves OpenAI-compatible endpoints, which speak the same
// protocol at a different base URL.
type openAIChatCaller struct {
	client *openai.Client
}

// NewOpenAIChatCaller builds a Caller over OpenAI's Chat Completions API.
// baseURL may be empty to use OpenAI's own endpoint, or point at any
// OpenAI-compatible endpoint. The API key is required and validated here so
// misconfiguration surfaces before any call is attempted.
func NewOpenAIChatCaller(apiKey, baseURL string, opts ...openai.ClientOption) (Caller, error) {
	if apiKey == "" {
		return nil, ErrNoAPIKey
	}

	if baseURL != "" {
		opts = append([]openai.ClientOption{openai.WithBaseURL(baseURL)}, opts...)
	}

	return &openAIChatCaller{client: openai.NewClient(apiKey, opts...)}, nil
}

// Protocol implements Caller.
func (c *openAIChatCaller) Protocol() schema.Protocol { return schema.ProtocolOpenAIChat }

// Call implements Caller.
func (c *openAIChatCaller) Call(ctx context.Context, req *Request) (*Response, error) {
	wire, err := c.buildRequest(req)
	if err != nil {
		return nil, err
	}

	resp, err := c.client.ChatCompletions(ctx, wire)
	if err != nil {
		return nil, normalizeOpenAIError(err)
	}

	if len(resp.Choices) == 0 {
		return nil, ErrEmptyResponse
	}

	choice := resp.Choices[0]

	out := &Response{
		ID:      resp.ID,
		Model:   resp.Model,
		Message: schema.NewOpenAIMessage(schema.ProtocolOpenAIChat, choice.Message, ""),
		Usage:   openAIUsage(resp.Usage, resp.ServiceTier),
	}

	if choice.FinishReason != nil {
		out.FinishReason = FinishReason(*choice.FinishReason)
	}

	return out, nil
}

// CallStream implements Caller.
func (c *openAIChatCaller) CallStream(ctx context.Context, req *Request) (*Stream, error) {
	wire, err := c.buildRequest(req)
	if err != nil {
		return nil, err
	}

	stream, err := c.client.ChatCompletionsStream(ctx, wire)
	if err != nil {
		return nil, normalizeOpenAIError(err)
	}

	return NewStream(func() (*Chunk, error) {
		chunk, recvErr := stream.Recv()
		if recvErr != nil {
			if errors.Is(recvErr, io.EOF) {
				return nil, io.EOF
			}

			return nil, normalizeOpenAIError(recvErr)
		}

		return openAIChunk(chunk), nil
	}, stream.Close), nil
}

// buildRequest translates a vage request into OpenAI's native request. It
// rejects messages recorded under another protocol rather than silently
// reinterpreting a wire form the vendor never produced.
func (c *openAIChatCaller) buildRequest(req *Request) (*openai.ChatCompletionRequest, error) {
	wire := &openai.ChatCompletionRequest{
		Model:       req.Model,
		Temperature: req.Temperature,
		// OpenAI deprecated max_tokens and reasoning models reject it, so
		// vage only ever sets max_completion_tokens.
		MaxCompletionTokens: req.MaxTokens,
		Stop:                req.Stop,
	}

	for i := range req.Messages {
		msg := req.Messages[i]

		if err := msg.RequireProtocol(schema.ProtocolOpenAIChat); err != nil {
			return nil, err
		}

		if msg.OpenAI == nil {
			return nil, fmt.Errorf("vage: openai message %d has no wire payload", i)
		}

		wire.Messages = append(wire.Messages, *msg.OpenAI)
	}

	for _, def := range req.Tools {
		wire.Tools = append(wire.Tools, openai.ChatCompletionTool{
			Type: "function",
			Function: openai.ChatCompletionFunction{
				Name:        def.Name,
				Description: def.Description,
				Parameters:  def.Parameters,
			},
		})
	}

	if choice := forcedToolChoice(req.Tools); choice != nil {
		wire.ToolChoice = choice
	}

	return wire, nil
}

// forcedToolChoice returns OpenAI's tool_choice value when a tool is marked
// ForceUse, or nil to leave the choice to the model.
func forcedToolChoice(defs []schema.ToolDef) any {
	for _, def := range defs {
		if def.ForceUse {
			return map[string]any{
				"type":     "function",
				"function": map[string]any{"name": def.Name},
			}
		}
	}

	return nil
}

// openAIUsage normalizes OpenAI's usage accounting, pulling the cache and
// reasoning subtotals out of the detail objects.
func openAIUsage(u *openai.ChatCompletionUsage, serviceTier string) schema.Usage {
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

// openAIChunk normalizes one streaming chunk. A chunk may carry fragments of
// several parallel tool calls at once, so every fragment is forwarded and the
// accumulator merges them by index.
func openAIChunk(chunk *openai.ChatCompletionChunk) *Chunk {
	out := &Chunk{}

	if chunk.Usage != nil {
		usage := openAIUsage(chunk.Usage, chunk.ServiceTier)
		out.Usage = &usage
	}

	if len(chunk.Choices) == 0 {
		return out
	}

	choice := chunk.Choices[0]
	out.TextDelta = choice.Delta.Content.Text()
	out.ThinkingDelta = choice.Delta.ReasoningContent

	if choice.FinishReason != nil {
		out.FinishReason = FinishReason(*choice.FinishReason)
	}

	for _, call := range choice.Delta.ToolCalls {
		out.ToolCallDeltas = append(out.ToolCallDeltas, ToolCallDelta{
			Index:          call.Index,
			ID:             call.ID,
			Name:           call.Function.Name,
			ArgumentsDelta: call.Function.Arguments,
		})
	}

	return out
}

// normalizeOpenAIError converts a native OpenAI failure into vage's APIError
// so retry, circuit-breaking and overflow detection can judge it without
// knowing which vendor produced it.
func normalizeOpenAIError(err error) error {
	var httpErr *openai.HTTPError
	if !errors.As(err, &httpErr) {
		return err
	}

	return &APIError{
		StatusCode: httpErr.StatusCode,
		Code:       httpErr.Code,
		Type:       httpErr.Type,
		Message:    httpErr.Message,
		Err:        err,
	}
}
