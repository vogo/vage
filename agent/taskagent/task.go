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

package taskagent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/vogo/aimodel"
	"github.com/vogo/vage/agent"
	"github.com/vogo/vage/guard"
	"github.com/vogo/vage/hook"
	"github.com/vogo/vage/memory"
	"github.com/vogo/vage/prompt"
	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/skill"
	"github.com/vogo/vage/tool"
)

const (
	defaultMaxIterations        = 10
	defaultMaxParallelToolCalls = 4
)

// Agent implements the agent.Agent interface using a ChatCompleter with ReAct-style tool calling.
type Agent struct {
	agent.Base
	systemPrompt     prompt.PromptTemplate
	model            string
	chatCompleter    aimodel.ChatCompleter
	toolRegistry     tool.ToolRegistry
	memoryManager    *memory.Manager
	maxIterations    int
	runTokenBudget   int
	maxTokens        *int
	temperature      *float64
	streamBufferSize int
	middlewares      []agent.StreamMiddleware
	hookManager      *hook.Manager
	inputGuards      []guard.Guard
	outputGuards     []guard.Guard
	toolResultGuards []guard.Guard
	skillManager     skill.Manager
	// maxParallelToolCalls caps the concurrency of within-assistant-message
	// tool dispatch. 0 uses defaultMaxParallelToolCalls; values <= 1 force
	// serial execution (byte-identical to the pre-P1-7 behaviour).
	maxParallelToolCalls int
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

// WithRunTokenBudget sets the total token budget for a single run.
// A value of 0 means unlimited (default).
func WithRunTokenBudget(n int) Option { return func(a *Agent) { a.runTokenBudget = n } }

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

// WithInputGuards sets guards to check user input before agent processing.
func WithInputGuards(guards ...guard.Guard) Option {
	return func(a *Agent) { a.inputGuards = guards }
}

// WithOutputGuards sets guards to check agent output before returning to the user.
func WithOutputGuards(guards ...guard.Guard) Option {
	return func(a *Agent) { a.outputGuards = guards }
}

// WithToolResultGuards sets guards that scan each tool result before it is
// appended to the model message queue. Guards see messages with
// Direction == DirectionToolResult and Metadata carrying tool_call_id /
// tool_name. If no guards are configured the scan is skipped entirely.
func WithToolResultGuards(guards ...guard.Guard) Option {
	return func(a *Agent) { a.toolResultGuards = guards }
}

// WithSkillManager sets the skill manager for prompt injection and tool filtering.
func WithSkillManager(m skill.Manager) Option {
	return func(a *Agent) { a.skillManager = m }
}

// WithMaxParallelToolCalls caps concurrent tool dispatch within a single
// assistant message. A value <= 1 forces serial execution (pre-P1-7
// behaviour); values >= 2 fan out execution under a semaphore. If the
// option is never set, the agent uses defaultMaxParallelToolCalls.
func WithMaxParallelToolCalls(n int) Option {
	return func(a *Agent) {
		if n < 0 {
			n = 0
		}
		a.maxParallelToolCalls = n
	}
}

// New creates a new Agent with the given config and options.
func New(cfg agent.Config, opts ...Option) *Agent {
	a := &Agent{
		Base:                 agent.NewBase(cfg),
		maxIterations:        defaultMaxIterations,
		streamBufferSize:     agent.DefaultStreamBufferSize,
		maxParallelToolCalls: defaultMaxParallelToolCalls,
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
	model          string
	temperature    *float64
	maxIter        int
	runTokenBudget int
	maxTokens      *int
	toolFilter     []string
	stopSeq        []string
}

// resolveRunParams merges request options with agent defaults.
func (a *Agent) resolveRunParams(opts *schema.RunOptions) runParams {
	p := runParams{
		model:          a.model,
		temperature:    a.temperature,
		maxIter:        a.maxIterations,
		runTokenBudget: a.runTokenBudget,
		maxTokens:      a.maxTokens,
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
	if opts.RunTokenBudget > 0 {
		p.runTokenBudget = opts.RunTokenBudget
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

// runContext holds shared state for a single Run/RunStream invocation,
// reducing the number of parameters passed between methods.
type runContext struct {
	sessionID  string
	start      time.Time
	tracker    *budgetTracker
	totalUsage aimodel.Usage
	br         buildResult
	reqMsgs    []schema.Message
	lastMsg    aimodel.Message
	iteration  int
	estimated  bool // true if token tracking is based on heuristic estimation
}

// buildInitialMessages builds the message list starting with the system prompt,
// followed by session history (if memory is configured), then request messages.
func (a *Agent) buildInitialMessages(ctx context.Context, reqMsgs []schema.Message) (buildResult, error) {
	sessionMsgs, sessionMsgCount := a.loadAndCompressSessionHistory(ctx)

	messages := make([]aimodel.Message, 0, 1+len(sessionMsgs)+len(reqMsgs))

	if a.systemPrompt != nil {
		sysText, err := a.systemPrompt.Render(ctx, nil)
		if err != nil {
			return buildResult{}, fmt.Errorf("vage: render system prompt: %w", err)
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
		slog.Warn("vage: load session messages", "error", err)
		return nil, 0
	}

	originalCount := len(loaded)

	if c := a.memoryManager.Compressor(); c != nil && len(loaded) > 0 {
		compressed, compErr := c.Compress(ctx, loaded, 0)
		if compErr != nil {
			slog.Warn("vage: compress session messages", "error", compErr)
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
			slog.Warn("vage: unexpected entry type in session", "key", e.Key, "type", fmt.Sprintf("%T", e.Value))
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

// mergeSkillToolFilter merges skill AllowedTools with the request-level tool filter.
// If any active skill does not declare AllowedTools (meaning it has no restriction),
// the result is the requestFilter as-is (no additional filtering).
// Only when ALL active skills that declare AllowedTools is the union used as a filter.
func (a *Agent) mergeSkillToolFilter(requestFilter []string, sessionID string) []string {
	if a.skillManager == nil {
		return requestFilter
	}

	active := a.skillManager.ActiveSkills(sessionID)
	if len(active) == 0 {
		return requestFilter
	}

	// Collect union of all skill allowed tools.
	// If any active skill does NOT declare AllowedTools, it means "unrestricted",
	// so we skip skill-level filtering entirely.
	var skillTools []string
	seen := make(map[string]bool)

	for _, act := range active {
		def := act.SkillDef()
		if len(def.AllowedTools) == 0 {
			// This skill has no tool restriction — don't filter.
			return requestFilter
		}
		for _, t := range def.AllowedTools {
			if !seen[t] {
				seen[t] = true
				skillTools = append(skillTools, t)
			}
		}
	}

	// If no request filter, use skill tools only.
	if len(requestFilter) == 0 {
		return skillTools
	}

	// Intersect skill tools with request filter.
	reqSet := make(map[string]bool, len(requestFilter))
	for _, t := range requestFilter {
		reqSet[t] = true
	}

	var result []string
	for _, t := range skillTools {
		if reqSet[t] {
			result = append(result, t)
		}
	}

	return result
}

// injectSkillInstructions appends active skill instructions to the system prompt.
func (a *Agent) injectSkillInstructions(br *buildResult, sessionID string) {
	if a.skillManager == nil {
		return
	}

	active := a.skillManager.ActiveSkills(sessionID)
	if len(active) == 0 {
		return
	}

	var sb strings.Builder
	for _, act := range active {
		def := act.SkillDef()
		if def.Instructions == "" {
			continue
		}
		sb.WriteString("\n<skill name=\"")
		sb.WriteString(act.SkillName)
		sb.WriteString("\">\n")
		sb.WriteString(def.Instructions)
		sb.WriteString("\n</skill>")
	}

	if sb.Len() == 0 {
		return
	}

	skillText := sb.String()

	// If there is a system message, append to it; otherwise prepend a new system message.
	if len(br.messages) > 0 && br.messages[0].Role == aimodel.RoleSystem {
		existing := br.messages[0].Content.Text()
		br.messages[0].Content = aimodel.NewTextContent(existing + skillText)
	} else {
		sysMsg := aimodel.Message{
			Role:    aimodel.RoleSystem,
			Content: aimodel.NewTextContent(skillText),
		}
		br.messages = append([]aimodel.Message{sysMsg}, br.messages...)
	}
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

// runInputGuards checks user input through input guards.
// Returns the (possibly rewritten) text content, or a BlockedError.
func (a *Agent) runInputGuards(ctx context.Context, req *schema.RunRequest) error {
	if len(a.inputGuards) == 0 || len(req.Messages) == 0 {
		return nil
	}

	// Find the last user message.
	idx := -1
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == aimodel.RoleUser {
			idx = i
			break
		}
	}

	if idx < 0 {
		return nil
	}

	msg := &guard.Message{
		Direction: guard.DirectionInput,
		Content:   req.Messages[idx].Content.Text(),
		AgentID:   a.ID(),
		SessionID: req.SessionID,
		Metadata:  req.Metadata,
	}

	result, err := guard.RunGuards(ctx, msg, a.inputGuards...)
	if err != nil {
		return err
	}

	if result.Action == guard.ActionBlock {
		return &guard.BlockedError{Result: result}
	}

	if result.Action == guard.ActionRewrite {
		req.Messages[idx].Content = aimodel.NewTextContent(msg.Content)
	}

	return nil
}

// runOutputGuards checks agent output through output guards.
// Returns the (possibly rewritten) text, or a BlockedError.
func (a *Agent) runOutputGuards(ctx context.Context, sessionID string, respMsgs []schema.Message) ([]schema.Message, error) {
	if len(a.outputGuards) == 0 || len(respMsgs) == 0 {
		return respMsgs, nil
	}

	text := respMsgs[0].Content.Text()

	msg := &guard.Message{
		Direction: guard.DirectionOutput,
		Content:   text,
		AgentID:   a.ID(),
		SessionID: sessionID,
		Metadata:  nil,
	}

	result, err := guard.RunGuards(ctx, msg, a.outputGuards...)
	if err != nil {
		return nil, err
	}

	if result.Action == guard.ActionBlock {
		return nil, &guard.BlockedError{Result: result}
	}

	if result.Action == guard.ActionRewrite {
		respMsgs[0].Content = aimodel.NewTextContent(msg.Content)
	}

	return respMsgs, nil
}

// runToolResultGuards scans the text of a tool result through toolResultGuards
// and returns the (possibly rewritten or error-replaced) ToolResult along with
// an optional guard_check event for the caller to dispatch via the appropriate
// channel (hook-only in Run, stream+hook in RunStream). A nil event means the
// guard produced no material outcome.
// When no guards are configured, IsError results, or non-text content, the
// input is returned unchanged and evt is nil.
func (a *Agent) runToolResultGuards(ctx context.Context, rc *runContext, tc aimodel.ToolCall, result schema.ToolResult) (schema.ToolResult, *schema.Event) {
	if len(a.toolResultGuards) == 0 {
		return result, nil
	}

	if result.IsError {
		return result, nil
	}

	textIdx := -1
	for i, p := range result.Content {
		if p.Type == "text" {
			textIdx = i
			break
		}
	}

	if textIdx < 0 {
		return result, nil
	}

	text := result.Content[textIdx].Text
	if text == "" {
		return result, nil
	}

	msg := &guard.Message{
		Direction: guard.DirectionToolResult,
		Content:   text,
		AgentID:   a.ID(),
		SessionID: rc.sessionID,
		Metadata: map[string]any{
			guard.MetaToolCallID: tc.ID,
			guard.MetaToolName:   tc.Function.Name,
		},
	}

	gres, err := guard.RunGuards(ctx, msg, a.toolResultGuards...)
	if err != nil {
		evt := a.buildGuardCheckEvent(rc, tc, text, "tool_result_injection", "error", nil, "", err.Error())
		return schema.ErrorResult(tc.ID, "tool result guard error: "+err.Error()), &evt
	}

	switch gres.Action {
	case guard.ActionPass:
		if len(gres.Violations) == 0 {
			return result, nil
		}
		// Log-only outcome: violations surfaced but content unchanged.
		sev := a.maxToolResultSeverity(gres.Violations)
		evt := a.buildGuardCheckEvent(rc, tc, text, gres.GuardName, "log", gres.Violations, sev, "")
		return result, &evt
	case guard.ActionRewrite:
		sev := a.maxToolResultSeverity(gres.Violations)
		evt := a.buildGuardCheckEvent(rc, tc, text, gres.GuardName, "rewrite", gres.Violations, sev, gres.Reason)

		out := result
		out.Content = make([]schema.ContentPart, len(result.Content))
		copy(out.Content, result.Content)
		out.Content[textIdx].Text = gres.Content
		return out, &evt
	case guard.ActionBlock:
		sev := a.maxToolResultSeverity(gres.Violations)
		evt := a.buildGuardCheckEvent(rc, tc, text, gres.GuardName, "block", gres.Violations, sev, gres.Reason)

		reason := gres.Reason
		if reason == "" {
			reason = "tool result blocked"
		}

		return schema.ErrorResult(tc.ID, fmt.Sprintf("blocked by %s: %s", gres.GuardName, reason)), &evt
	default:
		return result, nil
	}
}

// maxToolResultSeverity returns the highest severity name among rule hits,
// or "" if none. It consults any ToolResultInjectionGuard among the
// configured tool-result guards to resolve rule → severity mapping.
func (a *Agent) maxToolResultSeverity(hits []string) string {
	var max guard.Severity

	for _, g := range a.toolResultGuards {
		tg, ok := g.(*guard.ToolResultInjectionGuard)
		if !ok {
			continue
		}

		if s := tg.MaxSeverity(hits); s > max {
			max = s
		}
	}

	if max == 0 {
		return ""
	}

	return max.String()
}

// buildGuardCheckEvent creates an EventGuardCheck event and writes a
// structured warning log as a side effect. The returned event must be
// dispatched by the caller via the channel appropriate for the execution
// mode (a.dispatch for non-streaming, send for streaming).
func (a *Agent) buildGuardCheckEvent(rc *runContext, tc aimodel.ToolCall, text, guardName, action string, hits []string, severity, reason string) schema.Event {
	const snippetMax = 200

	snippet := safeSnippet(text, snippetMax)

	attrs := []any{
		"guard", guardName,
		"tool", tc.Function.Name,
		"tool_call_id", tc.ID,
		"action", action,
		"rules", hits,
		"agent_id", a.ID(),
		"session_id", rc.sessionID,
	}
	if severity != "" {
		attrs = append(attrs, "severity", severity)
	}
	if reason != "" {
		attrs = append(attrs, "reason", reason)
	}

	slog.Warn("vage: tool result guard hit", attrs...)

	return schema.NewEvent(schema.EventGuardCheck, a.ID(), rc.sessionID, schema.GuardCheckData{
		GuardName:  guardName,
		ToolCallID: tc.ID,
		ToolName:   tc.Function.Name,
		Action:     action,
		RuleHits:   hits,
		Severity:   severity,
		Snippet:    snippet,
	})
}

// safeSnippet returns the leading bytes of s up to max, walking back to the
// nearest rune boundary so the result is always valid UTF-8. An ellipsis
// marker is appended when truncation happened.
func safeSnippet(s string, max int) string {
	if len(s) <= max {
		return s
	}

	// Walk back until we land on a rune boundary. We never walk more than 3
	// bytes because a UTF-8 codepoint is at most 4 bytes.
	end := max
	for end > 0 && end > max-4 && !utf8.RuneStart(s[end]) {
		end--
	}

	return s[:end] + "..."
}

// buildResponseMsgs builds the response message slice from the last assistant message.
// For partial results (budget/iterations), it includes messages with tool calls.
// For normal completion, it always includes the message.
func (a *Agent) buildResponseMsgs(lastMsg aimodel.Message, partial bool) []schema.Message {
	if partial {
		if lastMsg.Content.Text() != "" || len(lastMsg.ToolCalls) > 0 {
			return []schema.Message{schema.NewAssistantMessage(lastMsg, a.ID())}
		}
		return []schema.Message{}
	}
	return []schema.Message{schema.NewAssistantMessage(lastMsg, a.ID())}
}

// finalizeRun is the unified termination path for Run(). It runs output guards,
// stores messages, dispatches events, and builds the RunResponse.
func (a *Agent) finalizeRun(ctx context.Context, rc *runContext, stopReason schema.StopReason) *schema.RunResponse {
	partial := stopReason != schema.StopReasonComplete
	respMsgs := a.buildResponseMsgs(rc.lastMsg, partial)

	// Run output guards. For partial results, log warnings instead of returning errors.
	guardedMsgs, err := a.runOutputGuards(ctx, rc.sessionID, respMsgs)
	if err != nil {
		if partial {
			slog.Warn("vage: output guard on partial result", "error", err, "stop_reason", stopReason)
		}
		// For normal completion, we still use the unguarded messages rather than failing.
	} else {
		respMsgs = guardedMsgs
	}

	a.storeAndPromoteMessages(ctx, rc.sessionID, rc.reqMsgs, respMsgs, rc.br.sessionMsgCount)

	// Emit budget exhaustion event if applicable.
	if stopReason == schema.StopReasonBudgetExhausted {
		a.dispatch(ctx, schema.NewEvent(schema.EventTokenBudgetExhausted, a.ID(), rc.sessionID,
			schema.TokenBudgetExhaustedData{
				Budget:     rc.tracker.Budget(),
				Used:       rc.tracker.Consumed(),
				Iterations: rc.iteration + 1,
				Estimated:  rc.estimated,
			}))
	}

	msg := ""
	if len(respMsgs) > 0 {
		msg = respMsgs[0].Content.Text()
	}

	duration := time.Since(rc.start).Milliseconds()

	a.dispatch(ctx, schema.NewEvent(schema.EventAgentEnd, a.ID(), rc.sessionID, schema.AgentEndData{
		Duration:   duration,
		Message:    msg,
		StopReason: stopReason,
	}))

	return &schema.RunResponse{
		Messages:   respMsgs,
		SessionID:  rc.sessionID,
		Usage:      &rc.totalUsage,
		Duration:   duration,
		StopReason: stopReason,
	}
}

// finalizeStream is the unified termination path for RunStream(). It runs output guards,
// stores messages, dispatches events via send, and returns nil for clean stream close.
func (a *Agent) finalizeStream(
	ctx context.Context,
	send func(schema.Event) error,
	rc *runContext,
	req *schema.RunRequest,
	stopReason schema.StopReason,
) error {
	partial := stopReason != schema.StopReasonComplete
	respMsgs := a.buildResponseMsgs(rc.lastMsg, partial)

	// Run output guards. For partial results, log warnings instead of returning errors.
	guardedMsgs, err := a.runOutputGuards(ctx, rc.sessionID, respMsgs)
	if err != nil {
		if partial {
			slog.Warn("vage: output guard on partial stream result", "error", err, "stop_reason", stopReason)
		} else {
			return err
		}
	} else {
		respMsgs = guardedMsgs
	}

	a.storeAndPromoteMessages(ctx, rc.sessionID, req.Messages, respMsgs, rc.br.sessionMsgCount)

	// Emit budget exhaustion event if applicable.
	if stopReason == schema.StopReasonBudgetExhausted {
		if err := send(schema.NewEvent(schema.EventTokenBudgetExhausted, a.ID(), rc.sessionID,
			schema.TokenBudgetExhaustedData{
				Budget:     rc.tracker.Budget(),
				Used:       rc.tracker.Consumed(),
				Iterations: rc.iteration + 1,
				Estimated:  rc.estimated,
			})); err != nil {
			return err
		}
	}

	msg := ""
	if len(respMsgs) > 0 {
		msg = respMsgs[0].Content.Text()
	}

	return send(schema.NewEvent(schema.EventAgentEnd, a.ID(), rc.sessionID, schema.AgentEndData{
		Duration:   time.Since(rc.start).Milliseconds(),
		Message:    msg,
		StopReason: stopReason,
	}))
}

// Run executes the ReAct loop: prompt -> LLM -> tool calls (loop) -> response.
func (a *Agent) Run(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
	if a.chatCompleter == nil {
		return nil, errors.New("vage: ChatCompleter is required")
	}

	// Run input guards before processing.
	if err := a.runInputGuards(ctx, req); err != nil {
		return nil, err
	}

	p := a.resolveRunParams(req.Options)
	agentID := a.ID()

	rc := &runContext{
		sessionID: req.SessionID,
		start:     time.Now(),
		tracker:   newBudgetTracker(p.runTokenBudget),
		br:        buildResult{},
		reqMsgs:   req.Messages,
	}

	a.dispatch(ctx, schema.NewEvent(schema.EventAgentStart, agentID, rc.sessionID, schema.AgentStartData{}))

	br, err := a.buildInitialMessages(ctx, req.Messages)
	if err != nil {
		return nil, err
	}

	rc.br = br
	// Inject skill instructions into the system prompt.
	a.injectSkillInstructions(&rc.br, rc.sessionID)
	messages := rc.br.messages
	aiTools := a.prepareAITools(a.mergeSkillToolFilter(p.toolFilter, rc.sessionID))

	for iter := 0; iter < p.maxIter; iter++ {
		rc.iteration = iter

		// Pre-call budget check.
		if rc.tracker.Exhausted() {
			return a.finalizeRun(ctx, rc, schema.StopReasonBudgetExhausted), nil
		}

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
			return nil, fmt.Errorf("vage: chat completion: %w", err)
		}

		rc.totalUsage.Add(&resp.Usage)
		rc.tracker.Add(resp.Usage.TotalTokens)

		if len(resp.Choices) == 0 {
			return nil, errors.New("vage: empty response from LLM")
		}

		choice := resp.Choices[0]
		assistantMsg := choice.Message
		rc.lastMsg = assistantMsg
		messages = append(messages, assistantMsg)

		if choice.FinishReason != aimodel.FinishReasonToolCalls || len(assistantMsg.ToolCalls) == 0 {
			return a.finalizeRun(ctx, rc, schema.StopReasonComplete), nil
		}

		// Post-call budget check before executing tool calls.
		if rc.tracker.Exhausted() {
			return a.finalizeRun(ctx, rc, schema.StopReasonBudgetExhausted), nil
		}

		sink := func(ev schema.Event) error {
			a.dispatch(ctx, ev)
			return nil
		}
		toolMsgs, _ := a.executeToolBatch(ctx, rc, agentID, assistantMsg.ToolCalls, false, sink)
		messages = append(messages, toolMsgs...)
	}

	// Max iterations exceeded.
	rc.iteration = p.maxIter - 1
	return a.finalizeRun(ctx, rc, schema.StopReasonMaxIterations), nil
}

// executeToolBatch dispatches the tool calls from a single assistant message
// with bounded concurrency, emitting events and returning the tool-result
// messages in ToolCalls[i] order regardless of goroutine finish order.
//
// For 0 or 1 tool calls (or when a.maxParallelToolCalls <= 1) the path
// degenerates to the pre-P1-7 serial loop — no goroutines, no semaphore.
//
// eventSink is called serially from the calling goroutine for every event
// (Start / End / GuardCheck / Result) in the original index order. Returning
// a non-nil error halts dispatch and propagates the error up; the sync-path
// adapter always returns nil, the stream path's send() may fail.
//
// emitResultEvent controls whether EventToolResult is sent after each guard
// pass — true for the streaming path, false for the sync path (which
// appends the result message directly without a user-facing event).
func (a *Agent) executeToolBatch(
	ctx context.Context,
	rc *runContext,
	agentID string,
	toolCalls []aimodel.ToolCall,
	emitResultEvent bool,
	eventSink func(schema.Event) error,
) ([]aimodel.Message, error) {
	n := len(toolCalls)
	if n == 0 {
		return nil, nil
	}

	// Dispatch all Start events up-front in ToolCalls order so downstream
	// consumers (tests, UI) see a stable sequence regardless of how the
	// workers below complete.
	starts := make([]time.Time, n)
	for i, tc := range toolCalls {
		if err := eventSink(schema.NewEvent(schema.EventToolCallStart, agentID, rc.sessionID, schema.ToolCallStartData{
			ToolCallID: tc.ID,
			ToolName:   tc.Function.Name,
			Arguments:  tc.Function.Arguments,
		})); err != nil {
			return nil, err
		}
		starts[i] = time.Now()
	}

	results := make([]schema.ToolResult, n)
	durations := make([]time.Duration, n)

	parallelCap := a.maxParallelToolCalls
	if parallelCap <= 0 {
		parallelCap = defaultMaxParallelToolCalls
	}

	if n == 1 || parallelCap <= 1 {
		for i, tc := range toolCalls {
			results[i] = a.executeToolCall(ctx, tc)
			durations[i] = time.Since(starts[i])
		}
	} else {
		if parallelCap > n {
			parallelCap = n
		}
		sem := make(chan struct{}, parallelCap)
		var wg sync.WaitGroup
		for i, tc := range toolCalls {
			wg.Add(1)
			sem <- struct{}{}
			go func(i int, tc aimodel.ToolCall) {
				defer wg.Done()
				defer func() { <-sem }()
				results[i] = a.executeToolCall(ctx, tc)
				durations[i] = time.Since(starts[i])
			}(i, tc)
		}
		wg.Wait()
	}

	// Dispatch End / Guard / (optional) Result events + build tool messages
	// in ToolCalls order — matches pre-P1-7 observable sequence.
	toolMsgs := make([]aimodel.Message, 0, n)
	for i, tc := range toolCalls {
		if err := eventSink(schema.NewEvent(schema.EventToolCallEnd, agentID, rc.sessionID, schema.ToolCallEndData{
			ToolCallID: tc.ID,
			ToolName:   tc.Function.Name,
			Duration:   durations[i].Milliseconds(),
		})); err != nil {
			return nil, err
		}

		res, guardEvt := a.runToolResultGuards(ctx, rc, tc, results[i])
		if guardEvt != nil {
			if err := eventSink(*guardEvt); err != nil {
				return nil, err
			}
		}

		if emitResultEvent {
			if err := eventSink(schema.NewEvent(schema.EventToolResult, agentID, rc.sessionID, schema.ToolResultData{
				ToolCallID: tc.ID,
				ToolName:   tc.Function.Name,
				Result:     res,
			})); err != nil {
				return nil, err
			}
		}

		toolMsgs = append(toolMsgs, aimodel.Message{
			Role:       aimodel.RoleTool,
			ToolCallID: res.ToolCallID,
			Content:    aimodel.NewTextContent(toolResultText(res)),
		})
	}

	return toolMsgs, nil
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
			slog.Warn("vage: store request message", "error", err)
		}

		idx++
	}

	for _, msg := range respMsgs {
		key := fmt.Sprintf("msg:%06d", idx)
		if err := working.Set(ctx, key, msg, 0); err != nil {
			slog.Warn("vage: store response message", "error", err)
		}

		idx++
	}

	if err := a.memoryManager.PromoteToSession(ctx, working); err != nil {
		slog.Warn("vage: promote to session", "error", err)
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
		// Skip hook dispatch for LLM lifecycle events — MetricsMiddleware
		// already dispatches these directly to hooks to avoid double-counting.
		if e.Type != schema.EventLLMCallEnd {
			a.hookManager.Dispatch(ctx, e)
		}

		return next(e)
	}

	return send
}

// RunStream returns a RunStream that emits events as the ReAct loop executes.
func (a *Agent) RunStream(ctx context.Context, req *schema.RunRequest) (*schema.RunStream, error) {
	if a.chatCompleter == nil {
		return nil, errors.New("vage: ChatCompleter is required")
	}

	// Run input guards before processing.
	if err := a.runInputGuards(ctx, req); err != nil {
		return nil, err
	}

	p := a.resolveRunParams(req.Options)

	br, err := a.buildInitialMessages(ctx, req.Messages)
	if err != nil {
		return nil, err
	}

	// Inject skill instructions into the system prompt.
	a.injectSkillInstructions(&br, req.SessionID)

	aiTools := a.prepareAITools(a.mergeSkillToolFilter(p.toolFilter, req.SessionID))

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
	agentID := a.ID()

	rc := &runContext{
		sessionID: req.SessionID,
		start:     time.Now(),
		tracker:   newBudgetTracker(p.runTokenBudget),
		br:        br,
		reqMsgs:   req.Messages,
		estimated: true, // streaming path uses heuristic token estimation
	}

	if err := send(schema.NewEvent(schema.EventAgentStart, agentID, rc.sessionID, schema.AgentStartData{})); err != nil {
		return err
	}

	for iter := 0; iter < p.maxIter; iter++ {
		rc.iteration = iter

		// Pre-call budget check.
		if rc.tracker.Exhausted() {
			return a.finalizeStream(ctx, send, rc, req, schema.StopReasonBudgetExhausted)
		}

		// Emit iteration start event.
		if err := send(schema.NewEvent(schema.EventIterationStart, agentID, rc.sessionID, schema.IterationStartData{
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
			return fmt.Errorf("vage: chat completion stream: %w", err)
		}

		var accumulated aimodel.Message
		accumulated.Role = aimodel.RoleAssistant
		var finishReason aimodel.FinishReason
		var streamBytes int

		for {
			chunk, recvErr := stream.Recv()
			if errors.Is(recvErr, io.EOF) {
				break
			}
			if recvErr != nil {
				_ = stream.Close()
				return fmt.Errorf("vage: stream recv: %w", recvErr)
			}

			if len(chunk.Choices) == 0 {
				continue
			}

			choice := chunk.Choices[0]
			delta := &choice.Delta

			// Emit text delta if present.
			if text := delta.Content.Text(); text != "" {
				streamBytes += len(text)
				if err := send(schema.NewEvent(schema.EventTextDelta, agentID, rc.sessionID, schema.TextDeltaData{Delta: text})); err != nil {
					_ = stream.Close()
					return err
				}
			}

			accumulated.AppendDelta(delta)

			if choice.FinishReason != nil {
				finishReason = aimodel.FinishReason(*choice.FinishReason)
			}
		}

		// Read actual usage from stream before closing (populated from final chunk).
		streamUsage := stream.Usage()

		_ = stream.Close()

		if streamUsage != nil {
			rc.totalUsage.Add(streamUsage)
			rc.tracker.Add(streamUsage.TotalTokens)

			// Emit through the stream pipeline so downstream consumers
			// (CLI, phase trackers) can observe token usage.
			_ = send(schema.NewEvent(schema.EventLLMCallEnd, agentID, rc.sessionID, schema.LLMCallEndData{
				Model:            chatReq.Model,
				PromptTokens:     streamUsage.PromptTokens,
				CompletionTokens: streamUsage.CompletionTokens,
				TotalTokens:      streamUsage.TotalTokens,
				CacheReadTokens:  streamUsage.CacheReadTokens,
				Stream:           true,
			}))
		} else {
			// Estimate token usage from stream bytes (4 bytes per token heuristic).
			estimatedTokens := (streamBytes + 3) / 4
			if estimatedTokens < 1 && streamBytes > 0 {
				estimatedTokens = 1
			}

			rc.tracker.Add(estimatedTokens)
		}

		rc.lastMsg = accumulated
		messages = append(messages, accumulated)

		if finishReason != aimodel.FinishReasonToolCalls || len(accumulated.ToolCalls) == 0 {
			return a.finalizeStream(ctx, send, rc, req, schema.StopReasonComplete)
		}

		// Post-call budget check before executing tool calls.
		if rc.tracker.Exhausted() {
			return a.finalizeStream(ctx, send, rc, req, schema.StopReasonBudgetExhausted)
		}

		// Execute tool calls with bounded concurrency; events and messages
		// emerge in ToolCalls order.
		toolMsgs, err := a.executeToolBatch(ctx, rc, agentID, accumulated.ToolCalls, true, send)
		if err != nil {
			return err
		}
		messages = append(messages, toolMsgs...)
	}

	// Max iterations exceeded.
	rc.iteration = p.maxIter - 1
	return a.finalizeStream(ctx, send, rc, req, schema.StopReasonMaxIterations)
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
