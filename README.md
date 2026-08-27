# vage

[![Build](https://github.com/vogo/vage/actions/workflows/build.yml/badge.svg)](https://github.com/vogo/vage/actions/workflows/build.yml)
[![codecov](https://codecov.io/gh/vogo/vage/branch/main/graph/badge.svg)](https://codecov.io/gh/vogo/vage)


A Go framework for building LLM-based intelligent agent systems.

## Features

- **Composable Agents** — TaskAgent (ReAct tool-calling), RouterAgent (routing), WorkflowAgent (DAG orchestration), and CustomAgent (user-defined)
- **DAG Orchestration** — Parallel execution, loops, conditionals, compensation (Saga), checkpointing, backpressure, priority scheduling
- **Three-Level Memory** — Working (request) → Session (conversation) → Store (persistent), with context compression and token budgets
- **Security Guardrails** — Prompt injection, content filter, PII, topic, length, and custom guards
- **Agent Middleware** — One decorator chain shared by sync and streaming runs: short-circuit, audit, or rewrite the final response once per run (hooks stay read-only, stream middleware stays event-level)
- **LLM Middleware** — Decorator chain: logging, rate limiting, timeout, cache, metrics (retries and endpoint health live in the caller's router pool)
- **Tool System** — Local functions, MCP remote tools, agent-as-tool, built-in bash tool with process isolation
- **Agent Skills** — Compatible with the [Agent Skills](https://agentskills.io) open standard
- **MCP Protocol** — Client (consume external tools) and server (expose agent capabilities)
- **Evaluation** — ExactMatch, Contains, LLMJudge, ToolCall, Latency, Cost evaluators
- **HTTP Service** — REST endpoints for sync, streaming, and async agent execution


## Installation

```bash
go get github.com/vogo/vage
```

## Quick Start

```go
package main

import (
	"context"
	"fmt"

	"github.com/vogo/vage/agent"
	"github.com/vogo/vage/schema"
)

func main() {
	cfg := agent.Config{
		ID:          "greeter",
		Name:        "Greeter",
		Description: "A simple greeting agent",
		Protocol:    schema.ProtocolOpenAIChat,
	}

	a := agent.NewCustomAgent(cfg,
		func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
			return &schema.RunResponse{
				Messages: []schema.Message{
					schema.NewTextMessage(cfg.Protocol, schema.RoleAssistant, "Hello! How can I help you?"),
				},
			}, nil
		},
	)

	resp, err := agent.RunText(context.Background(), a, "Hi")
	if err != nil {
		panic(err)
	}

	fmt.Println(resp.Messages[0].Text())
}
```

## Connecting a Model

vage speaks each vendor's native protocol directly. A model endpoint is a
`largemodel.Caller` bound to one protocol at construction time, wrapped in
whichever governance middlewares you want:

```go
// OpenAI Chat Completions (or any OpenAI-compatible endpoint).
caller, err := largemodel.NewOpenAIChatCallerFromConfig(largemodel.OpenAIConfig{
	Endpoints: []largemodel.OpenAIEndpoint{{Alias: "default", APIKey: apiKey, BaseURL: "https://api.openai.com/v1"}},
})

// Anthropic Messages. An empty base URL uses https://api.anthropic.com;
// vendor headers go through the provider's own options.
caller, err := largemodel.NewAnthropicMessagesCallerFromConfig(
	largemodel.AnthropicConfig{
		Endpoints: []largemodel.AnthropicEndpoint{{Alias: "default", APIKey: apiKey, BaseURL: ""}},
	},
	largemodel.WithAnthropicClientOptions(anthropic.WithBeta("context-1m-2025-08-07")))

model := largemodel.New(caller,
	largemodel.WithMiddleware(
		largemodel.NewTimeoutMiddleware(30*time.Second),
	),
)

// Quick carries the identity, endpoint, model and system prompt an
// entry-level agent needs anyway.
a := taskagent.Quick("assistant", "Assistant", model, "claude-sonnet-4-5", "You are helpful.")
```

`Quick` is a shorthand for the frequent parameters, not a reduced agent: it
delegates to `taskagent.New`, so the six lines below build exactly the same
thing — same defaults, same protocol derived from the caller, same ReAct loop.

```go
a := taskagent.New(
	agent.Config{ID: "assistant", Name: "Assistant"},
	taskagent.WithCaller(model),
	taskagent.WithModel("claude-sonnet-4-5"),
	taskagent.WithSystemPrompt(prompt.StringPrompt("You are helpful.")),
)
```

`New + Option` stays the full construction contract — use it whenever the agent
needs a description, a named or versioned prompt template, or any other
`agent.Config` field. Every remaining option (tools, memory, guards, budgets,
checkpointing) also layers onto `Quick` as trailing arguments, where a trailing
option overrides the matching preset.

There is no retry middleware in the `largemodel.New` chain above, and no
circuit breaker: a caller reaches its endpoint through a **router pool** inside
`largemodel`, and the pool owns the retries, the endpoint health and the
failover.
`WithRetryPolicy` and `WithRecoverTime` tune them:

```go
caller, err := largemodel.NewOpenAIChatCallerFromConfig(
	largemodel.OpenAIConfig{
		Endpoints: []largemodel.OpenAIEndpoint{{Alias: "default", APIKey: apiKey, BaseURL: baseURL}},
	},
	largemodel.WithRetryPolicy(time.Second, 2), // 1s then 2s, three attempts
	largemodel.WithRecoverTime(5*time.Minute),  // how long a dead endpoint stays out
)
```

When the recover window elapses the endpoint comes back *on probation* rather
than restored: the next real call re-tests it with a single attempt instead of
a whole retry round, and only a success promotes it back to available.

Two consequences are worth knowing. The router treats only HTTP 401 and 403 as
unretryable, so a deterministic 400 is retried like a transient failure and
then costs the endpoint its recover window — `largemodel.IsRetryable` is vage's
own, narrower reading of an error for code that has to judge one. And a pool
serves one call at a time, so a caller shared by parallel agents keeps several;
`WithConcurrency` caps how many (8 by default), and calls beyond that
wait rather than fail.

Messages are stored in the wire form of the vendor that produced them, so an
agent's `Protocol` must match its caller's; replaying a conversation against a
different protocol fails with `schema.ErrProtocolMismatch` rather than being
silently converted. Runnable versions of these snippets live in
[`largemodel/example_test.go`](largemodel/example_test.go); the `Quick` and
`New` constructions above run side by side in
[`agent/taskagent/example_test.go`](agent/taskagent/example_test.go).

### Several endpoints behind one model

A single-endpoint caller is a pool of one, so spreading a model over several
backends of the same protocol only changes how the pool is built:

```go
caller, err := largemodel.NewOpenAIChatCallerFromConfig(largemodel.OpenAIConfig{
	Strategy: largemodel.StrategyFailover,
	Endpoints: []largemodel.OpenAIEndpoint{
		{Alias: "primary", BaseURL: primaryURL, APIKey: primaryKey, Model: "gpt-4o"},
		{Alias: "backup", BaseURL: backupURL, APIKey: backupKey, Model: "gpt-4o-mini"},
	},
},
	largemodel.WithRecoverTime(5*time.Minute),
)
```

Each endpoint sends the model name it was configured with, whatever model the
request named. Strategies are failover, random, weighted, cost, and latency;
`caller.EndpointStats()` reports per-endpoint health merged across the
caller's pools. There is no cross-protocol failover: an OpenAI pool and an
Anthropic pool are separate, with no shared request to hand between them.

## Run Middleware (One Chain for Sync and Streaming)

TaskAgent gives every top-level `Run` and `RunStream` the same `agent.Middleware`
chain, executed exactly once per call — ReAct iterations, model retries and tool
counts do not multiply it. A middleware wraps the run's `RunFunc`, so it can
observe, short-circuit (never call `next`: no model call, no tool execution, no
checkpoint), or rewrite the final response:

```go
rewriter := agent.MiddlewareFunc(func(next agent.RunFunc) agent.RunFunc {
	return func(ctx context.Context, req *schema.RunRequest) (*schema.RunResponse, error) {
		// post-phase: the run already happened, rewrite its final response.
		resp, err := next(ctx, req)
		if err != nil {
			return nil, err
		}
		resp.Messages = []schema.Message{
			schema.NewTextMessage(schema.ProtocolOpenAIChat, schema.RoleAssistant, "rewritten"),
		}
		return resp, nil
	}
})

a := taskagent.New(
	agent.Config{ID: "assistant", Protocol: model.Protocol()},
	taskagent.WithCaller(model),
	taskagent.WithMiddleware(rewriter),
)

// The same middleware fires once on both paths:
//   - a.Run(ctx, req)
//   - a.RunStream(ctx, req)   // terminal AgentEnd.Message carries the rewrite
```

Whatever response leaves the chain — passed through, rewritten, or synthesised
by a short-circuit — is still what runs through the output guards, gets stored
to session memory, and becomes `AgentEnd.Message`; a middleware cannot route
around the safety boundary. `SessionID` and `Duration` are stamped by the
framework afterwards and cannot be forged. Returning `nil, nil` fails the run
with `taskagent.ErrNilMiddlewareResponse`.

Decide between the neighbouring seams by layer: `hook.Hook` observes events
(read-only), `agent.StreamMiddleware` transforms events on their way to a
stream consumer (streaming only), `largemodel.Middleware` wraps a single model
call (caching, rate limiting, timeouts), and Agent Middleware is the one
run-level control point that both entry points share. Existing Hook and
StreamMiddleware users do not need to migrate; move logic that changes a run's
outcome to Agent Middleware instead.

## Returning a Tool Result as the Final Answer

For tools that already *are* the answer — fetching a document, generating a
report — the extra model round that paraphrases the result adds cost and can
rewrite text you wanted delivered verbatim. `taskagent.WithReturnDirectTools`
marks such tools: when one of them succeeds, the ReAct loop skips the next
model call and returns the tool result as the final assistant answer
(`StopReasonComplete`).

```go
reg := tool.NewRegistry()
_ = reg.Register(schema.ToolDef{Name: "fetch_report"}, func(_ context.Context, _, _ string) (schema.ToolResult, error) {
	return schema.TextResult("", "Q3 report ..."), nil
})

a := taskagent.New(
	agent.Config{ID: "assistant"},
	taskagent.WithCaller(model),
	taskagent.WithModel("claude-sonnet-4-5"),
	taskagent.WithToolRegistry(reg),
	taskagent.WithReturnDirectTools("fetch_report"),
)

resp, err := agent.RunText(context.Background(), a, "Pull the Q3 report")
// resp.Messages[0].Text() == the report verbatim, and the model ran exactly once.
```

The option is off by default; an agent that never calls it behaves exactly as
before. Rules:

- The loop ends on the first tool, in the model's call order, whose name is
  configured and whose guard-passed result succeeded. The whole batch still
  runs under the existing concurrency rules, and the batch's completion order
  does not participate in the selection. A tool-result guard rewrite is
  exactly what gets returned.
- A failed tool never short-circuits: handler/registry errors, `IsError`
  results and tool-result guards that block or turn the result into an error
  all keep the existing loop behaviour.
- Direct return only skips further model rounds. Output guards, session
  memory, Agent middleware and `AgentEnd.Message` still run on the returned
  text.
- Names match by exact string equality; empty names are ignored and repeated
  calls merge. Names that are not registered, or that a request or skill
  filter removes, stay inert — nothing fails at construction.

## Sharing State Across Tools in One Run

Tools in the same run can hand each other temporary state through the run-value
store that `taskagent` binds to the context on every `Run`, `RunStream` and
`Resume`. Below, `plan` publishes a key in one ReAct round and `apply` reads it
back in a later round — no shared globals, no change to the tool signature:

```go
_ = reg.Register(schema.ToolDef{Name: "plan"},
	func(ctx context.Context, _, args string) (schema.ToolResult, error) {
		if !schema.SetRunValue(ctx, "demo/plan", args) {
			// No store bound: this executor does not provide run values.
			return schema.ErrorResult("", "run values unavailable"), nil
		}
		return schema.TextResult("", "planned"), nil
	},
)

_ = reg.Register(schema.ToolDef{Name: "apply"},
	func(ctx context.Context, _, _ string) (schema.ToolResult, error) {
		v, ok := schema.GetRunValue(ctx, "demo/plan")
		if !ok {
			return schema.ErrorResult("", "call plan first"), nil
		}
		return schema.TextResult("", "applied "+v.(string)), nil
	},
)
```

The store is scoped to one run, not to one session: every round and every
parallel tool of that run shares it, while a second run — even on the same
agent with the same `SessionID` — starts empty. `SetRunValue` returns `false`
and `GetRunValue` returns `nil, false` when no store is bound, so the same tool
still works under an executor that provides none. Reads and writes are safe
from parallel tools, but concurrent writes to one key have no defined winner
and there is no compare-and-swap or ordering guarantee. Nothing here is
persisted or checkpointed — use `memory` or `workspace` for state that must
outlive the run.

## Reporting Progress From Inside a Tool

A tool that runs for a while can push its own events onto the active stream
with `schema.EmitCustomData`, so the caller sees progress before the tool
returns:

```go
_ = reg.Register(schema.ToolDef{Name: "ingest"},
	func(ctx context.Context, _, _ string) (schema.ToolResult, error) {
		for i, stage := range []string{"fetch", "parse", "index"} {
			schema.EmitCustomData(ctx, "ingest.progress", map[string]any{
				"stage": stage,
				"step":  i + 1,
			})
		}
		return schema.TextResult("", "ok"), nil
	},
)
```

Consumers receive these on `RunStream` as `schema.EventCustom` with a
`schema.CustomEventData{Name, Payload}` body, between that tool call's
`tool_call_start` and `tool_call_end`:

```go
if e.Type == schema.EventCustom {
	d := e.Data.(schema.CustomEventData)
	if d.Name == "ingest.progress" {
		// interpret d.Payload
	}
}
```

The top-level event type is always `custom`, so dispatch on the type *and* the
name — `EventData` stays sealed and applications cannot add event types. Use a
stable, namespaced name and a JSON-serializable payload with no credentials in
it: neither is validated, and the payload is stored as given, not copied. The
call is best-effort and returns nothing — with no emitter bound to the context
it is a silent no-op, and a closing stream simply drops the event — so treat
custom events as observability, never as the only trigger for a state change.

## License

Apache License 2.0 — see [LICENSE](LICENSE).
