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

package llmagent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/vogo/aimodel"
	"github.com/vogo/vagent/agent"
	"github.com/vogo/vagent/hook"
	"github.com/vogo/vagent/memory"
	"github.com/vogo/vagent/prompt"
	"github.com/vogo/vagent/schema"
	"github.com/vogo/vagent/tool"
)

const defaultMaxIterations = 10

// Agent implements the agent.Agent interface using a ChatCompleter with ReAct-style tool calling.
type Agent struct {
	agent.Base
	systemPrompt     prompt.PromptTemplate
	model            string
	chatCompleter    aimodel.ChatCompleter
	toolRegistry     tool.ToolRegistry
	memoryManager    *memory.Manager
	maxIterations    int
	maxTokens        *int
	temperature      *float64
	streamBufferSize int
	middlewares      []agent.StreamMiddleware
	hookManager      *hook.Manager
}

var (
	_ agent.Agent       = (*Agent)(nil)
	_ agent.StreamAgent = (*Agent)(nil)
)

// Option configures LLM-specific fields of an Agent.
type Option func(*Agent)

// WithSystemPrompt sets the system prompt template.
func WithSystemPrompt(p prompt.PromptTemplate) Option {
	return func(a *Agent) { a.systemPrompt = p }
}

// WithModel sets the model name.
func WithModel(model string) Option { return func(a *Agent) { a.model = model } }

// WithChatCompleter sets the chat completion provider.
func WithChatCompleter(cc aimodel.ChatCompleter) Option {
	return func(a *Agent) { a.chatCompleter = cc }
}

// WithToolRegistry sets the tool registry.
func WithToolRegistry(r tool.ToolRegistry) Option {
	return func(a *Agent) { a.toolRegistry = r }
}

// WithMaxIterations sets the maximum ReAct loop iterations.
func WithMaxIterations(n int) Option { return func(a *Agent) { a.maxIterations = n } }

// WithMaxTokens sets the max tokens for LLM responses.
func WithMaxTokens(n int) Option { return func(a *Agent) { a.maxTokens = &n } }

// WithTemperature sets the sampling temperature.
func WithTemperature(t float64) Option { return func(a *Agent) { a.temperature = &t } }

// WithStreamBufferSize sets the channel buffer size for streaming events.
func WithStreamBufferSize(n int) Option {
	return func(a *Agent) { a.streamBufferSize = n }
}

// WithStreamMiddleware appends one or more middleware to the stream processing chain.
func WithStreamMiddleware(mw ...agent.StreamMiddleware) Option {
	return func(a *Agent) { a.middlewares = append(a.middlewares, mw...) }
}

// WithMemory sets the memory manager for multi-turn conversation support.
func WithMemory(m *memory.Manager) Option {
	return func(a *Agent) { a.memoryManager = m }
}

// WithHookManager sets the hook manager for event dispatch.
func WithHookManager(m *hook.Manager) Option {
	return func(a *Agent) { a.hookManager = m }
}

// New creates a new Agent with the given config and options.
func New(cfg agent.Config, opts ...Option) *Agent {
	a := &Agent{
		Base:             agent.NewBase(cfg),
		maxIterations:    defaultMaxIterations,
		streamBufferSize: agent.DefaultStreamBufferSize,
	}
	for _, o := range opts {
		o(a)
	}
	return a
}

