<div align="center">
  <h1>pk</h1>
  <p>Keep coding work moving.</p>
  <p>
    <a href="https://github.com/pkyanam/pk/actions/workflows/ci.yml"><img alt="CI" src="https://github.com/pkyanam/pk/actions/workflows/ci.yml/badge.svg"></a>
    <img alt="Go 1.27+" src="https://img.shields.io/badge/Go-1.27%2B-00ADD8?logo=go&logoColor=white">
    <img alt="OpenTUI 0.5.12" src="https://img.shields.io/badge/OpenTUI-0.5.12-6E56CF">
  </p>
  <img src="docs/assets/pk-coding-session.png" alt="pk completing a real Go coding task in Cmux" width="720">
  <p><sub>Real pk session · a focused fix with passing tests</sub></p>
</div>

pk is a terminal coding workspace built on [Unreal Agent v0.1.1](https://github.com/unreallabsai/unreal-agent/tree/v0.1.1). Work in a live OpenTUI session, or hand a longer job to a durable task and come back to its progress.

## Get started

```sh
curl -fsSL https://raw.githubusercontent.com/pkyanam/pk/main/install.sh | bash
pk login
cd /path/to/project
pk
```

The installer requires Git, Bun, and Go 1.21+; Go selects the source module's Go 1.27 toolchain
according to your `GOTOOLCHAIN` setting. It builds from a temporary source checkout, preserves an
existing managed release for rollback, and honors `PK_BIN_DIR` and `PK_LIB_DIR`. It does not install
system packages.

For work that should keep running after you leave the interface:

```sh
pk task create -p "Implement the requested change and run the relevant checks."
pk task list
pk task attach TASK_ID
```

Tasks have durable output and can be attached to, steered, canceled, or resumed. CLI-created tasks use a fresh workspace under `~/.pk/workspaces`; choose another with `--workspace DIR`.

## A workspace for longer coding work

- **See what is happening.** Follow assistant progress and live tool activity, inspect completed tool output, and answer clarification questions during foreground work.
- **Keep work moving.** Delegate bounded subtasks to child agents (using Luna by default), or detach a task and return to its saved progress later.
- **Bring your setup.** Use discovered skills, explicitly enabled plugins, and configured MCP servers. pk can also run as an ACP agent for compatible editors and clients.
- **Bring files into the task.** Attach images, text, and PDFs. Optional local Poppler previews let the model inspect scanned pages through `ViewImage`. See [file support and limits](docs/attachments-design.md).
- **Choose a model connection.** ChatGPT/Codex is the built-in provider. Add OpenAI-compatible Responses or Chat Completions endpoints, inspect their model lists, and select a provider for new sessions.
- **Know what a session uses.** `/usage` shows saved token and cache totals when the provider reports them, without another model request. Context snapshots preserve the instructions, skills, and tools a session started with.
- **Update from the terminal.** Run `pk update` or use `/update`, then roll back if needed. Selecting transcript text copies it when your terminal supports clipboard writes; Ctrl-Y is the fallback.
- **No first-party telemetry.** pk operates no usage-analytics or crash-reporting service; provider and enabled-tool requests happen when you use them. See [privacy and data handling](docs/privacy.md).

The default model is `gpt-6-luna` with `medium` reasoning effort. Change it with `pk config set model MODEL` and `pk config set effort EFFORT`. The default context policy is `full`; `pk config set context-policy compact` opts into compacting large completed Bash results in the model-visible context while preserving their local captures. This policy is snapshot-bound for a session and does not promise lower total usage.

pk builds on Unreal Agent's asynchronous tool coordinator and can keep full local shell captures while compacting large result text sent back to the model when explicitly enabled. Prompt caching is provider-controlled; reported counters are actual provider usage, and a cache hit is never guaranteed. See the [verified runtime foundation](docs/unreal-foundation-audit.md). A small paired coding-task pilot is documented in [benchmark findings](docs/benchmark-findings.md); it is exploratory and does not establish a general performance advantage.

**Learn more:** [Getting started](docs/getting-started.md) · [Providers](docs/providers.md) · [Available tools](docs/toolbelt.md) · [MCP](docs/mcp.md) · [ACP](docs/acp.md) · [Validation](docs/validation.md)

**Permissions:** Bash uses the operating-system permissions of pk. A workspace organizes files; it does not sandbox the process.
