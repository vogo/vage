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

	"github.com/vogo/vage/agent"
	"github.com/vogo/vage/checkpoint"
	vctx "github.com/vogo/vage/context"
	"github.com/vogo/vage/guard"
	"github.com/vogo/vage/hook"
	"github.com/vogo/vage/interrupt"
	"github.com/vogo/vage/largemodel"
	"github.com/vogo/vage/largemodel/middleware/contexteditor"
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
	// defaultInterruptLeaseTTL bounds how long a ResumeInterrupt call holds
	// the store lease before another attempt may reclaim it. See
	// WithInterruptLeaseTTL.
	defaultInterruptLeaseTTL = 5 * time.Minute
)

// Agent implements the agent.Agent interface using a model caller with
// ReAct-style tool calling.
type Agent struct {
	agent.Base
	systemPrompt     prompt.PromptTemplate
	model            string
	caller           largemodel.Caller
	toolRegistry     tool.ToolRegistry
	memoryManager    *memory.Manager
	maxIterations    int
	runTokenBudget   int
	maxTokens        *int
	temperature      *float64
	streamBufferSize int
	streamMiddleware []agent.StreamMiddleware
	// runMiddlewares decorate a whole Run / RunStream as a unit. See
	// WithMiddleware; Resume deliberately stays out of the chain.
	runMiddlewares   []agent.Middleware
	hookManager      *hook.Manager
	inputGuards      []guard.Guard
	outputGuards     []guard.Guard
	toolResultGuards []guard.Guard
	skillManager     skill.Manager
	// maxParallelToolCalls caps the concurrency of within-assistant-message
	// tool dispatch. 0 uses defaultMaxParallelToolCalls; values <= 1 force
	// serial execution (byte-identical to the pre-P1-7 behaviour).
	maxParallelToolCalls int
	// returnDirectTools holds tool names whose successful result ends the
	// ReAct loop immediately, returning the tool result as the final answer
	// without a further model round. nil / empty disables direct return
	// entirely. Names match model tool calls by exact string equality;
	// unregistered or request/skill-filtered names stay inert until the
	// model actually invokes them.
	returnDirectTools map[string]struct{}
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
	// interruptStore and interruptPolicy together enable the interrupt
	// choke point in runReactLoop and ResumeInterrupt. Both must be set
	// or both left nil — see checkInterruptConfig. Deliberately separate
	// from iterationStore/checkpoint: an interrupt is a pending-decision
	// state machine, not a crash-replay snapshot, see vage/interrupt.
	interruptStore  interrupt.Store
	interruptPolicy InterruptPolicy
	// interruptLeaseTTL bounds how long ResumeInterrupt holds the store
	// lease before another attempt may reclaim it. See
	// WithInterruptLeaseTTL.
	interruptLeaseTTL time.Duration
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
	// contextEditor, when non-nil, is wrapped around caller at
	// the end of New so multi-iteration ReAct loops automatically fold
	// older tool_result messages into placeholders. See WithContextEditor.
	contextEditor *contexteditor.ContextEditorMiddleware
	// paramResolver is the optional single-slot Run parameter hook.
	// See WithParamResolver; Resume paths never invoke it.
	paramResolver ParamResolver
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

// WithCaller sets the model caller the agent invokes. The caller carries the
// wire protocol, so it also determines which vendor wire form the agent's
// messages are built and stored in.
func WithCaller(c largemodel.Caller) Option {
	return func(a *Agent) { a.caller = c }
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
//
// This chain sees individual events on their way to a stream consumer and has
// no effect on Run. To apply one policy to both entry points, use
// WithMiddleware instead.
func WithStreamMiddleware(mw ...agent.StreamMiddleware) Option {
	return func(a *Agent) { a.streamMiddleware = append(a.streamMiddleware, mw...) }
}

// WithMiddleware appends agent.Middleware to the run middleware chain. Both
// Run and RunStream execute this one chain exactly once per top-level call —
// ReAct iterations, model retries and tool count do not multiply it — so a
// policy written once holds for the sync and the streaming entry point alike.
// The first registered middleware is outermost; nil entries are ignored.
//
// A middleware may short-circuit by not calling next, in which case no model
// call, tool execution or ReAct checkpoint happens. Whatever response comes
// out of the chain — passed through, rewritten or synthesised — is still the
// input to the output guards, to session memory and to AgentEnd.Message, so a
// middleware cannot route around the safety boundary. SessionID and Duration
// are stamped by the framework afterwards and cannot be forged. Returning
// (nil, nil) is a contract violation and surfaces as
// ErrNilMiddlewareResponse.
//
// The request context is already built when the chain runs (RunStream builds
// it up front so build errors surface synchronously), so rewriting
// req.Messages inside a middleware does not retroactively change the messages
// the model sees — use input guards or vctx sources for that.
//
// Resume does not run this chain: it continues a run that already passed
// through it.
func WithMiddleware(mw ...agent.Middleware) Option {
	return func(a *Agent) {
		for _, m := range mw {
			if m == nil {
				continue
			}

			a.runMiddlewares = append(a.runMiddlewares, m)
		}
	}
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
// Prefer WithGuards(GuardsConfig{...}) for the grouped form.
func WithInputGuards(guards ...guard.Guard) Option {
	return func(a *Agent) { a.inputGuards = guards }
}

// WithOutputGuards sets guards to check agent output before returning to the user.
// Prefer WithGuards(GuardsConfig{...}) for the grouped form.
func WithOutputGuards(guards ...guard.Guard) Option {
	return func(a *Agent) { a.outputGuards = guards }
}

// WithToolResultGuards sets guards that scan each tool result before it is
// appended to the model message queue. Guards see messages with
// Direction == DirectionToolResult and Metadata carrying tool_call_id /
// tool_name. If no guards are configured the scan is skipped entirely.
// Prefer WithGuards(GuardsConfig{...}) for the grouped form.
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

// WithReturnDirectTools marks the named tools as direct-return: when one of
// them executes successfully, the ReAct loop skips the next model round and
// wraps the tool result as the final assistant answer (StopReasonComplete).
//
// Names match model tool calls by exact string equality; empty names are
// ignored and repeated calls merge (idempotent). Names that are not
// registered, or that a request or skill filter removes, stay inert — nothing
// fails at construction and only a real, successful invocation can
// short-circuit. Tools not named here keep the existing ReAct behaviour, and
// the default set is empty so agents that never call this option behave
// exactly as before.
//
// Direct return only skips further model rounds: output guards, message
// memory, Agent middleware post-processing and AgentEnd still run on the
// returned text. A failed tool — handler/registry error, IsError result, or a
// tool-result guard turning it into an error — never short-circuits.
func WithReturnDirectTools(names ...string) Option {
	return func(a *Agent) {
		for _, n := range names {
			if n == "" {
				continue
			}

			if a.returnDirectTools == nil {
				a.returnDirectTools = make(map[string]struct{})
			}

			a.returnDirectTools[n] = struct{}{}
		}
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

// WithInterruptStore sets the persistence backend for suspended tool
// batches — see vage/interrupt. It is one half of the configuration
// ResumeInterrupt needs; WithInterruptPolicy is the other. Configuring
// only one of the two is a configuration error surfaced at the first
// Run/RunStream/Resume call (see checkInterruptConfig), or at construction
// time by NewValidated. Prefer WithInterrupt(InterruptConfig{...}) for the
// grouped form.
func WithInterruptStore(s interrupt.Store) Option {
	return func(a *Agent) { a.interruptStore = s }
}

// WithInterruptPolicy sets the policy that decides, for each model tool-call
// batch, which calls need an external decision before any of the batch's
// handlers run. nil (the default) disables the interrupt choke point
// entirely — every batch executes exactly as before this option existed.
// See WithInterruptToolNames for the common "flag these tool names"
// shortcut, and WithInterrupt(InterruptConfig{...}) for the grouped form.
func WithInterruptPolicy(p InterruptPolicy) Option {
	return func(a *Agent) { a.interruptPolicy = p }
}

// WithInterruptToolNames installs a convenience InterruptPolicy that flags
// every tool call in a batch whose Name exactly matches one of names — the
// common case of "pause whenever the model calls ask_user" without having
// to implement InterruptPolicy. Calling WithInterruptPolicy afterward
// replaces it; option order follows the package's normal
// "later-applied-wins" rule. Prefer WithInterrupt(InterruptConfig{...})
// for the grouped form.
func WithInterruptToolNames(names ...string) Option {
	return func(a *Agent) { a.interruptPolicy = interruptPolicyByToolName(toolNameSet(names)) }
}

// WithInterruptLeaseTTL overrides how long ResumeInterrupt holds the store
// lease (see interrupt.Store.AcquireLease) before another attempt may
// reclaim it after a crash. Defaults to defaultInterruptLeaseTTL (5
// minutes). Values <= 0 are ignored. Prefer
// WithInterrupt(InterruptConfig{...}) for the grouped form.
func WithInterruptLeaseTTL(d time.Duration) Option {
	return func(a *Agent) {
		if d > 0 {
			a.interruptLeaseTTL = d
		}
	}
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

// WithContextEditor wraps the agent's model caller with a Context
// Editing middleware so multi-iteration ReAct loops automatically
// fold older tool_result messages into short placeholders before each
// LLM request leaves the agent.
//
// Wrapping happens at New time AFTER WithCaller is resolved, so option
// order does not matter. Pass nil to leave the chain untouched. If the
// caller is itself nil at New time the option is a no-op (the agent will
// fail at first Run as before).
func WithContextEditor(mw *contexteditor.ContextEditorMiddleware) Option {
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

// New creates a new Agent with the given config and options. It never
// fails at construction: a misconfigured interrupt pair surfaces at the
// first Run/RunStream/Resume/ResumeInterrupt call. Use NewValidated when
// the assembly failure should be diagnosed earlier.
func New(cfg agent.Config, opts ...Option) *Agent {
	return newAgent(cfg, opts...)
}

// NewValidated is the construction-time-checking counterpart to New: it
// builds the same agent (same defaults, option order, protocol derivation
// and ContextEditor wrapping) and then verifies the final configuration.
// A broken interrupt pair — store without policy, policy without store, or
// both a custom policy and tool names at once — is returned here as
// ErrInterruptConfig, before any model, store or tool I/O happens, instead
// of surfacing at the first Run/RunStream/Resume/ResumeInterrupt call.
//
// New keeps its single-return signature for compatibility; reach for
// NewValidated whenever a diagnostic assembly-time failure is preferable to
// a first-run failure.
func NewValidated(cfg agent.Config, opts ...Option) (*Agent, error) {
	a := newAgent(cfg, opts...)
	if err := a.checkInterruptConfig(); err != nil {
		return nil, err
	}
	return a, nil
}

// newAgent is the single construction path shared by New and NewValidated:
// it seeds the defaults, applies every option in order, then performs the
// caller-protocol adoption and ContextEditor wrapping that must happen once
// all options have resolved. It deliberately performs no validation — that
// is NewValidated's job — so the two constructors cannot drift on defaults
// or derivation.
func newAgent(cfg agent.Config, opts ...Option) *Agent {
	a := &Agent{
		Base:                 agent.NewBase(cfg),
		maxIterations:        defaultMaxIterations,
		streamBufferSize:     agent.DefaultStreamBufferSize,
		maxParallelToolCalls: defaultMaxParallelToolCalls,
		promptCaching:        defaultPromptCaching,
		interruptLeaseTTL:    defaultInterruptLeaseTTL,
	}
	for _, o := range opts {
		o(a)
	}

	// The caller is the single source of truth for the wire protocol: it is
	// what actually reaches a vendor, and cfg.Protocol defaults to
	// openai-chat whenever it is left unset. Adopting the caller's protocol
	// here keeps a caller/config mismatch from building every message in the
	// wrong wire form and failing only at the first real call.
	if a.caller != nil {
		a.AgentProtocol = a.caller.Protocol()
	}

	// WithContextEditor is order-insensitive: wrap the caller at the
	// innermost layer once all options have resolved. A nil caller means
	// the agent will fail at first Run as before — wrapping nil would just
	// defer the same failure.
	if a.contextEditor != nil && a.caller != nil {
		a.caller = largemodel.Chain(a.caller, a.contextEditor)
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
	model           string
	temperature     *float64
	maxIter         int
	runTokenBudget  int
	maxTokens       *int
	toolMode        string
	toolFilter      []string
	stopSeq         []string
	subject         string
	enabledFunc     ToolEnabledFunc
	toolsFrozen     bool
	resolverTouched bool
}

// buildResult holds the output of buildInitialMessages.
type buildResult struct {
	messages        []schema.Message
	sessionMsgCount int // original session message count (pre-compression), used as key offset
}

// runContext holds shared state for a single Run/RunStream invocation,
// reducing the number of parameters passed between methods.
type runContext struct {
	sessionID  string
	start      time.Time
	tracker    *budgetTracker
	totalUsage schema.Usage
	br         buildResult
	reqMsgs    []schema.Message
	lastMsg    schema.Message
	iteration  int
	estimated  bool // true if token tracking is based on heuristic estimation

	// reactRan reports whether the shared ReAct loop actually executed, and
	// stopReason is the terminator it reached. They stay separate from
	// RunResponse.StopReason because an agent middleware may rewrite what the
	// response reports — or short-circuit the loop entirely — while guard
	// strictness and the token-budget event must keep describing what really
	// happened.
	reactRan   bool
	stopReason schema.StopReason

	// interruptDesc is set by maybeInterrupt when stopReason ==
	// StopReasonInterrupted; draftResponse is the only reader.
	interruptDesc *schema.InterruptDescriptor
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
func (a *Agent) buildInitialMessages(ctx context.Context, req *schema.RunRequest, budget int) (buildResult, error) {
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
	builderOpts = append(
		builderOpts,
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
		Protocol:  a.Protocol(),
		Budget:    budget,
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
//
// The ReAct execution and its draft response sit inside the agent middleware
// chain (WithMiddleware) — the same chain RunStream drives — so a run-level
// policy behaves identically on both entry points. Output guards, memory
// persistence and AgentEnd run after the chain, on whatever response it
// produced.
func (a *Agent) Run(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
	policy := policyFreshRun

	ctx = bindRunValues(ctx, policy)
	ctx = schema.WithEventDispatcher(ctx, a.hookManager.Dispatch)
	ctx = schema.WithSessionID(ctx, req.SessionID)

	p, err := a.preflightEntry(ctx, policy, req)
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

	react := func(ctx context.Context, _ *schema.RunRequest) (*schema.RunResponse, error) {
		mode := &syncMode{a: a, ctx: ctx}

		stopReason, err := a.runReactLoop(ctx, rc, p, br.messages, aiTools, mode, 0)
		if err != nil {
			return nil, err
		}

		rc.reactRan = true
		rc.stopReason = stopReason

		return a.draftResponse(rc), nil
	}

	var resp *schema.RunResponse

	if policy.agentMiddleware {
		resp, err = a.runMiddlewareChain(ctx, req, react)
	} else {
		resp, err = react(ctx, req)
	}

	if err != nil {
		return nil, err
	}

	return a.finalizeRun(ctx, rc, resp), nil
}

// runMiddlewareChain drives react through the configured agent middleware
// chain and enforces the one invariant every entry point depends on: a
// middleware that reports no error must hand back a response, because the
// terminal path has nothing to guard, store or announce otherwise.
func (a *Agent) runMiddlewareChain(
	ctx context.Context,
	req *schema.RunRequest,
	react agent.RunFunc,
) (*schema.RunResponse, error) {
	resp, err := agent.ChainMiddleware(react, a.runMiddlewares...)(ctx, req)
	if err != nil {
		return nil, err
	}

	if resp == nil {
		return nil, ErrNilMiddlewareResponse
	}

	return resp, nil
}

// RunStream returns a RunStream that emits events as the ReAct loop executes.
//
// The shared preparation and iteration skeleton live in loop.go; RunStream
// builds the context up front (so build errors surface synchronously) and then
// runs the stream execution mode inside the stream body, where AgentStart is
// the first event sent through the stream middleware + hook pipeline.
//
// The agent middleware chain (WithMiddleware) wraps the ReAct execution inside
// the stream body, so it runs exactly once per stream and sees the same draft
// response Run sees. Text deltas already sent are never replayed: a terminal
// rewrite shows up in AgentEnd and in the persisted messages only.
func (a *Agent) RunStream(ctx context.Context, req *schema.RunRequest) (*schema.RunStream, error) {
	policy := policyFreshRun

	ctx = bindRunValues(ctx, policy)
	ctx = schema.WithEventDispatcher(ctx, a.hookManager.Dispatch)

	p, err := a.preflightEntry(ctx, policy, req)
	if err != nil {
		return nil, err
	}

	br, aiTools, err := a.prepareContext(ctx, req, p)
	if err != nil {
		return nil, err
	}

	return schema.NewRunStream(ctx, a.streamBufferSize, func(ctx context.Context, rawSend func(schema.Event) error) error {
		send := a.buildSend(ctx, rawSend)
		ctx = schema.WithEmitter(ctx, send)
		ctx = schema.WithSessionID(ctx, req.SessionID)

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

		react := func(ctx context.Context, _ *schema.RunRequest) (*schema.RunResponse, error) {
			mode := &streamMode{a: a, ctx: ctx, agentID: a.ID(), send: send}

			stopReason, err := a.runReactLoop(ctx, rc, p, br.messages, aiTools, mode, 0)
			if err != nil {
				return nil, err
			}

			rc.reactRan = true
			rc.stopReason = stopReason

			return a.draftResponse(rc), nil
		}

		var resp *schema.RunResponse

		if policy.agentMiddleware {
			resp, err = a.runMiddlewareChain(ctx, req, react)
		} else {
			resp, err = react(ctx, req)
		}

		if err != nil {
			return err
		}

		return a.finalizeStream(ctx, send, rc, resp)
	}), nil
}
