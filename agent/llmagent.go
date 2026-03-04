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

package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/vogo/aimodel"
	"github.com/vogo/vagent/prompt"
	"github.com/vogo/vagent/schema"
	"github.com/vogo/vagent/tool"
)

const (
	defaultMaxIterations    = 10
	defaultStreamBufferSize = 32
)

// LLMAgent implements the Agent interface using a ChatCompleter with ReAct-style tool calling.
type LLMAgent struct {
	agentMeta
	systemPrompt     prompt.PromptTemplate
	model            string
	chatCompleter    aimodel.ChatCompleter
	toolRegistry     tool.ToolRegistry
	maxIterations    int
	maxTokens        *int
	temperature      *float64
	streamBufferSize int
	middlewares      []StreamMiddleware
}

var (
	_ Agent       = (*LLMAgent)(nil)
	_ StreamAgent = (*LLMAgent)(nil)
)

// LLMOption configures LLM-specific fields of an LLMAgent.
type LLMOption func(*LLMAgent)

// WithSystemPrompt sets the system prompt template.
func WithSystemPrompt(p prompt.PromptTemplate) LLMOption {
	return func(a *LLMAgent) { a.systemPrompt = p }
}

// WithModel sets the model name.
func WithModel(model string) LLMOption { return func(a *LLMAgent) { a.model = model } }

// WithChatCompleter sets the chat completion provider.
func WithChatCompleter(cc aimodel.ChatCompleter) LLMOption {
	return func(a *LLMAgent) { a.chatCompleter = cc }
}

// WithToolRegistry sets the tool registry.
func WithToolRegistry(r tool.ToolRegistry) LLMOption {
	return func(a *LLMAgent) { a.toolRegistry = r }
}

// WithMaxIterations sets the maximum ReAct loop iterations.
func WithMaxIterations(n int) LLMOption { return func(a *LLMAgent) { a.maxIterations = n } }

// WithMaxTokens sets the max tokens for LLM responses.
func WithMaxTokens(n int) LLMOption { return func(a *LLMAgent) { a.maxTokens = &n } }

// WithTemperature sets the sampling temperature.
func WithTemperature(t float64) LLMOption { return func(a *LLMAgent) { a.temperature = &t } }

// WithStreamBufferSize sets the channel buffer size for streaming events.
func WithStreamBufferSize(n int) LLMOption {
	return func(a *LLMAgent) { a.streamBufferSize = n }
}

// WithStreamMiddleware appends one or more middleware to the stream processing chain.
func WithStreamMiddleware(mw ...StreamMiddleware) LLMOption {
	return func(a *LLMAgent) { a.middlewares = append(a.middlewares, mw...) }
}

// NewLLMAgent creates a new LLMAgent with the given config and options.
func NewLLMAgent(cfg Config, opts ...LLMOption) *LLMAgent {
	a := &LLMAgent{
		agentMeta:        newAgentMeta(cfg),
		maxIterations:    defaultMaxIterations,
		streamBufferSize: defaultStreamBufferSize,
	}
	for _, o := range opts {
		o(a)
	}
	return a
}

// Tools returns the tool definitions from the registry.
func (a *LLMAgent) Tools() []schema.ToolDef {
	if a.toolRegistry == nil {
		return nil
	}
	return a.toolRegistry.List()
}

// runParams holds resolved parameters for a single run invocation.
type runParams struct {
	model       string
	temperature *float64
	maxIter     int
	maxTokens   *int
	toolFilter  []string
	stopSeq     []string
}

// resolveRunParams merges request options with agent defaults.
func (a *LLMAgent) resolveRunParams(opts *schema.RunOptions) runParams {
	p := runParams{
		model:       a.model,
		temperature: a.temperature,
		maxIter:     a.maxIterations,
		maxTokens:   a.maxTokens,
	}

	if opts == nil {
		return p
	}

	if opts.Model != "" {
		p.model = opts.Model
	}
	if opts.Temperature != nil {
		p.temperature = opts.Temperature
	}
	if opts.MaxIterations > 0 {
		p.maxIter = opts.MaxIterations
	}
	if opts.MaxTokens > 0 {
		mt := opts.MaxTokens
		p.maxTokens = &mt
	}
	p.toolFilter = opts.Tools
	p.stopSeq = opts.StopSequences

	return p
}

