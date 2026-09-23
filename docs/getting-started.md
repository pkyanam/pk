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

## Update an installed release

From an installed `pk`, select a pk source checkout and run:

```sh
pk update --source /path/to/pk
pk version
```

The updater validates the checkout, runs Go and OpenTUI checks/builds in a staged copy, then activates
a paired release containing both the Go binary and UI assets. `pk rollback` restores the previous
release. Without `--source`, `pk update` fetches the official [pk GitHub repository](https://github.com/pkyanam/pk)
on `main`; use `--source /path/to/pk` to update from a local checkout. The TUI also supports
`/update [--source PATH]`, `/rollback`, and `/reload`; bare `/update` uses the same GitHub default. Update and
rollback require an idle foreground session. After the update succeeds, `/reload` restarts the
supervised process and restores the current session. It preserves that session's saved prompt, tools,
and skills; use `/new` to start with the current instructions and tool configuration, or `/attach ID`
to continue an older saved session. If the terminal does not relaunch cleanly, exit and run `pk` again.

New sessions load the current prompt and skill files. Existing sessions retain their saved prompt,
tools, and skill contents; attaching does not refresh them. Use `/new` to pick up an updated bundled
skill or prompt, or `/attach SESSION_ID` to continue with the saved context.

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
pk config set context-policy full
```

`full` is the default and sends full completed Bash results in context. To opt into compacting large completed Bash results while keeping their local captures available, use `pk config set context-policy compact` or pass `--context-policy compact` to `pk run`. This policy can change token use or task behavior; a small exploratory pilot does not establish a general savings claim. A saved session keeps the context policy from its original snapshot, so resume it with the same policy or start a new session to adopt a different one.

These defaults are saved to `~/.pk/config.json` (or `$PK_HOME/config.json`) and are used by the TUI
and by new detached tasks. A run's `--model`, `--effort`, and `--context-policy` flags override the defaults for that
run. The TUI's `/model` and `/effort` pickers update the saved defaults. Valid reasoning efforts
are `low`, `medium`, `high`, `xhigh`, and `max`; the selected model must accept the chosen effort.

## Use the OpenTUI session

Run `pk` from the project directory. That directory is the workspace for the session:

```sh
cd /path/to/project
pk
```

Type a prompt and press Enter to submit. Shift-Enter or Ctrl-J adds a line without submitting.
Ctrl-P opens the slash command menu; type to filter, use Up/Down to move, Tab to complete, and
Enter to select. Ctrl+O toggles the latest tool details. Use `/help` for the full command list; common
commands include `/model`, `/effort`, `/sessions`, `/tasks`, `/new`, `/attach SESSION_ID`, `/history`,
`/usage`, `/tools`, `/update`, `/reload`, `/cancel`, and `/exit`. `/sessions` browses saved
conversations; `/tasks` opens the detached task picker.

During longer work, pk shows a live wait/run state and elapsed time, including while waiting for a
model response. Assistant progress messages appear in order alongside tool activity. Tool rows
update as operations run and finish, with bounded command/argument previews, elapsed time, and
available shell result excerpts. Enabled extensions that support negotiated progress can also show
transient updates on their active tool row; these are best-effort, may be dropped under load, and are
not added to model context or saved history. The UI redacts common credential-shaped values in these
previews; do not treat the preview filter as a complete secret detector. Use `/new` to start a fresh
conversation with current system instructions and skills. Attaching an older session retains its
saved instruction and tool/skill snapshot. See the [prompt and tool reference](prompt-reference.md)
for the full prompt template and default TUI tool schemas, including the installed model-aware
identity. Use `/new` to adopt the updated prompt; legacy sessions keep their saved prompt.

When a foreground session needs a user decision, `AskUser` presents its choices above the composer;
select with Up/Down and Enter, or type a freeform answer. Escape cancels the pending question and the
current turn. This is a clarification flow, not a tool permission gate. Detached tasks do not expose
model-generated questions, so include necessary choices in their initial prompt or steer them while
attached.

To attach explicitly selected files to a foreground prompt, queue a path with `/file` (quote a path
containing spaces). You can also enter a bare path and press Enter to send it with the default
request “Inspect the attached file,” or put a path first and add your request after it, such as
`notes.txt describe this`. Terminal paste routes recognized file paths and file URIs into the
attachment queue. Native drag/drop has not been verified yet.

```text
/file /tmp/report.pdf
/file "/path with spaces/weekly report.pdf"
/files
/files remove 1
/files clear
/paste
```

PDFs use bounded local text extraction, not native PDF upload; scanned pages are not OCRed. See the
[attachment guide](attachments-design.md) for the current preview fallback and its installation and
resource limits.

`/files` lists queued paths. Click a visible file chip to remove it, or use `/files remove N` with
its 1-based number. A prompt sends the queued files and shows a loaded notice; the queue clears only
after the host confirms loading succeeded. If the prompt fails, the queued files remain available
for retry. The effective limit is 8 files per prompt. Ctrl+V invokes clipboard import; Cmd+V can
invoke it when the terminal delivers that key to pk, and `/paste` is the explicit fallback when it
does not. Both Finder-file Cmd+V of a spaced image path and Preview raw-pixel Cmd+V reached
`ViewImage` in Cmux; other terminals may behave differently. Bracketed paste recognizes file URI
lists and quoted/escaped file paths.

Double-click selects transcript text; Ctrl+Y copies the selection through OSC 52. Some terminals
intercept Cmd+C, so Ctrl+Y is the reliable in-app copy shortcut. This is explicit path selection,
not a file browser; relative paths use the session workspace, while explicitly selected absolute
paths may be outside it. Attachments belong to the foreground prompt draft: starting a detached task
does not transfer or consume the queued files. See [attachment formats and bounds](attachments-design.md).

`/usage` opens a local view of provider-reported token totals saved for the current session. It makes
no provider request and shows per-metric response coverage so unavailable counts are not mistaken
for zero.

Esc closes an open picker or stops the active turn. Ctrl-D detaches from an idle session and closes
the interface while preserving its durable conversation; with no session it closes pk. Ctrl-C
closes pk when idle. Finish or cancel a turn before detaching. Use `/attach SESSION_ID` to return to
a saved conversation. In plain mode, use `pk --plain --session SESSION_ID`.

For a one-shot request, use `pk -p "Summarize this project"`. It accepts the same model, effort,
workspace, session, system-instruction, and credential flags as `pk run`:

```sh
pk run -p "Summarize this project" --workspace /path/to/project --model gpt-6-luna --effort medium
```

For a one-shot request, `--extension` can load an explicitly named manifest, and `--image-driver`
adds the separate ImageGen tool:

```sh
pk -p "Inspect the repository" --extension /path/to/manifest.json
pk -p "Create a small mint cursor image" --image-driver gpt-6-astra
```

Repeat `--extension` to load multiple manifests. These one-shot options change the tool schema and
cannot be combined with `--session`. For interactive sessions, use the plugin browser described
below. Plugins can provide tools and namespaced slash commands; their worker programs run with pk's
OS permissions. The ImageGen driver is separate from the chat model, and the verified path uses the
Astra driver with ChatGPT authentication; this does not mean Luna generates images natively. See
[extension design](extensions-design.md) for the protocol and its limits.

For the line-oriented interface without OpenTUI, run `pk --plain`. Add `--session SESSION_ID` to
continue a session. Its commands are `/help`, `/exit`, and `/quit`; Ctrl-C cancels the current turn
and exits. Use `pk run -p PROMPT --jsonl` for assistant and tool-call events as JSON lines.

## Run a detached task in a new workspace

From the TUI, `/task new PROMPT` creates a fresh `pk-work/task-*` workspace below the current project.
To choose another destination, use `/task new --workspace "/path with spaces" PROMPT`. `/tasks`
opens the task picker; select a task and press Enter to follow its output. While attached, enter a
follow-up prompt to steer the task. Keep detached task prompts self-contained: they have no durable
request/reply flow for a model-generated blocking question.
Use `/task resume ID` for an interrupted task and `/task cancel ID` to stop its worker.

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

The core foreground tools are **Bash**, **ViewImage**, **SkillUse**, and **AskUser**. Parent runs also
have tools for starting, steering, checking, waiting for, and canceling child agents. Bash and child
agents run with the permissions of the pk process; neither is confined to the workspace. ViewImage
lets the model inspect images, and SkillUse loads a registered skill. Configured MCP servers and
enabled plugins can add tools to a new session. ImageGen is an optional tool with a driver model
separate from pk's chat model; it is off by default. In the TUI, use `/image` to enable or disable
it, then start a new session for the change to apply. The default driver when enabled is
`gpt-6-astra`. From a shell, `pk config set image-driver gpt-6-astra` enables it persistently and
`pk config set image-driver off` disables it. One-shot `--image-driver MODEL` overrides remain
available. Image generation uses the Codex CLI and its existing ChatGPT authentication.

Use `/skills` to browse available skills and insert one into the prompt. Use `/plugins` to list
installed plugins or discover a source. To add one, open the plugin source prompt and enter a
`OWNER/REPO`, GitHub URL, or local repository path, then inspect a candidate and choose **Install
and enable**. Browsing only reads supported manifest metadata; it does not start plugin workers.
Installation is an explicit trust decision: pk copies the selected component into its managed
plugin directory and may build its declared Go worker. Candidates marked **build required** can be
reviewed but are not installable from the picker yet. You can also use `pk plugin discover SOURCE`
to inspect candidates or `pk plugin add --id ID SOURCE` to install and enable one from a shell.

In the `/plugins` list, select an installed plugin and press Enter to toggle it. From a shell, use
`pk plugin disable ID` or `pk plugin enable ID`. The lower-level `/plugin enable "MANIFEST_PATH"`
and `/plugin disable ID` commands are also available. Changes to plugin tools and commands apply to
new sessions, so use `/new` after installing, enabling, or disabling a plugin. `/commands` browses
enabled plugins' namespaced slash commands. `pk plugin list` shows the installed plugin catalog.
Plugins run with pk's OS permissions; inspect a candidate before installing it.

By default pk discovers skills below `~/.codex/skills`, `~/.agents/skills`, and its managed
`~/.pk/skills` directory. For direct runs, pass
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
