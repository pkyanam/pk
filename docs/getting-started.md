# Getting started

`pk` needs Go 1.27 or newer to build, Bun to build and run its OpenTUI interface, and network access
for login and model requests.

## Build and install

Clone the repository and run the installer:

```sh
git clone https://github.com/pkyanam/pk.git
cd pk
./scripts/install
```

The installer uses the frozen `ui/bun.lock`, builds the OpenTUI frontend and Go executable, then
installs them under `~/.local/bin/pk` and `~/.local/lib/pk/ui`. Add `~/.local/bin` to your `PATH` if
needed. Set `PK_BIN_DIR` or `PK_LIB_DIR` to install elsewhere. For a repo-local build, run
`./scripts/build`; it writes `bin/pk` and `ui/dist/main.js`.

## Sign in

Connect pk to ChatGPT:

```sh
pk login
pk status
```

Device-code login must be enabled in your ChatGPT security settings or by your workspace
administrator. Follow the URL and one-time code shown by pk. Credentials go to `~/.pk/auth.json` or
`$PK_HOME/auth.json` if `PK_HOME` is set. pk creates its private directory with mode `0700` and the
credential file with mode `0600`. `pk status` shows account and expiry information without printing
tokens; `pk logout` removes only pk's own saved credentials.

pk does not read Codex CLI credentials by default. For a one-shot command, `--use-codex` explicitly
reads `$CODEX_HOME/auth.json` (or `~/.codex/auth.json` when `CODEX_HOME` is unset) in read-only mode:

```sh
pk run -p "Summarize this repository" --use-codex
```

If that token has expired, refresh it with `codex login`. pk does not refresh or delete it.

## Choose defaults

The initial defaults are `gpt-6-luna` and `medium` reasoning effort. Inspect or change them with:

```sh
pk config show
pk config set model gpt-6-luna
pk config set effort medium
```

These defaults are saved to `~/.pk/config.json` (or `$PK_HOME/config.json`) and are used by the TUI
and by new detached tasks. A task's `--model` and `--effort` flags override the defaults for that
task. The TUI's `/model` and `/effort` pickers update the saved defaults. Valid reasoning efforts
are `low`, `medium`, `high`, `xhigh`, and `max`; the selected model must accept the chosen effort.

## Use the OpenTUI session

Run `pk` from the project directory. That directory is the workspace for the session:

```sh
cd /path/to/project
pk
```

Type a prompt and press Enter to submit. Shift-Enter or Ctrl-J adds a line. Ctrl-P opens the slash
command menu; type to filter, use Up/Down to move, Tab to complete, and Enter to select. The menu
contains `/model`, `/effort`, `/tasks`, `/task`, `/new`, `/attach SESSION_ID`, `/detach`, `/cancel`,
`/status`, `/login`, `/help`, and `/exit`. `/tasks` opens the detached task picker. Use
`/attach SESSION_ID` to return to a saved conversation.

Esc closes an open picker or stops the active turn. Ctrl-D detaches from an idle session and closes
the interface while preserving its durable conversation; with no session it closes pk. Ctrl-C
closes pk when idle. Finish or cancel a turn before detaching. Use `/attach SESSION_ID` to return to
a saved conversation. In plain mode, use `pk --plain --session SESSION_ID`.

For a one-shot request, use `pk -p "Summarize this project"`. It accepts the same model, effort,
workspace, session, system-instruction, and credential flags as `pk run`:

```sh
pk run -p "Summarize this project" --workspace /path/to/project --model gpt-6-luna --effort medium
```

For the line-oriented interface without OpenTUI, run `pk --plain`. Add `--session SESSION_ID` to
continue a session. Its commands are `/help`, `/exit`, and `/quit`; Ctrl-C cancels the current turn
and exits. Use `pk run -p PROMPT --jsonl` for assistant and tool-call events as JSON lines.

## Run a detached task in a new workspace

From the TUI, `/task new PROMPT` creates a fresh `pk-work/task-*` workspace below the current project.
To choose another destination, use `/task new --workspace "/path with spaces" PROMPT`. `/tasks`
opens the task picker; select a task and press Enter to follow its output. While attached, enter a
follow-up prompt to steer the task. Use `/task resume ID` for an interrupted task and `/task cancel
ID` to stop its worker.

From a shell, `pk task create` starts a worker process and returns immediately. If the workspace path
does not exist, pk creates it. This example creates a separate workspace directory for a longer
coding task:

```sh
pk task create --workspace ../pk-fix -p "Inspect the failing tests, implement a fix, and summarize the changes."
```

The command prints the task ID, current status, and workspace. Use that ID to inspect or follow it:

```sh
pk task list
pk task status TASK_ID
pk task attach TASK_ID
```

`attach` streams stored output until the task finishes. Ctrl-C stops following and leaves the worker
running. Cancel the worker explicitly with `pk task cancel TASK_ID`; cancellation sends a graceful
termination signal. To resume an interrupted, canceled, or failed task, run `pk task resume TASK_ID`.
Resume refuses to launch while the existing worker is still alive. It continues the durable session
with the original request and asks the model not to repeat completed work, but interruption recovery
cannot guarantee that an interrupted external side effect was not already performed.

Task manifests, event streams, and worker logs live under `~/.pk/tasks` (or `$PK_HOME/tasks`). The
associated model session lives under `~/.pk/sessions` (or `$PK_HOME/sessions`). The workspace is an
operating location, not a process sandbox.

## Skills and workspace instructions

The built-in tools are **Bash**, **ViewImage**, and **SkillUse**. Bash runs with the permissions of the
pk process and is not confined to the workspace. ViewImage lets the model inspect images. SkillUse
loads a registered skill.

By default pk discovers skills below `~/.codex/skills` and `~/.agents/skills`. For direct runs, pass
one or more `--skills-dir DIR` flags to use custom directories instead of those defaults. A skill is
a directory containing `SKILL.md`; for example:

```text
my-skills/
└── review-pr/
    └── SKILL.md
```

The root workspace's `AGENTS.md` is included in instructions when it is at most 64 KiB. For nested
paths, the agent is told to inspect the nearest nested `AGENTS.md`; pk does not load nested files
automatically. Session prefix snapshots preserve the skill text and tool schemas used when the
session was created, so editing a skill later does not silently change an existing session.

## Local data and cache visibility

pk stores prompts, responses, tool calls, and results in local durable session history. Task state and
worker output are also kept locally. Set `PK_HOME` to move pk's auth, config, task, and session data.

Responses API prompt caching is implicit and controlled by the provider. pk preserves a stable
session prefix and supplies the session ID as the cache key; this improves the chance of reuse but
does not guarantee a cache hit. `pk run --jsonl` reports per-response usage fields including cached
input and cache-write token counts when the provider supplies them. Availability flags distinguish a
reported zero from an unavailable count. OpenTUI shows the most recent cached-input count when
available and displays `cache —` when the provider does not report it. See
[Architecture](architecture.md) for the event fields and what they cannot prove.