// Tools returns the tool definitions from the registry.
func (a *Agent) Tools() []schema.ToolDef {
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
func (a *Agent) resolveRunParams(opts *schema.RunOptions) runParams {
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

// buildResult holds the output of buildInitialMessages.
type buildResult struct {
	messages        []aimodel.Message
	sessionMsgCount int // original session message count (pre-compression), used as key offset
}

// buildInitialMessages builds the message list starting with the system prompt,
// followed by session history (if memory is configured), then request messages.
func (a *Agent) buildInitialMessages(ctx context.Context, reqMsgs []schema.Message) (buildResult, error) {
	sessionMsgs, sessionMsgCount := a.loadAndCompressSessionHistory(ctx)

	messages := make([]aimodel.Message, 0, 1+len(sessionMsgs)+len(reqMsgs))

	if a.systemPrompt != nil {
		sysText, err := a.systemPrompt.Render(ctx, nil)
		if err != nil {
			return buildResult{}, fmt.Errorf("vagent: render system prompt: %w", err)
		}

		if sysText != "" {
			messages = append(messages, aimodel.Message{
				Role:    aimodel.RoleSystem,
				Content: aimodel.NewTextContent(sysText),
			})
		}
	}

	messages = append(messages, schema.ToAIModelMessages(sessionMsgs)...)
	messages = append(messages, schema.ToAIModelMessages(reqMsgs)...)

	return buildResult{messages: messages, sessionMsgCount: sessionMsgCount}, nil
}

// loadAndCompressSessionHistory loads session messages, applies compression,
// and returns the (possibly compressed) messages along with the original count.
// Returns (nil, 0) if memory is not configured or on error.
func (a *Agent) loadAndCompressSessionHistory(ctx context.Context) ([]schema.Message, int) {
	if a.memoryManager == nil || a.memoryManager.Session() == nil {
		return nil, 0
	}

	loaded, err := a.loadSessionMessages(ctx)
	if err != nil {
		slog.Warn("vagent: load session messages", "error", err)
		return nil, 0
	}

	originalCount := len(loaded)

	if c := a.memoryManager.Compressor(); c != nil && len(loaded) > 0 {
		compressed, compErr := c.Compress(ctx, loaded, 0)
		if compErr != nil {
			slog.Warn("vagent: compress session messages", "error", compErr)
		} else {
			loaded = compressed
		}
	}

	return loaded, originalCount
}

// loadSessionMessages loads stored messages from session memory, sorted by key.
func (a *Agent) loadSessionMessages(ctx context.Context) ([]schema.Message, error) {
	entries, err := a.memoryManager.Session().List(ctx, "msg:")
	if err != nil {
		return nil, err
	}

	if len(entries) == 0 {
		return nil, nil
	}

	slices.SortFunc(entries, func(a, b memory.Entry) int {
		return strings.Compare(a.Key, b.Key)
	})

	msgs := make([]schema.Message, 0, len(entries))

	for _, e := range entries {
		msg, ok := e.Value.(schema.Message)
		if !ok {
			slog.Warn("vagent: unexpected entry type in session", "key", e.Key, "type", fmt.Sprintf("%T", e.Value))
			continue
		}

		msgs = append(msgs, msg)
	}

	return msgs, nil
}

// prepareAITools converts registry tools to aimodel.Tool slice, applying any filter.
func (a *Agent) prepareAITools(filter []string) []aimodel.Tool {
	if a.toolRegistry == nil {
		return nil
	}
	defs := a.toolRegistry.List()
	defs = tool.FilterTools(defs, filter)
	return tool.ToAIModelTools(defs)
}

// executeToolCall runs a single tool call and returns the result.
func (a *Agent) executeToolCall(ctx context.Context, tc aimodel.ToolCall) schema.ToolResult {
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

// dispatch sends an event to the hook manager if configured.
func (a *Agent) dispatch(ctx context.Context, event schema.Event) {
	a.hookManager.Dispatch(ctx, event)
}

// Run executes the ReAct loop: prompt -> LLM -> tool calls (loop) -> response.
func (a *Agent) Run(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
	if a.chatCompleter == nil {
		return nil, errors.New("vagent: ChatCompleter is required")
	}

	start := time.Now()
	p := a.resolveRunParams(req.Options)
	agentID := a.ID()
	sessionID := req.SessionID

	a.dispatch(ctx, schema.NewEvent(schema.EventAgentStart, agentID, sessionID, schema.AgentStartData{}))

	br, err := a.buildInitialMessages(ctx, req.Messages)
	if err != nil {
		return nil, err
	}

	messages := br.messages
	aiTools := a.prepareAITools(p.toolFilter)

	var totalUsage aimodel.Usage

	for range p.maxIter {
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
			respMsgs := []schema.Message{schema.NewAssistantMessage(assistantMsg, agentID)}
			runResp := &schema.RunResponse{
				Messages:  respMsgs,
				SessionID: sessionID,
				Usage:     &totalUsage,
				Duration:  time.Since(start).Milliseconds(),
			}

			a.storeAndPromoteMessages(ctx, sessionID, req.Messages, respMsgs, br.sessionMsgCount)

			msg := ""
			if len(respMsgs) > 0 {
				msg = respMsgs[0].Content.Text()
			}

			a.dispatch(ctx, schema.NewEvent(schema.EventAgentEnd, agentID, sessionID, schema.AgentEndData{
				Duration: runResp.Duration,
				Message:  msg,
			}))

			return runResp, nil
		}

		for _, tc := range assistantMsg.ToolCalls {
			a.dispatch(ctx, schema.NewEvent(schema.EventToolCallStart, agentID, sessionID, schema.ToolCallStartData{
				ToolCallID: tc.ID,
				ToolName:   tc.Function.Name,
				Arguments:  tc.Function.Arguments,
			}))

			toolStart := time.Now()
			result := a.executeToolCall(ctx, tc)

			a.dispatch(ctx, schema.NewEvent(schema.EventToolCallEnd, agentID, sessionID, schema.ToolCallEndData{
				ToolCallID: tc.ID,
				ToolName:   tc.Function.Name,
				Duration:   time.Since(toolStart).Milliseconds(),
			}))

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

// storeAndPromoteMessages stores request and response messages in working memory
// and promotes them to session memory. sessionMsgCount is the original session
// message count (pre-compression), used as key offset to avoid collisions.
func (a *Agent) storeAndPromoteMessages(ctx context.Context, sessionID string, reqMsgs, respMsgs []schema.Message, sessionMsgCount int) {
	if a.memoryManager == nil {
		return
	}

	working := memory.NewWorkingMemory(a.ID(), sessionID)

	idx := sessionMsgCount

	for _, msg := range reqMsgs {
		key := fmt.Sprintf("msg:%06d", idx)
		if err := working.Set(ctx, key, msg, 0); err != nil {
			slog.Warn("vagent: store request message", "error", err)
		}

		idx++
	}

	for _, msg := range respMsgs {
		key := fmt.Sprintf("msg:%06d", idx)
		if err := working.Set(ctx, key, msg, 0); err != nil {
			slog.Warn("vagent: store response message", "error", err)
		}

		idx++
	}

	if err := a.memoryManager.PromoteToSession(ctx, working); err != nil {
		slog.Warn("vagent: promote to session", "error", err)
	}
}

// buildSend builds a send function with the middleware chain and hook dispatch applied.
func (a *Agent) buildSend(ctx context.Context, raw func(schema.Event) error) func(schema.Event) error {
	send := raw
	// Apply middlewares in reverse order so the first middleware is outermost.
	for i := len(a.middlewares) - 1; i >= 0; i-- {
		send = a.middlewares[i](send)
	}

	next := send
	send = func(e schema.Event) error {
		a.hookManager.Dispatch(ctx, e)
		return next(e)
	}

	return send
}

// RunStream returns a RunStream that emits events as the ReAct loop executes.
func (a *Agent) RunStream(ctx context.Context, req *schema.RunRequest) (*schema.RunStream, error) {
	if a.chatCompleter == nil {
		return nil, errors.New("vagent: ChatCompleter is required")
	}

	p := a.resolveRunParams(req.Options)

	br, err := a.buildInitialMessages(ctx, req.Messages)
	if err != nil {
		return nil, err
	}

	aiTools := a.prepareAITools(p.toolFilter)

	return schema.NewRunStream(ctx, a.streamBufferSize, func(ctx context.Context, rawSend func(schema.Event) error) error {
		send := a.buildSend(ctx, rawSend)
		return a.runStreamLoop(ctx, req, p, br, aiTools, send)
	}), nil
}

// runStreamLoop is the streaming ReAct loop that emits events via send.
func (a *Agent) runStreamLoop(
	ctx context.Context,
	req *schema.RunRequest,
	p runParams,
	br buildResult,
	aiTools []aimodel.Tool,
	send func(schema.Event) error,
) error {
	messages := br.messages
	start := time.Now()
	agentID := a.ID()
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
			// Store messages in memory (same as Run path).
			assistantMsg := schema.NewAssistantMessage(accumulated, agentID)
			a.storeAndPromoteMessages(ctx, sessionID, req.Messages, []schema.Message{assistantMsg}, br.sessionMsgCount)

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
