# Extension host design

## Design goal

`pk` should make it easy to add useful capabilities without tying extensions to one Go compiler build or importing extension code into the agent process. The initial implementation is a versioned JSON Lines protocol to an explicitly configured child process. The host owns model-facing registration, lifecycle, cancellation, timeouts, output limits, and error reporting. A worker implements declared tools and commands.

This is a portability and failure-isolation choice, not a security sandbox. A subprocess launched as the current user can still read files, use credentials, start processes, and access the network unless the operating system separately restricts it. Extensions are trusted executable code. A manifest's capability list describes the extension's intended interface and lets the host reject unsupported integrations; it does not constrain arbitrary syscalls.

## What Pi provides

Pi's current extension guide describes TypeScript modules with tools, commands, lifecycle event handlers, providers, session state, and TUI components. Its extension API can intercept tool calls and transform context, and the guide distinguishes actionable boundaries such as `agent_before_settle` from the notification-only `agent_settled`. Tool calls in a response may execute in parallel, and extension handlers have explicit concurrency and cleanup guidance. These are real strengths to match at the host API level, not claims that Pi lacks extensibility. Pi extensions run in the Pi process with the same OS permissions as the user, so Pi also treats loading them as a trust decision. See the [current extension guide](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/extensions.md), [extension API types](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/src/core/extensions/types.ts), and [loader implementation](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/src/core/extensions/loader.ts). The repository is currently served as `earendil-works/pi`; the earlier `badlogic/pi-mono` URL redirects there.

## Host boundary

The runtime host receives manifest paths from explicit user configuration or command-line options. It does not scan the active workspace for extensions or auto-run files found there. Separately, the plugin source manager can inspect a user-selected local repository or GitHub source for supported manifests, show candidate metadata, and install a selected component into pk's managed plugin directory. Discovery and candidate review do not start workers; installation is explicit, and an installable candidate may build its declared Go worker. Candidates requiring an unsupported build remain review-only. Loading one enabled extension must not prevent other extensions or the agent from starting. Invalid manifests, duplicate IDs, conflicting names, a failed handshake, or a dead worker disable that extension and return a diagnostic in the host report.

Each manifest has a stable ID, semantic extension version, protocol version, executable plus arguments, and declarations for tools, commands, and capability names. Tool declarations include a JSON Schema parameter object. Protocol v1 supports explicitly negotiated metadata-only lifecycle observers. Transform hooks are rejected.

Registration is deterministic. The host loads manifests in caller-supplied order. Tool names are global: built-in names always win, and a tool collision disables the conflicting extension with an error naming both owners. Slash commands are instead namespaced by extension ID under the reserved `/ext:` prefix, so the same leaf command name can be declared by multiple extensions without colliding with built-in commands. Duplicate extension IDs are rejected. The host never silently replaces an existing integration.

## Wire protocol

The worker's standard input and standard output are UTF-8 JSON Lines. Standard output is protocol-only; worker logs go to standard error. Each request has an ID, method, and JSON params. Each response echoes that ID and contains exactly one of a result or an error object. The first exchange is `initialize`, which sends the supported `api_version`, extension ID, working directory, and declared capabilities; a worker responds with its protocol version, ID, and implemented tool/command names. A mismatch disables only that worker.

Tool calls use `tool.execute` with the registered name, stable call ID, raw JSON arguments, and workspace path. Command calls use `command.execute` with the registered command name and text arguments. The response contains a result value or a typed error. The Go host validates IDs, declared names, JSON values, and response sizes before returning data to the agent.

Requests to each worker are serialized. The runner submits a tool call as an Unreal remote-job operation, so the coordinator can report it as running while its child process executes. A per-call deadline bounds a stuck worker. Cancellation terminates that worker process; on Unix-like systems the host kills its process group, including descendants. Windows v1 currently guarantees termination of the worker executable but does not claim descendant cleanup. Other workers continue. Workers can opt into the bounded progress notifications described below.

## Go integration

The host exposes validated tool definitions and a registry decorator. The decorator implements the pinned Unreal `tool.Registry` seam; its translator submits a typed `operation.RemoteJobSpec`, and a run-scoped `operation.RemoteJobHandler` invokes the worker asynchronously and stores its terminal result in the operation log. Runtime worker failure leaves the registered schema frozen for that run and returns a terminal tool error. It must not change the model's available tool prefix midway through a session.

Commands are exposed as a separate host API for a CLI/TUI command registry. `Host.SlashCommands()` returns loaded declarations in deterministic order without invoking them; `Host.ExecuteSlashCommand(ctx, name, args)` runs only the explicitly selected command. The public invocation key is namespaced as `/ext:<extension-id>:<command-name>`, so extensions may reuse leaf names without shadowing built-in commands. The existing `Commands()` and `ExecuteCommand()` leaf-name methods remain available for compatibility; the latter rejects ambiguous leaf names. Command arguments are limited to 16 KiB of UTF-8 text and returned text to 64 KiB; the host rejects oversize values instead of truncating them. Listing declarations never runs a command, although creating a host from explicitly selected manifests starts and handshakes their workers.

