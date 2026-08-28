# 0001. Independent Interrupt State Machine (vs. Extending Checkpoint)

- **Status:** proposed
- **Date:** 2026-08-28

## Context

TaskAgent needs a human-in-the-loop pause that can resume in another process: after the model emits tool calls that require an external decision, the run suspends *before* any tool handler executes, persists the unfinished batch plus continuation, and lets a second process resume from exactly those calls once the decisions are supplied.

Existing `checkpoint.IterationStore` is an iteration snapshot for "replay the latest completed turn after a crash"; its invariant is "this is a complete, finished turn". `tool/askuser` is an in-process synchronous block with no framework-level durable record — timeout or process exit ends it. Neither can safely carry "suspend-before-execute, pending set, lease, inject external decisions": stuffing a pre-execution hang into checkpoint would break its complete-turn assumption, and wrapping cross-process resume as another layer of blocking `ask_user` would be a fake API without real resumability.

## Decision

Add an independent `interrupt` package that defines the `Record`/`Store` persistence contract and the Pending → Ready → Resuming → Completed state machine. TaskAgent opts in via `WithInterruptStore`/`WithInterruptPolicy`. Interrupt records are not written to `IterationStore`, and `Resume(sessionID)` does not serve both semantics.

The suspend check lives in the shared `runReactLoop` — after the full assistant tool-call message is in hand, after the budget check, and before `executeToolBatch` — so sync and stream share one gate. On a hit the whole batch is frozen; ordinary sibling calls in that batch run only after every pending call has a decision. `ResumeInterrupt(ctx, req)` is the public resume entry: it addresses `interrupt_id + tool_call_id`, does not enter the Agent middleware chain, does not re-run input guards, and starts with empty Run values.

`Pending` is a non-empty unique subset of the batch's tool-call IDs. `SubmitDecisions` never demotes `Resuming` back to `Ready` (an idempotent resubmit must not drop a live lease). FileStore `Delete` takes the same per-record cross-process lock as every other mutation.

## Rationale

Checkpoint's "complete-turn snapshot" invariant and interrupt's "pre-execution hang with an unfinished pending set" invariant are mutually exclusive; merging them would pollute both. Independent interfaces let each configure its own backend, retention, and resume semantics. The cost is a second persistence contract (`interrupt.Store`) and state machine; the gain is zero sibling side effects before suspend, an explainable resume boundary, and a `Resume(sessionID)` caller that never has to guess intent.

Freezing the whole tool batch rather than releasing unflagged calls one-by-one trades some parallelism for zero same-batch side effects, stable result order, and a clear resume boundary. Persisting already-resolved run parameters (`interrupt.EffectiveParams`) rather than re-reading Agent defaults on resume makes the record larger, but a new process cannot silently change remaining budget, tool scope, or model. A lease (owner + expiry) rather than a permanent lock lets a crashed resumer be taken over; the trade-off is no end-to-end exactly-once.

## Consequences

- Positive: `ask_user` (blocking), checkpoint (crash replay), and interrupt (human-in-the-loop suspend) stay three distinct paths that do not substitute for each other; docs and code can evolve separately (see [agent-core](../../domains/agent/agent-core/agent-core.md) AC-14, [orchestration](../../domains/agent/orchestration/orchestration.md) OR-9, [tooling](../../domains/capability/tooling/tooling.md) TOOL-9).
- Positive: `interrupt.Store` configures its backend (`MapStore`/`FileStore`) and retention independently of checkpoint lifetime. `FileStore` uses an `<id>.lock` file for cross-process mutual exclusion on every mutation of that record, including `AcquireLease` and `Delete`, so two independent processes can safely contend for the same resume.
- Negative: one more persistence contract and state machine to maintain; callers must understand the three adjacent mechanisms.
- Negative: no end-to-end exactly-once — ordinary tools may replay after a lease expires and is taken over; callers of side-effecting tools still need idempotency keys or compensation.