// buildInitialMessages builds the message list starting with the system prompt.
func (a *LLMAgent) buildInitialMessages(ctx context.Context, reqMsgs []schema.Message) ([]aimodel.Message, error) {
	messages := make([]aimodel.Message, 0, len(reqMsgs)+1)

	if a.systemPrompt != nil {
		sysText, err := a.systemPrompt.Render(ctx, nil)
		if err != nil {
			return nil, fmt.Errorf("vagent: render system prompt: %w", err)
		}
		if sysText != "" {
			messages = append(messages, aimodel.Message{
				Role:    aimodel.RoleSystem,
				Content: aimodel.NewTextContent(sysText),
			})
		}
	}

	messages = append(messages, schema.ToAIModelMessages(reqMsgs)...)
	return messages, nil
}

// prepareAITools converts registry tools to aimodel.Tool slice, applying any filter.
func (a *LLMAgent) prepareAITools(filter []string) []aimodel.Tool {
	if a.toolRegistry == nil {
		return nil
	}
	defs := a.toolRegistry.List()
	defs = tool.FilterTools(defs, filter)
	return tool.ToAIModelTools(defs)
}

// executeToolCall runs a single tool call and returns the result.
func (a *LLMAgent) executeToolCall(ctx context.Context, tc aimodel.ToolCall) schema.ToolResult {
	if a.toolRegistry == nil {
		return schema.ErrorResult(tc.ID, fmt.Sprintf("tool %q: no registry configured", tc.Function.Name))
	}

	tr, err := a.toolRegistry.Execute(ctx, tc.Function.Name, tc.Function.Arguments)
	if err != nil {
		return schema.ErrorResult(tc.ID, err.Error())
	}

	tr.ToolCallID = tc.ID
	return tr
}

// Run executes the ReAct loop: prompt -> LLM -> tool calls (loop) -> response.
func (a *LLMAgent) Run(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
	if a.chatCompleter == nil {
		return nil, errors.New("vagent: ChatCompleter is required")
	}

	start := time.Now()
	p := a.resolveRunParams(req.Options)

	messages, err := a.buildInitialMessages(ctx, req.Messages)
	if err != nil {
		return nil, err
	}

	aiTools := a.prepareAITools(p.toolFilter)

	var totalUsage aimodel.Usage

	for iter := range p.maxIter {
		_ = iter

		chatReq := &aimodel.ChatRequest{
			Model:       p.model,
			Messages:    messages,
			Temperature: p.temperature,
			MaxTokens:   p.maxTokens,
			Stop:        p.stopSeq,
			Tools:       aiTools,
		}

		resp, err := a.chatCompleter.ChatCompletion(ctx, chatReq)
		if err != nil {
			return nil, fmt.Errorf("vagent: chat completion: %w", err)
		}

		totalUsage.PromptTokens += resp.Usage.PromptTokens
		totalUsage.CompletionTokens += resp.Usage.CompletionTokens
		totalUsage.TotalTokens += resp.Usage.TotalTokens

		if len(resp.Choices) == 0 {
			return nil, errors.New("vagent: empty response from LLM")
		}

		choice := resp.Choices[0]
		assistantMsg := choice.Message
		messages = append(messages, assistantMsg)

		if choice.FinishReason != aimodel.FinishReasonToolCalls || len(assistantMsg.ToolCalls) == 0 {
			return &schema.RunResponse{
				Messages:  []schema.Message{schema.NewAssistantMessage(assistantMsg, a.id)},
				SessionID: req.SessionID,
				Usage:     &totalUsage,
				Duration:  time.Since(start).Milliseconds(),
			}, nil
		}

		for _, tc := range assistantMsg.ToolCalls {
			result := a.executeToolCall(ctx, tc)
			toolMsg := aimodel.Message{
				Role:       aimodel.RoleTool,
				ToolCallID: result.ToolCallID,
				Content:    aimodel.NewTextContent(toolResultText(result)),
			}
			messages = append(messages, toolMsg)
		}
	}

	return nil, fmt.Errorf("vagent: exceeded max iterations (%d)", p.maxIter)
}

// buildSend builds a send function with the middleware chain applied.
func (a *LLMAgent) buildSend(raw func(schema.Event) error) func(schema.Event) error {
	send := raw
	// Apply middlewares in reverse order so the first middleware is outermost.
	for i := len(a.middlewares) - 1; i >= 0; i-- {
		send = a.middlewares[i](send)
	}
	return send
}

// RunStream returns a RunStream that emits events as the ReAct loop executes.
func (a *LLMAgent) RunStream(ctx context.Context, req *schema.RunRequest) (*schema.RunStream, error) {
	if a.chatCompleter == nil {
		return nil, errors.New("vagent: ChatCompleter is required")
	}

	p := a.resolveRunParams(req.Options)

	messages, err := a.buildInitialMessages(ctx, req.Messages)
	if err != nil {
		return nil, err
	}

	aiTools := a.prepareAITools(p.toolFilter)

	return schema.NewRunStream(ctx, a.streamBufferSize, func(ctx context.Context, rawSend func(schema.Event) error) error {
		send := a.buildSend(rawSend)
		return a.runStreamLoop(ctx, req, p, messages, aiTools, send)
	}), nil
}

