# pk toolbelt

pk should make small, deliberate actions easy for an agent to combine. The toolbelt is a set of typed affordances over Unreal's durable tool-call and operation loop, not a second execution runtime. Each tool should reduce a recurring source of model guesswork while keeping its inputs, output bounds, and side effects legible.

## Current foundation

The pinned Unreal harness already supplies **Bash**, **ViewImage**, and **SkillUse**. Bash and ViewImage translate calls into durable operations; SkillUse selects a registered skill. The pk runner's default local operation manager executes the built-in shell and image operations. `runner.Options.RegistryFactory` is a composition seam for an in-repository host to provide a registry, but it does not replace the operation manager. A custom translator must submit an operation type that the selected manager can execute. New side effects need a compatible durable operation path. See [the integration notes](research/unreal-integration.md) for the boundary.

That distinction matters: a new schema is easy; a new durable operation requires an executable operation path. We should not present a proposed affordance as shipped until its translator is registered and its operation path is executable.

## Toolbelt design

Keep a short eager set for frequent work and make specialized tools discoverable as the catalog grows. Prefer tools that expose a bounded observation or a narrow edit over generic wrappers that hide broad authority. Keep translation pure: validate arguments, form a serializable operation spec, and let the coordinator persist and dispatch it.

| Status | Primitive | What it contributes | Runtime requirement |
| --- | --- | --- | --- |
| Available through Unreal | `Bash` | General command execution with durable process tracking and bounded returned output. | Existing shell operation. Its configured process access is broad; output limits are not a security boundary. |
| Available through Unreal | `ViewImage` | Image inspection through an asynchronous operation. | Existing image operation. |
| Available through Unreal | `SkillUse` | Load a registered instruction set when needed. | Existing registry behavior. |
| Proposed | `ArtifactSlice` | Return a byte-bounded slice with stable line/byte offsets, a content hash, and a continuation cursor. If the file changes, the next read reports a stale cursor instead of silently mixing revisions. | Needs a pk registry and a filesystem operation that resolves paths beneath an explicit workspace root. A quoted shell command is neither a root policy nor a sandbox. |
| Proposed | `WorkspaceDelta` | Summarize changed paths and compact diff hunks, with explicit truncation and a base revision. | Can run as a fixed read-only command over the existing shell operation, but needs a pk registry and should require a Git checkout with an explicit root. |
| Proposed | `PatchApply` | Apply a unified diff only if the target's expected content hash still matches, then return the resulting hash and diff. | Requires an atomic compare-and-swap file operation; the current local manager has no such operation. Do not emulate it with a check followed by an unrelated shell write. |
| Proposed | `EventWait` | Wait for the next matching session or operation event after a cursor and return a compact delta. | Needs a cursor-aware event API. Unreal's coordinator already selects on events internally, but it does not expose that subscription as a tool. |
| Proposed | `RunBudget` | Give a tool call an explicit deadline, output budget, and cancellation reason; report which limit ended it. | Unreal has output bounds and cancellation, but no common per-call deadline/budget envelope across tool types. |

## Async call path in Unreal

The diagram shows the current coordinator behavior, including its one-second tool-result grace window. A fast result can be included while a slower call remains represented by a running placeholder. The coordinator waits on events instead of polling the model. User input is queued as another event; an in-flight request keeps the prefix it was submitted with, and newly available items are appended to a later request.

```mermaid
sequenceDiagram
    autonumber
    participant M as Model
    participant C as Unreal coordinator
    participant S as Session store
    participant O as Operation manager
    participant U as User / inbox

    M->>C: Response with parallel tool calls
    C->>S: Append model response and call statuses / operation snapshots
    Note over C,S: Durable intent is recorded before dispatch
    C->>O: Dispatch accepted operations
    par Independent operation A
        O-->>C: A completes quickly
    and Independent operation B
        O-->>C: B is still running
    end
    Note over C: Collect results during the 1 s grace window
    C->>S: Append completed result A; retain B as pending
    Note over C,M: If grace expires with B pending, submit A's result and a running placeholder for B
    C->>M: Next request with available results and pending placeholder
    Note over C: While waiting, select on inbox, operation updates, and model response; no tight polling
    opt User steers while the model request is in flight
        U->>C: Steering input arrives
        C->>S: Persist input
        C-xM: Cancel the superseded model request
        C->>S: Append a new turn after the prior submitted turn
        C->>M: Replacement request keeps its input snapshot and adds steering
    end
    O-->>C: B completes while the active request is in flight
    C->>S: Persist operation update; keep the active request snapshot unchanged
    M-->>C: Active or replacement request completes
    C->>S: Append model response, then any result items now available
    C->>M: Following request preserves the submitted prefix and adds the new suffix
```

The diagram describes one event ordering. If an operation finishes after the model response instead, its result joins the next request's suffix. User steering interrupts the active model request and starts a new turn; it does not cancel tool operations. The coordinator's main loop blocks on inbox, operation, model-response, and timer channels; it does not tightly poll the model. If configured, a heartbeat timer adds a status input and triggers a model check-in while tool calls remain pending, so heartbeats are not free.

## First implementation candidate

`ArtifactSlice` is a strong base candidate because stable offsets and a content hash let the model retrieve only evidence it needs, then continue safely without rereading the whole file. Its contract should accept a workspace-relative path, a byte offset, and a capped byte count; return the exact slice, hash, and continuation cursor; and reject traversal and symlink escapes against a configured workspace root. If the artifact changes between reads, the cursor should fail as stale. The root check belongs in a filesystem-aware operation that opens the file beneath that root. It must not be described as a sandbox: Bash still has whatever process permissions the host grants it.

Unreal's local operation manager currently has no filesystem-read operation, and routing this feature through a generated shell command would inherit shell/runtime access and would not provide reliable root confinement. Therefore `ArtifactSlice` remains proposed until pk owns an operation manager or an upstream-compatible operation is available. A workspace adapter also cannot just pass a new translator to `tool.NewRegistry`: that registry's static definitions are closed over its built-in names. This document does not claim a working custom tool implementation.

## Acceptance checks for future primitives

- The tool schema rejects malformed and oversized arguments before submitting work.
- The translator performs no filesystem, process, or network I/O on the coordinator's event loop.
- The operation has explicit bounds, durable state, cancellation behavior, and a recovery policy.
- Results state truncation and preserve enough metadata to request the next slice without repeating the whole artifact.
- A real manager executes it; tests cover its authorization boundary and its operation result path.
- Names and descriptions tell the model what the tool can change and how to recover from a stale snapshot.
