# AGENTS.md

## Project

`github.com/vogo/vage` — a composable, observable, operable Go framework for LLM agent systems.
Builds Task / Router / Workflow / Custom agents on `github.com/vogo/aimodel` native protocol clients.

- **Doc entry:** [doc/overview.md](doc/overview.md) — knowledge-base rules and domain map
- **Hard constraints:** [doc/constitution.md](doc/constitution.md) — highest-priority red lines; read before any design change
- **Features / usage:** [README.md](README.md)

## Core Principles

Conflict arbitration (lower number wins); full text in [constitution.md](doc/constitution.md):

1. **Contract stability > feature growth** — `schema` and public APIs stay backward-compatible.
2. **Protocol fidelity > convenience** — vendor-native wire shapes; no lossy unification.
3. **Correctness & safety > performance** — guards, budgets, idempotency, checkpointing first.
4. **Composable > monolithic** — small replaceable parts over inseparable wholes.

## Rules

- **code ↔ doc sync** — behavior changes update `doc/`; doc changes land in code. Docs write WHY; code writes HOW ([overview.md](doc/overview.md)).
- **Comments** — intent, usage, caveats only; never restate what the code already says.
- **Quality gate** — every commit passes `make build` (license-check → format → lint → test).
- **License header** — every Go file carries the Apache-2.0 header.
- **Docs language** — `doc/` in English.

## Build

`make build` · `make test` · `make lint` · `make format` · `make license-check`

## Where the design lives

Read the relevant page before changing behavior — this file does not restate the design.

| Touching… | Read | Packages |
|-----------|------|----------|
| Vision, scope, non-goals | [doc/project.md](doc/project.md) | — |
| Hard constraints / red lines | [doc/constitution.md](doc/constitution.md) | — |
| Architecture & dependency topology | [doc/architecture/architecture.md](doc/architecture/architecture.md) | — |
| Terms | [doc/glossary.md](doc/glossary.md) | — |
| Agent forms, schema, prompts | [doc/domains/agent/](doc/domains/agent/) | `agent`, `schema`, `prompt` |
| DAG orchestration, checkpoints | [doc/domains/agent/orchestration/](doc/domains/agent/orchestration/) | `orchestrate`, `checkpoint` |
| Memory & context | [doc/domains/memory/](doc/domains/memory/) | `memory`, `context` |
| Session, workspace, sessionview | [doc/domains/memory/session/](doc/domains/memory/session/) | `session`, `workspace`, `sessionview` |
| LLM middleware & router | [doc/domains/capability/model/](doc/domains/capability/model/) | `largemodel` |
| Tools, MCP, skills | [doc/domains/capability/tooling/](doc/domains/capability/tooling/) | `tool`, `mcp`, `skill` |
| Guards & credential scrubbing | [doc/domains/platform/guard/](doc/domains/platform/guard/) | `guard`, `security` |
| HTTP service, hooks, eval, vector | [doc/domains/platform/](doc/domains/platform/) | `service`, `hook`, `eval`, `vector` |

Cross-package tests: `integrations/`.
