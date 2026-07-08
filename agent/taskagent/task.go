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
	"fmt"
	"time"

	"github.com/vogo/aimodel"
	"github.com/vogo/vage/agent"
	"github.com/vogo/vage/checkpoint"
	vctx "github.com/vogo/vage/context"
	"github.com/vogo/vage/guard"
	"github.com/vogo/vage/hook"
	"github.com/vogo/vage/largemodel"
	"github.com/vogo/vage/memory"
	"github.com/vogo/vage/prompt"
	"github.com/vogo/vage/schema"
	"github.com/vogo/vage/skill"
	"github.com/vogo/vage/tool"
)

const (
	defaultMaxIterations        = 10
	defaultMaxParallelToolCalls = 4
	defaultPromptCaching        = true
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
	// promptCaching, when true, marks the system prompt and the last tool
	// definition with cache breakpoints so Anthropic prompt caching kicks
	// in on the repeat ReAct iterations. No on-wire effect for OpenAI.
	promptCaching bool
	// extraSources are vctx.Source plug-ins inserted into the ContextBuilder
	// pipeline AFTER SessionMemorySource and BEFORE RequestMessagesSource.
	// Used to inject cross-cutting context like the Plan Workspace, vector
	// recall, or session tree without rewriting the whole Builder.
	extraSources []vctx.Source
	// iterationStore persists per-iteration ReAct snapshots so a Run can
	// be resumed across crashes. nil disables checkpointing entirely.
	iterationStore checkpoint.IterationStore
	// checkpointFailureCB, when non-nil, runs after a non-fatal save
	// failure on iterationStore. Used to feed observability counters
	// (e.g., session.SessionMetrics.CheckpointSaveFailures) without
	// dragging metrics types into vage/agent/taskagent. The callback
	// must not block — it is invoked inline on the ReAct hot path.
	checkpointFailureCB CheckpointFailureCallback
	// buildReportSink, when non-nil, persists the per-turn BuildReport
	// produced by the internal vctx.DefaultBuilder. Forwarded as
	// vctx.WithBuildReportSink so callers do not have to replace the
	// whole Builder to get the report archive.
	buildReportSink vctx.BuildReportSink
	// contextEditor, when non-nil, is wrapped around chatCompleter at
	// the end of New so multi-iteration ReAct loops automatically fold
	// older tool_result messages into placeholders. See WithContextEditor.
	contextEditor *largemodel.ContextEditorMiddleware
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

// WithPromptCaching enables or disables emission of prompt-cache
// boundary hints on the system message and the last tool definition.
// Default true. Has no on-wire effect for OpenAI-compatible backends —
// OpenAI caches identical prefixes automatically with no request-side
// marker.
func WithPromptCaching(on bool) Option {
	return func(a *Agent) { a.promptCaching = on }
}

// WithIterationStore enables per-iteration checkpointing for Run /
// RunStream and is the prerequisite for Resume. When nil (the default)
// no checkpoints are written and Resume returns
// checkpoint.ErrInvalidArgument.
func WithIterationStore(s checkpoint.IterationStore) Option {
	return func(a *Agent) { a.iterationStore = s }
}

// CheckpointFailureCallback is invoked after a non-fatal
// IterationStore.Save failure. The agent has already logged the error
// at slog.Warn level; the callback exists so observability layers can
// turn the failure into a counter (e.g., bumping
// session.SessionMetrics.CheckpointSaveFailures) without forcing
// vage/agent/taskagent to import session.
//
// Callbacks must not block — they execute inline on the ReAct hot
// path between iterations. Errors returned from the callback are
// dropped; the agent continues execution.
type CheckpointFailureCallback func(ctx context.Context, sessionID string, saveErr error)

// WithCheckpointFailureCallback installs the failure callback. nil
// (the default) leaves save failures observable only via slog.
func WithCheckpointFailureCallback(cb CheckpointFailureCallback) Option {
	return func(a *Agent) { a.checkpointFailureCB = cb }
}

// WithBuildReportSink wires a per-turn BuildReport archive into the
// agent's internal context Builder. When non-nil, every successful
// Build dispatches a Save(ctx, sessionID, report). nil (the default)
// preserves the existing zero-cost path; the EventContextBuilt event
// is still dispatched regardless so live observers keep working.
func WithBuildReportSink(sink vctx.BuildReportSink) Option {
	return func(a *Agent) { a.buildReportSink = sink }
}

// WithContextEditor wraps the agent's ChatCompleter with a Context
// Editing middleware so multi-iteration ReAct loops automatically
// fold older tool_result messages into short placeholders before each
// LLM request leaves the agent.
//
// Wrapping happens at New time AFTER WithChatCompleter is resolved, so
// option order does not matter. Pass nil to leave the chain untouched.
// If chatCompleter is itself nil at New time the option is a no-op
// (the agent will fail at first Run as before).
func WithContextEditor(mw *largemodel.ContextEditorMiddleware) Option {
	return func(a *Agent) { a.contextEditor = mw }
}

// WithExtraSources appends vctx.Source plug-ins to the ContextBuilder used
// by every Run / RunStream call. Extras are inserted AFTER the built-in
// SessionMemorySource and BEFORE RequestMessagesSource so the resulting
// message order is [system, session_memory, ...extras, request].
//
// Use this to plug in cross-cutting context like a Plan Workspace, a
// vector recall layer, or a session tree without rewriting the whole
// Builder. Calling the option multiple times appends; nil sources are
// ignored.
//
// Equivalent to vage/context's "WithSource", but at the TaskAgent layer
// rather than the Builder layer — convenient because TaskAgent owns its
// Builder construction internally.
func WithExtraSources(srcs ...vctx.Source) Option {
	return func(a *Agent) {
		for _, s := range srcs {
			if s == nil {
				continue
			}
			a.extraSources = append(a.extraSources, s)
		}
	}
}

// New creates a new Agent with the given config and options.
func New(cfg agent.Config, opts ...Option) *Agent {
	a := &Agent{
		Base:                 agent.NewBase(cfg),
		maxIterations:        defaultMaxIterations,
		streamBufferSize:     agent.DefaultStreamBufferSize,
		maxParallelToolCalls: defaultMaxParallelToolCalls,
		promptCaching:        defaultPromptCaching,
	}
	for _, o := range opts {
		o(a)
	}

	// WithContextEditor is order-insensitive: wrap chatCompleter at the
	// innermost layer once all options have resolved. nil chatCompleter
	// means the agent will fail at first Run as before — wrapping nil
	// would just defer the same failure.
	if a.contextEditor != nil && a.chatCompleter != nil {
		a.chatCompleter = largemodel.Chain(a.chatCompleter, a.contextEditor)
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

// buildInitialMessages assembles the message list sent to the LLM via a
// vctx.DefaultBuilder configured with the built-in sources
// SystemPromptSource → SessionMemorySource → ...extras → RequestMessagesSource.
// When no extras are configured the message order matches the previous
// hand-rolled assembly ([system, session history, request]) byte-for-byte;
// extras (configured via WithExtraSources, e.g. a Plan Workspace) slot in
// just before the current-turn request so cross-cutting context lands as
// late context rather than as part of recallable memory.
//
// sessionMsgCount in the returned buildResult is read from the
// SessionMemorySource report so storeAndPromoteMessages can offset its
// indices past existing entries.
func (a *Agent) buildInitialMessages(ctx context.Context, req *schema.RunRequest) (buildResult, error) {
	// Source order: [system, session_memory, ...extras, request].
	// Extras (like WorkspaceSource) sit between session history and the
	// current-turn request so the LLM reads "what we had before" → "what we
	// know about this task across runs" → "what the user is asking now".
	builderOpts := []vctx.Option{
		vctx.WithSource(&vctx.SystemPromptSource{Template: a.systemPrompt}),
		vctx.WithSource(&vctx.SessionMemorySource{Manager: a.memoryManager}),
	}
	for _, s := range a.extraSources {
		builderOpts = append(builderOpts, vctx.WithSource(s))
	}
	builderOpts = append(builderOpts,
		vctx.WithSource(&vctx.RequestMessagesSource{}),
		vctx.WithHookManager(a.hookManager),
	)
	if a.buildReportSink != nil {
		builderOpts = append(builderOpts, vctx.WithBuildReportSink(a.buildReportSink))
	}
	builder := vctx.NewDefaultBuilder(builderOpts...)

	res, err := builder.Build(ctx, vctx.BuildInput{
		SessionID: req.SessionID,
		AgentID:   a.ID(),
		Intent:    "react-iter",
		Request:   req,
	})
	if err != nil {
		return buildResult{}, fmt.Errorf("vage: build context: %w", err)
	}

	return buildResult{
		messages:        res.Messages,
		sessionMsgCount: sessionMsgCountFromReport(res.Report),
	}, nil
}

// sessionMsgCountFromReport extracts the pre-compression message count
// the SessionMemorySource recorded so taskagent can offset newly stored
// message keys past existing entries.
func sessionMsgCountFromReport(r vctx.BuildReport) int {
	for _, s := range r.Sources {
		if s.Source == vctx.SourceNameSessionMemory {
			return s.OriginalCount
		}
	}
	return 0
}

// dispatch sends an event to the hook manager if configured.
func (a *Agent) dispatch(ctx context.Context, event schema.Event) {
	a.hookManager.Dispatch(ctx, event)
}

// Run executes the ReAct loop: prompt -> LLM -> tool calls (loop) -> response.
//
// The shared preparation and iteration skeleton live in loop.go; Run only
// wires the sync execution mode and the synchronous finalize path. AgentStart
// is dispatched before prepareContext so its EventContextBuilt still follows
// AgentStart, matching the historical non-streaming event order.
func (a *Agent) Run(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
	p, err := a.preflightRun(ctx, req)
	if err != nil {
		return nil, err
	}

	rc := &runContext{
		sessionID: req.SessionID,
		start:     time.Now(),
		tracker:   newBudgetTracker(p.runTokenBudget),
		br:        buildResult{},
		reqMsgs:   req.Messages,
	}

	a.dispatch(ctx, schema.NewEvent(schema.EventAgentStart, a.ID(), rc.sessionID, schema.AgentStartData{}))

	br, aiTools, err := a.prepareContext(ctx, req, p)
	if err != nil {
		return nil, err
	}
	rc.br = br

	mode := &syncMode{a: a, ctx: ctx}
	stopReason, err := a.runReactLoop(ctx, rc, p, br.messages, aiTools, mode, 0)
	if err != nil {
		return nil, err
	}

	return a.finalizeRun(ctx, rc, stopReason), nil
}

// RunStream returns a RunStream that emits events as the ReAct loop executes.
//
// The shared preparation and iteration skeleton live in loop.go; RunStream
// builds the context up front (so build errors surface synchronously) and then
// runs the stream execution mode inside the stream body, where AgentStart is
// the first event sent through the middleware+hook pipeline.
func (a *Agent) RunStream(ctx context.Context, req *schema.RunRequest) (*schema.RunStream, error) {
	p, err := a.preflightRun(ctx, req)
	if err != nil {
		return nil, err
	}

	br, aiTools, err := a.prepareContext(ctx, req, p)
	if err != nil {
		return nil, err
	}

	return schema.NewRunStream(ctx, a.streamBufferSize, func(ctx context.Context, rawSend func(schema.Event) error) error {
		send := a.buildSend(ctx, rawSend)

		rc := &runContext{
			sessionID: req.SessionID,
			start:     time.Now(),
			tracker:   newBudgetTracker(p.runTokenBudget),
			br:        br,
			reqMsgs:   req.Messages,
			estimated: true, // streaming path uses heuristic token estimation
		}

		if err := send(schema.NewEvent(schema.EventAgentStart, a.ID(), rc.sessionID, schema.AgentStartData{})); err != nil {
			return err
		}

		mode := &streamMode{a: a, ctx: ctx, agentID: a.ID(), send: send}
		stopReason, err := a.runReactLoop(ctx, rc, p, br.messages, aiTools, mode, 0)
		if err != nil {
			return err
		}

		return a.finalizeStream(ctx, send, rc, req, stopReason)
	}), nil
}
