# 0003. Run Parameter Resolver (Single-Slot Parameterization, Not a New Plane)

- **Status:** accepted
- **Date:** 2026-09-03

## Context

A TaskAgent Run merges Agent defaults with `schema.RunOptions` inside `resolveRunParams`, freezes the tool set later in `prepareContext`, and only then enters `agent.Middleware`. Three existing planes therefore cannot express "narrow this Run's model, limits and tool visibility before the model sees tool definitions":

- Input guards inspect the last user message, not run parameters or the tool registry.
- Agent Middleware wraps the ReAct loop after context and tools are already built; a rewrite of `req.Messages` does not un-disclose tools already placed on the outbound request.
- Tool execute middleware and InterruptPolicy run after the model has already been shown the tool list.

A related but orthogonal gap sits in Caller assembly: hosts that used `NewCaller` (a pool of one) had to construct parallel Agent/Caller instances per credential or endpoint. Multi-endpoint failover already exists in `largemodel/router` via `BuildCaller` / `ComposeCaller`; it is not a Run-parameter problem and must not be solved by injecting Caller or endpoint into `RunRequest`.

The seam-admission rule in [adr.md](adr.md) requires an ADR before any *new* ReAct hot-path interception plane. This change needs a recorded decision on whether a pre-context parameter hook is a new plane or a parameterization of the existing preflight.

## Decision

Add a single-slot `taskagent.ParamResolver` applied with `WithParamResolver`. It runs once per fresh `Run` / `RunStream`, after input guards and after built-in default/`RunOptions` merge, and before parameter validation, tool-set freeze, context build, model I/O, tool I/O and checkpoint writes. `Resume` and `ResumeInterrupt` never call it.

**Seam criterion:** a hook that is a single slot, injected once at construction, and not chainable is *parameterization* of an existing step. A hook that admits multiple ordered registrations, wrapping, or per-iteration control flow is a *new plane* and needs a fresh ADR before runtime code. ParamResolver is parameterization: later `WithParamResolver` replaces the earlier one; the framework does not sort or compose resolvers.

Do **not** introduce `RunPlan` / `RunPlanner` / `PreflightMiddleware`, per-iteration `prepareStep`, `auth.Principal`, per-request Caller/endpoint/`RoutePolicy`, or `largemodel.Capabilities`.

Routing Caller assembly stays in the host/integration layer: `largemodel.BuildCaller` is the declarative multi-endpoint entry; the host binds a `ComposeCaller` to a TaskAgent at construction (or at the Run entry by selecting a pre-built Agent), never inside the ReAct loop.

`schema.RunOptions` grows additively with nested `Limits` (`*int` fields) and `ToolMode`. Old int fields stay. Tool visibility is a fail-closed three-way intersection (`RunOptions` scope ∩ skill `AllowedTools` ∩ `EnabledFunc`); empty is distinct from unrestricted. Interrupt `EffectiveParams` becomes v2 (`CurrentVersion = 2`) and stores `tool_mode` plus the post-intersection names. Version mismatch is `ErrUnknownVersion`; there is no v1↔v2 guess.

## Rationale

Extending input guards would overload a user-text policy engine with authorization and budget semantics it does not own. Extending Agent Middleware would be too late: tools are already frozen and sent. A chained preflight middleware plane would be the right shape *if* multiple independent policies had to wrap control flow; that is exactly the "new plane" trigger, and this change does not have that requirement. One construction-time function that returns a `RunParams` value is enough to let a host apply tenant policy without a second interception API.

Keeping Caller routing out of ParamResolver preserves [constitution.md](../../constitution.md) § retry/routing: `largemodel/router` remains the only retry and same-protocol failover source. ParamResolver cannot substitute for endpoint selection.

Pointer `Limits` plus `ToolMode` avoid a breaking `int` → `*int` migration. The cost is two input locations for the same ideas, resolved by a fixed priority: non-nil Limits field, else a positive old field, else Agent default. `ptr(0)` is the only way to say "explicit unlimited / omit vendor cap"; old `0` keeps the Agent default, including `RunTokenBudget: 0`.

## Consequences

- Positive: authorization and tool disclosure can fail closed before the model sees definitions; resume restores the frozen names rather than re-running policy; ParamsResolved and RouteSelected events make the two decisions observable without credentials.
- Positive: hosts migrate from `NewCaller` to `BuildCaller` without constructing one Agent per backup endpoint; tenant→Caller binding stays outside `service.Service`.
- Negative: interrupt v2 is not independently rollback-safe. A v1 reader must refuse v2 records with a version error, never interpret an empty v1 `ToolFilter` as v2 `all` or `none`.
- Negative: ParamResolver cannot change Caller, cannot expand tools mid-run, and cannot run on Resume. Changing model or authorization mid-logical-Run requires a new Run.
- The "single slot / not chainable = parameterization" criterion is now the admission test for similar preflight hooks. Multiple ordered resolvers would be a new plane.

### ADR six questions

1. **Why existing planes are insufficient.** Input guards see user text only. Agent Middleware and tool/interrupt planes run after tools are already on the model request. None of them emit a parameters-resolved event.
2. **Why extending those planes is not enough.** Putting authorization into Middleware cannot un-send tool definitions. Putting it into guards conflates PII/injection policy with run-parameter policy. A new chained preflight plane would work but is more API than the requirement needs.
3. **Trigger.** Once per fresh Run/RunStream, after input guards, before context build. Never on Resume / ResumeInterrupt.
4. **Observability.** `EventParamsResolved` after successful freeze, via the hook path, payload credscrubbed. Router emits `EventRouteSelected` at selection/reuse. Observer failure must not change routing or Run outcome.
5. **Guards / interrupt / resume.** Input guards still run first. Output and tool-result guards are unchanged. Interrupt snapshots v2 EffectiveParams; ResumeInterrupt restores them and does not re-query skills or EnabledFunc. Checkpoint Resume still uses Agent defaults. Both resume entries still skip input guards and Agent Middleware.
6. **Compatibility.** Old clients omitting Limits/ToolMode/ParamResolver keep historical behaviour. `RunTokenBudget: 0` still keeps the Agent default. Interrupt v2 is a persistence format switch with fail-closed version checks.
