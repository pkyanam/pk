<div align="center">
  <h1>pk</h1>
  <p>Keep coding work moving—from a focused terminal session to a task that runs in the background.</p>
  <p>
    <a href="https://github.com/pkyanam/pk/actions/workflows/ci.yml"><img alt="CI" src="https://github.com/pkyanam/pk/actions/workflows/ci.yml/badge.svg"></a>
    <img alt="Go 1.27+" src="https://img.shields.io/badge/Go-1.27%2B-00ADD8?logo=go&logoColor=white">
    <img alt="OpenTUI 0.5.12" src="https://img.shields.io/badge/OpenTUI-0.5.12-6E56CF">
  </p>
  <img src="output/imagegen/pk-welcome-dark.png" alt="pk interface design concept" width="720">
  <p><sub>Design preview · screenshot coming soon</sub></p>
</div>

## Start in a project

```sh
git clone https://github.com/pkyanam/pk.git
cd pk
./scripts/install       # requires Go 1.27+ and Bun
pk login
cd /path/to/project
pk
```

`pk` opens a native [OpenTUI](https://opentui.com/) coding session. For long work, start a detached
task in a fresh workspace; it keeps running when you close pk. The TUI can attach and send follow-up
input, while the CLI can follow saved output.

```sh
pk task create -p "Implement the requested change and run the relevant checks."
pk task list
pk task attach TASK_ID
```

CLI tasks get a fresh workspace under `~/.pk/workspaces`. Choose another with `--workspace DIR`.
Inspect with `pk task status TASK_ID`, stop with `pk task cancel TASK_ID`, or retry unfinished work
with `pk task resume TASK_ID`.

## Runtime foundation

pk builds on [Unreal Agent v0.1.1](https://github.com/unreallabsai/unreal-agent/tree/v0.1.1). Its
coordinator runs independent tool operations asynchronously, records their progress, and waits for
events instead of polling. It preserves a stable conversation prefix, loads skill instructions on
demand, and keeps full shell captures while bounding the output sent back into the conversation.
Its stable session cache key and response usage fields make provider-reported cache reuse visible;
they do not promise a cache hit.

## pk adds

- An OpenTUI frontend and a Go CLI for conversations, settings, and task control.
- Detached workers with isolated workspaces and durable status/output, attach, cancel, and resume.
- Context snapshots that keep workspace instructions, tool schemas, and skill text consistent when
  a session resumes.
- Provider-reported cached-input counts in the TUI and usage events.

The default is `gpt-6-luna` with `medium` reasoning effort. Change defaults with `pk config set model
MODEL` and `pk config set effort EFFORT`.

**More:** [Getting started](docs/getting-started.md) · [Architecture and cache counters](docs/architecture.md) ·
[Available tools](docs/toolbelt.md) · [Validation](docs/validation.md) · [Design concepts](docs/design/tui-concepts.md)

**Safety:** Bash uses the operating-system permissions of pk. A workspace organizes work; it does
not sandbox the process.
