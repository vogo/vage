# AGENTS.md

## Project

`github.com/vogo/vage` — a composable, observable, operable Go framework for LLM agent systems.
Builds Task / Router / Workflow / Custom agents on `github.com/vogo/aimodel` native protocol clients.

- **Doc entry:** `doc/overview.md` — knowledge-base rules and domain map
- **Hard constraints:** `doc/constitution.md` — highest-priority red lines; read before any design change
- **Features / usage:** `README.md`

## Core Principles

Conflict arbitration (lower number wins); full text in `doc/constitution.md`:

1. **Contract stability > feature growth** — `schema` and public APIs stay backward-compatible.
2. **Protocol fidelity > convenience** — vendor-native wire shapes; no lossy unification.
3. **Correctness & safety > performance** — guards, budgets, idempotency, checkpointing first.
4. **Composable > monolithic** — small replaceable parts over inseparable wholes.

## Rules

- **code ↔ doc sync** — behavior changes update `doc/`; doc changes land in code. Docs write WHY; code writes HOW (`doc/overview.md`).
- **Comments** — intent, usage, caveats only; never restate what the code already says.
- **Quality gate** — every commit passes `make build` (license-check → format → lint → test).
- **License header** — every Go file carries the Apache-2.0 header.
- **Docs language** — `doc/` in English.
- No `Co-Authored-By` in commit messages.

## Build

`make build` · `make test` · `make lint` · `make format` · `make license-check`

## Where the design lives

Read the relevant page before changing behavior — this file does not restate the design.

- **Vision, scope, non-goals** — `doc/project.md`
- **Hard constraints / red lines** — `doc/constitution.md`
- **Architecture & dependency topology** — `doc/architecture/architecture.md`
- **Terms** — `doc/glossary.md`
- **Agent forms, schema, prompts** — `doc/domains/agent/` · packages `agent`, `schema`, `prompt`
- **DAG orchestration, checkpoints** — `doc/domains/agent/orchestration/` · packages `orchestrate`, `checkpoint`
- **Memory & context** — `doc/domains/memory/` · packages `memory`, `context`
- **Session, workspace, sessionview** — `doc/domains/memory/session/` · packages `session`, `workspace`, `sessionview`
- **LLM middleware & router** — `doc/domains/capability/model/` · package `largemodel`
- **Tools, MCP, skills** — `doc/domains/capability/tooling/` · packages `tool`, `mcp`, `skill`
- **Guards & credential scrubbing** — `doc/domains/platform/guard/` · packages `guard`, `security`
- **HTTP service, hooks, eval, vector** — `doc/domains/platform/` · packages `service`, `hook`, `eval`, `vector`

Cross-package tests: `integrations/`.