Lifecycle observers receive the run metadata described below. They cannot transform model context or return changes to the transcript. Mutating hooks and context transformations remain unimplemented; any future interface needs explicit, validated patches and separate evaluation.

The host is constructed with a caller-owned context and closed when that session/run ends. This lets cancellation reach all workers without changing the runner API. Do not create workers for headless or detached operation unless those hosts explicitly configure extensions.

## Prototype scope and limits

`internal/extensions` provides the versioned manifest and wire types, explicit manifest loading, a process worker, deterministic conflict handling, typed tool and command dispatch, a namespaced slash-command discovery/invocation API, an async registry decorator, subprocess lifecycle tests, and in-process host tests. The sample `workspace_stats` tool and `stats` command count files and bytes without changing the workspace; the host exposes that command as `/ext:workspace-stats:stats`.

The prototype uses bounded calls and durable remote-job operations. It does not implement mutating event hooks, dynamic reload within an existing session, persistent extension state, privilege reduction, a sandbox, or automatic project-local extension loading. Repository-source discovery and managed package installation are supported; an installable candidate is selected explicitly after metadata review, and unsupported build requirements are shown without executing the candidate. Installed plugins are managed in `/plugins`; `/commands` lists namespaced commands from the session's frozen enabled-manifest set. Choosing a command inserts it into the composer, and submitting it invokes only that command with the remaining text as arguments. `/cancel` or Escape requests cancellation while it runs. Browsing candidates and listing commands do not execute plugin tools or commands. The sample worker demonstrates the actual process protocol; in-process fakes cover registration and host behavior, not the security of arbitrary executable extensions.

### Worker progress notifications

The host advertises `tool_progress` in `host_features` during `initialize`. A worker can then send a JSONL notification during `tool.execute`, using that request's transport ID:

```json
{"id":"request-42","method":"tool.progress","params":{"text":"Fetched 2 of 5 pages"}}
```

The in-repository Go worker helper is `extensions.ReportProgress(ctx, "Fetched 2 of 5 pages")`; a `false` return means progress was unavailable or dropped, so the worker should continue its work without it. The host sanitizes and bounds text to 4 KiB, accepts at most 64 notifications per call, and ignores notifications with stale request IDs. The host and RPC/UI relay queues are asynchronous and bounded; overflow drops updates to keep the progress callback off the worker execution path. RPC output still shares the normal UI transport. The UI shows progress only on the active tool row. These transient updates are not added to the model context or saved history. Avoid putting secrets in progress text: sanitization covers common credential patterns, not every possible secret.

Workers that do not use the negotiated feature remain compatible and return their ordinary final response. Installed in `71170b9`; the extension and RPC race suites plus all 115 UI tests passed before activation.

### Lifecycle observers

A manifest can opt into one or more metadata events:

```json
"hooks": [
  {"event": "run_start", "mode": "observe"},
  {"event": "response_complete", "mode": "observe"},
  {"event": "run_end", "mode": "observe"}
]
```

The host offers `lifecycle_notifications` in `host_features`; the worker must return it in `initialize.features` when declaring hooks. Existing workers without hooks remain compatible. Go workers implement `LifecycleObserver.NotifyLifecycle(context.Context, LifecycleEvent) error` alongside the ordinary handler interface.

`run_start` and `run_end` delimit one runner invocation, including an invocation that resumes a saved session. They are not session creation/deletion events. `response_complete` means a model response has been persisted: asynchronous tools may still be running, so it does not imply an idle agent or a finished user task. Events contain only type, run ID, session ID, model, workspace, and status. They omit prompts, tool arguments/results, credentials, and transcript text.

Process workers receive one-way `lifecycle.notify` frames, separate from tool request/response IDs. The SDK dispatches callbacks asynchronously; workers sharing state between tools and observers must synchronize it themselves. Queues are bounded to 16 entries at each stage and drop on pressure. Shutdown permits a short, bounded transport drain; delivery and callback completion are not guaranteed, especially during cancellation or worker failure. These events are suitable for optional integrations, not transactional accounting. They never enter model context or change the frozen tool prefix.

## How to claim an advantage

“Better than Pi” is a testable hypothesis. Compare the same extension tasks, schemas, behavior, and lifecycle requirements against a pinned Pi release. Record extension load time, cold and warm tool latency, resident memory, process count, bytes transferred, cancellation latency, recovery after worker crash, and successful task outcomes. Include a tool, command, observer hook, mutating hook, context transform, and an unresponsive worker. Report differences and failures. Do not claim lower cost, better safety, or greater extensibility from this prototype alone.
