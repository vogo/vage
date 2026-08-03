# vage

[![Build](https://github.com/vogo/vage/actions/workflows/build.yml/badge.svg)](https://github.com/vogo/vage/actions/workflows/build.yml)
[![codecov](https://codecov.io/gh/vogo/vage/branch/main/graph/badge.svg)](https://codecov.io/gh/vogo/vage)


A Go framework for building LLM-based intelligent agent systems.

## Features

- **Composable Agents** — TaskAgent (ReAct tool-calling), RouterAgent (routing), WorkflowAgent (DAG orchestration), and CustomAgent (user-defined)
- **DAG Orchestration** — Parallel execution, loops, conditionals, compensation (Saga), checkpointing, backpressure, priority scheduling
- **Three-Level Memory** — Working (request) → Session (conversation) → Store (persistent), with context compression and token budgets
- **Security Guardrails** — Prompt injection, content filter, PII, topic, length, and custom guards
- **LLM Middleware** — Decorator chain: logging, circuit breaker, rate limiting, retry, timeout, cache, metrics
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
	anthropic.WithBeta("context-1m-2025-08-07"))

model := largemodel.New(caller,
	largemodel.WithMiddleware(
		largemodel.NewRetryMiddleware(largemodel.WithMaxRetries(3)),
		largemodel.NewTimeoutMiddleware(30*time.Second),
	),
)

a := taskagent.New(
	agent.Config{ID: "assistant", Protocol: model.Protocol()},
	taskagent.WithCaller(model),
	taskagent.WithModel("claude-sonnet-4-5"),
)
```

Messages are stored in the wire form of the vendor that produced them, so an
agent's `Protocol` must match its caller's; replaying a conversation against a
different protocol fails with `schema.ErrProtocolMismatch` rather than being
silently converted. Runnable versions of these snippets live in
[`largemodel/example_test.go`](largemodel/example_test.go).

## License

Apache License 2.0 — see [LICENSE](LICENSE).
