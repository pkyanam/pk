# pk

`pk` is a small Go coding agent for terminal sessions backed by a durable Go harness. It is an early
alpha. Install it, enter a project directory, and start an interactive session:

```sh
go install ./cmd/pk
cd /path/to/project
pk login
pk
```

`pk` uses the current directory as its workspace. Enter a prompt at `pk> `; each line continues the
same session. Use Ctrl-D or `/exit` to leave. Ctrl-C hard-stops active work and exits, leaving its
session history available for resume with `pk --session ID`. For a one-shot prompt, use
`pk -p "Summarize this project"`.

## Install

Use Go 1.27 or newer. To install this checkout into your Go binary directory:

```sh
go install ./cmd/pk
```

See [Getting started](docs/getting-started.md) for login, credential reuse, session resume, tools, and
workspace instructions.

## Credentials and privacy

`pk login` uses the Codex device authorization flow and stores pk's credentials in its own file:
`~/.pk/auth.json`, or `$PK_HOME/auth.json` when `PK_HOME` is set. The containing directory is
created with mode `0700`, and the credential file is written with mode `0600`.

Existing Codex CLI credentials are not read by default. `pk run --use-codex ...` explicitly reads
the existing Codex auth file in read-only mode; pk does not refresh, rewrite, or delete it. `pk logout`
removes only pk's credential file. `pk status` reports whether pk is logged in and, when it is, the
account identifier and expiry; it reports an expired login and tells you to reconnect. It does not
display tokens. pk refreshes its own credentials as needed. Credentials selected with `--use-codex`
remain read-only and must be refreshed separately with `codex login` if expired.

Prompts, model responses, tool calls, and results are stored in local durable session history. The
built-in Bash tool runs commands with the operating-system permissions of the pk process. pk does not
provide an OS sandbox or restrict that process to the workspace. Review prompts and tools accordingly,
especially when reusing credentials.

## Current scope

The built-in tool set is Bash, ViewImage, and SkillUse. SkillUse discovers skills in `~/.codex/skills`
and `~/.agents/skills` by default; `-skills-dir DIR` selects one or more custom skill directories. pk
uses the Unreal Agent runtime, pinned at v0.1.1 in `go.mod`, for session coordination and storage. See
[the toolbelt notes](docs/toolbelt.md) for which tool ideas are available and which remain proposals.

This alpha exposes one terminal session at a time. There is no separate session browser or
cancellation subcommand yet.

Thanks to Unreal Labs for [Unreal Agent](https://github.com/unreallabsai/unreal-agent), distributed under the [MIT License](https://github.com/unreallabsai/unreal-agent/blob/v0.1.1/LICENSE).
