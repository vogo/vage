# 0002. Session Memory Namespace by (agentID, sessionID)

- **Status:** proposed
- **Date:** 2026-09-03

## Context

A single `memory.Manager` can be reused across TaskAgent Runs that share a `Store`. Session messages were stored under bare logical keys such as `msg:000001`. `SessionMemorySource` ignored `FetchInput.SessionID`, and `Clear` called `Store.Clear`, so a miswired or naïve caller mixed one session's history into another session's model prompt, or wiped the whole backend. That is accidental crosstalk from shared configuration, not a tenant-auth gap.

Checkpoint resume is addressed by query `sessionID`, but a stored `Checkpoint.SessionID` can disagree with that query. Continuing the run under the query identity would write restored messages into a different session's namespace.

## Decision

Session memory is partitioned by the caller-declared pair `(agentID, sessionID)`:

- `Manager.ForSession` returns a view that shares the original backend, promoter, archiver, compressor, and session-tier mutex, and rebinds session identity without mutating the original Manager or stacking prefixes.
- Logical Memory keys stay `msg:000001` (and similar). `memoryBase` maps them to `mem:<tier>:<b64url(agent)>:<b64url(session)>:<logical-key>`. Identity segments are unpadded base64url encodings of the UTF-8 ID bytes. The long-term store tier leaves those segments empty so facts remain cross-session without sharing a prefix with session data.
- `syncMemory.Clear` lists the current physical prefix and deletes those keys. It does not call `Store.Clear`.
- Read and write paths (`SessionMemorySource`, TaskAgent `storeAndPromoteMessages`) go through `ForSession`. An empty `SessionID` skips session memory; the Run still proceeds.
- On `Resume`, the query `sessionID` still locates the latest checkpoint and a mismatched `Checkpoint.AgentID` is still rejected. After a successful load, `Checkpoint.SessionID` is the authority for the resumed memory scope, events, and response. If it differs from the query value, both are recorded with `slog.Warn`.

This change intentionally breaks the physical keyspace: old bare `msg:` keys are not read, and there is no dual-read, dual-write, or migrator.

## Rationale

base64url identity segments prevent an ID from forging `:` delimiters or colliding with a sibling prefix; the cost is that physical keys are no longer human-readable. Scope is a caller assertion: the framework partitions by the values it is given and does not re-check Metadata or implement TenantID/UserID authorization.

RunID is not part of the namespace in this cut. Concurrent Runs on the same `(agentID, sessionID)` can still compute the same `msg:%06d` offset from the same history length and overwrite each other. That remains a documented limitation rather than a silent fix.

Keeping the `Store` interface unchanged avoids a `ScopedStore` adapter and leaves existing backends usable.

## Consequences

- Positive: one Manager plus one Store can be reused across sessions without mixing conversation history; `Clear` cannot empty a shared backend; Resume cannot continue a checkpoint into the wrong session namespace.
- Positive: the Memory API still speaks logical keys, so promoters, compressors, and callers do not concatenate prefixes themselves.
- Negative: upgrading without an external export/rewrite loses previously stored session entries. Operators who need history must migrate themselves before switching versions.
- Negative: empty `SessionID` no longer reads or writes session memory (temporary per-run namespaces are a later capability). Concurrent same-session Runs can still clobber `msg:%06d` keys.
- Negative: Resume that is given a different query `sessionID` than the checkpoint records will warn and continue under the checkpoint identity, which can surprise callers who treated the query parameter as the live namespace.
