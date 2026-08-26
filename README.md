# vage

[![Build](https://github.com/vogo/vage/actions/workflows/build.yml/badge.svg)](https://github.com/vogo/vage/actions/workflows/build.yml)
[![codecov](https://codecov.io/gh/vogo/vage/branch/main/graph/badge.svg)](https://codecov.io/gh/vogo/vage)


A Go framework for building LLM-based intelligent agent systems.

## Features

- **Composable Agents** — TaskAgent (ReAct tool-calling), RouterAgent (routing), WorkflowAgent (DAG orchestration), and CustomAgent (user-defined)
- **DAG Orchestration** — Parallel execution, loops, conditionals, compensation (Saga), checkpointing, backpressure, priority scheduling
- **Three-Level Memory** — Working (request) → Session (conversation) → Store (persistent), with context compression and token budgets
- **Security Guardrails** — Prompt injection, content filter, PII, topic, length, and custom guards
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
caller, err := largemodel.NewOpenAIChatCaller(apiKey, "https://api.openai.com/v1")

// Anthropic Messages. An empty base URL uses https://api.anthropic.com;
// vendor headers go through the provider's own options.
caller, err := largemodel.NewAnthropicMessagesCaller(apiKey, "",
	largemodel.WithAnthropicClientOptions(anthropic.WithBeta("context-1m-2025-08-07")))

model := largemodel.New(caller,
	largemodel.WithMiddleware(
		largemodel.NewTimeoutMiddleware(30*time.Second),
	),
)

a := taskagent.New(
	agent.Config{ID: "assistant", Protocol: model.Protocol()},
	taskagent.WithCaller(model),
	taskagent.WithModel("claude-sonnet-4-5"),
)
```

There is no retry middleware in that chain, and no circuit breaker: a caller
reaches its endpoint through a **router pool** inside `largemodel`, and the
pool owns the retries, the endpoint health and the failover.
`WithRetryPolicy` and `WithRecoverTime` tune them:

```go
caller, err := largemodel.NewOpenAIChatCaller(apiKey, baseURL,
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
[`largemodel/example_test.go`](largemodel/example_test.go).

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

## License

Apache License 2.0 — see [LICENSE](LICENSE).
