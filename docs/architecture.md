# Architecture and extension points

`pk` keeps terminal rendering, process control, durable work, and model execution in separate layers.
The command-line program is the composition root: it owns credentials and config, starts detached task
workers, or serves the local protocol used by the OpenTUI process.

```mermaid
flowchart LR
    UI[OpenTUI frontend] <-->|versioned JSON lines on stdio| RPC[pk rpc]
    CLI[pk CLI] --> TASKS[internal/tasks]
    RPC --> TASKS
    TASKS --> WORKER[detached pk worker]
    RPC --> RUNNER[internal/runner]
    WORKER --> RUNNER
    PLAIN[--plain / pk run] --> RUNNER
    RUNNER --> HARNESS[Unreal Agent v0.1.1 harness]
    RUNNER --> STORE[durable session store + prefix snapshot]
    HARNESS --> PROVIDER[OpenAI Responses API]
```

The UI sends commands and renders events; it does not own model credentials or call the provider.
The Go `pk rpc` process resolves config, prepares the authenticated adapter, and passes turns to the
runner. `internal/tasks` persists a task manifest and append-only output events, then starts a
detached copy of the pk executable as the worker. `pk task attach` follows that event stream and can
be interrupted without stopping the worker. Cancellation is a separate command that signals the
worker. The TUI's `/tasks` picker and `/task` commands use the same task store through RPC.

## Progress and tool feedback

The runner emits assistant messages in provider order, excluding analysis content. Longer tasks are
prompted to send brief plans and factual milestone updates alongside tool work; these are ordinary
assistant messages, not hidden reasoning or timer-driven filler. Tool-call events carry a stable
`call_id`, name, running/completed/failed state, elapsed time, and bounded argument/command previews.
Operation summaries can include bounded shell output/error excerpts and exit codes. The OpenTUI adds
the first event as a chronological transcript row, then updates that row by `call_id` as work moves
from running to terminal, keeping completed results visible. Common secret fields and credential
assignments are redacted from previews; this is a defensive display filter, not a complete secret
detector. The Go `pk run --jsonl` interface exposes these runner events for scripts.

Detached tasks can receive explicit follow-up prompts through their task host. They do not have a
persisted typed `awaiting_input` request/reply state for model-generated questions or tool approvals;
model-generated questions are available only in foreground sessions through a request-ID-correlated
`AskUser` broker. Answers are fed back into the same tool call; canceling the request cancels the
turn. `AskUser` is for clarifying decisions, not permission to execute a tool. See
[the harness interaction note](research/harness-interactions.md).

`internal/runner` composes the pinned Unreal Agent session coordinator, durable session storage,
operation manager, tool registry, and model adapter. The `llm.Adapter` boundary isolates model
requests. The operation manager is responsible for executing durable Bash and image operations. The
default tool registry exposes Bash, ViewImage, and SkillUse; skills are discovered from disk and
registered in that runtime. This project does not discover arbitrary plugin binaries or load a
user-defined tool registry from config.

## Context reuse and cache reporting

Each session has a stable prefix snapshot containing its workspace, system instructions, registered
tool schemas, and the skill documents captured at session start. Changing the model or reasoning
effort does not rewrite that snapshot. On resume, pk restores the same prefix and continues the same
durable session. This keeps repeated requests structurally consistent and avoids silently changing
instructions when a skill file is edited later. Starting a fresh session with `/new` captures the
current system instructions and skill contents; an existing session continues using its saved
snapshot.

The OpenAI Responses API cache is implicit and provider-controlled. The adapter passes the stable
session ID through the runtime's cache-key field, and keeping a long stable prefix can make reuse more
likely. It does not promise a hit or a particular cache lifetime. For each response with usage data,
`pk run --jsonl` emits a `usage` event with `input_tokens`, `output_tokens`, `reasoning_tokens`,
`cached_input_tokens`, and `cache_write_input_tokens`. The two cache counts have matching
`*_available` booleans, and `usage_available` indicates whether raw provider usage was present. A
zero is a reported zero only when its availability boolean is true; otherwise the backend did not
provide that count. If all usage values are absent or zero, no usage event is emitted. These are
provider-returned counters, not local estimates. Other provider adapters may expose different usage
details. See the [OpenAI prompt-caching guide](https://developers.openai.com/api/docs/guides/prompt-caching)
for the provider's current caching behavior.

For an in-repository Go host, [`runner.Options.ContextSnapshots`](../internal/runner/runner.go)
accepts the [`ContextSnapshotStore`](../internal/runner/context_snapshot.go) interface with
`LoadContext` and `SaveContext` methods. A host can use this seam to store the exact prefix snapshot
alongside a custom session store. `runner.Options.Adapter` accepts a host-provided Unreal
`llm.Adapter`. `BuilderFactory` and `RegistryFactory` are seams for composing context and the tool
registry. These types live below Go's `internal/` boundary; they are not a stable external Go package
or a runtime plugin API. A custom registry still needs operations the selected operation manager can
execute. Adding a tool with a new side effect also requires a compatible durable operation path.

## Supported ways to extend a user setup

Add instructions and repeatable workflows as skills. For example, create
`~/.codex/skills/review-pr/SKILL.md` and pk can discover it on the next new session. In direct mode,
`--skills-dir PATH` selects one or more custom roots instead of the defaults. Existing sessions keep
the skill content captured in their prefix snapshot.

Choose the model and effort in config, from a CLI override, or in the TUI pickers:

```sh
pk config set model gpt-6-luna
pk config set effort medium
pk task create --workspace ../pk-review --model gpt-6-luna --effort high \
  -p "Review the code and summarize the highest-risk issues."
```

The default model is `gpt-6-luna`; the default effort is `medium`. The model must support the chosen
effort. The task flags set values for that task and leave the saved defaults alone.

## Local state boundaries

By default, pk stores its own config and credentials in `~/.pk`, session history in
`~/.pk/sessions`, and detached task manifests/events/logs in `~/.pk/tasks`. `PK_HOME` moves all of
these together. Workspace files remain in the selected workspace. The Bash operation runs with the
host user's permissions; workspace selection is not a sandbox.