// runStreamLoop is the streaming ReAct loop that emits events via send.
func (a *LLMAgent) runStreamLoop(
	ctx context.Context,
	req *schema.RunRequest,
	p runParams,
	messages []aimodel.Message,
	aiTools []aimodel.Tool,
	send func(schema.Event) error,
) error {
	start := time.Now()
	agentID := a.id
	sessionID := req.SessionID

	if err := send(schema.NewEvent(schema.EventAgentStart, agentID, sessionID, schema.AgentStartData{})); err != nil {
		return err
	}

	for iter := range p.maxIter {
		// Emit iteration start event.
		if err := send(schema.NewEvent(schema.EventIterationStart, agentID, sessionID, schema.IterationStartData{
			Iteration: iter,
		})); err != nil {
			return err
		}

		chatReq := &aimodel.ChatRequest{
			Model:       p.model,
			Messages:    messages,
			Temperature: p.temperature,
			MaxTokens:   p.maxTokens,
			Stop:        p.stopSeq,
			Tools:       aiTools,
		}

		stream, err := a.chatCompleter.ChatCompletionStream(ctx, chatReq)
		if err != nil {
			return fmt.Errorf("vagent: chat completion stream: %w", err)
		}

		var accumulated aimodel.Message
		accumulated.Role = aimodel.RoleAssistant
		var finishReason aimodel.FinishReason

		for {
			chunk, recvErr := stream.Recv()
			if errors.Is(recvErr, io.EOF) {
				break
			}
			if recvErr != nil {
				_ = stream.Close()
				return fmt.Errorf("vagent: stream recv: %w", recvErr)
			}

			if len(chunk.Choices) == 0 {
				continue
			}

			choice := chunk.Choices[0]
			delta := &choice.Delta

			// Emit text delta if present.
			if text := delta.Content.Text(); text != "" {
				if err := send(schema.NewEvent(schema.EventTextDelta, agentID, sessionID, schema.TextDeltaData{Delta: text})); err != nil {
					_ = stream.Close()
					return err
				}
			}

			accumulated.AppendDelta(delta)

			if choice.FinishReason != nil {
				finishReason = aimodel.FinishReason(*choice.FinishReason)
			}
		}

		_ = stream.Close()

		messages = append(messages, accumulated)

		if finishReason != aimodel.FinishReasonToolCalls || len(accumulated.ToolCalls) == 0 {
			return send(schema.NewEvent(schema.EventAgentEnd, agentID, sessionID, schema.AgentEndData{
				Duration: time.Since(start).Milliseconds(),
				Message:  accumulated.Content.Text(),
			}))
		}

		// Execute tool calls.
		for _, tc := range accumulated.ToolCalls {
			if err := send(schema.NewEvent(schema.EventToolCallStart, agentID, sessionID, schema.ToolCallStartData{
				ToolCallID: tc.ID,
				ToolName:   tc.Function.Name,
				Arguments:  tc.Function.Arguments,
			})); err != nil {
				return err
			}

			toolStart := time.Now()
			result := a.executeToolCall(ctx, tc)

			if err := send(schema.NewEvent(schema.EventToolCallEnd, agentID, sessionID, schema.ToolCallEndData{
				ToolCallID: tc.ID,
				ToolName:   tc.Function.Name,
				Duration:   time.Since(toolStart).Milliseconds(),
			})); err != nil {
				return err
			}

			if err := send(schema.NewEvent(schema.EventToolResult, agentID, sessionID, schema.ToolResultData{
				ToolCallID: tc.ID,
				ToolName:   tc.Function.Name,
				Result:     result,
			})); err != nil {
				return err
			}

			toolMsg := aimodel.Message{
				Role:       aimodel.RoleTool,
				ToolCallID: result.ToolCallID,
				Content:    aimodel.NewTextContent(toolResultText(result)),
			}
			messages = append(messages, toolMsg)
		}
	}

	return fmt.Errorf("vagent: exceeded max iterations (%d)", p.maxIter)
}

// toolResultText extracts the text content from a ToolResult.
func toolResultText(r schema.ToolResult) string {
	for _, p := range r.Content {
		if p.Type == "text" {
			return p.Text
		}
	}
	return ""
}
