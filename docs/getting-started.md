# Getting started

You need Go 1.27 or newer to build pk and network access for device login and model requests.

## Install

From the repository root, install pk into your Go binary directory:

```sh
go install ./cmd/pk
```

Put `GOBIN` or `$(go env GOPATH)/bin` on your `PATH`. To build a local binary instead:

```sh
mkdir -p ./bin
go build -o ./bin/pk ./cmd/pk
```

The commands below use `pk`; use `./bin/pk` when running the local binary.

## Sign in

Start pk's native device login:

```sh
pk login
```

Device code login must be enabled in your ChatGPT security settings, or by your ChatGPT workspace
administrator. See [OpenAI's device code login instructions](https://learn.chatgpt.com/docs/auth#preferred-device-code-authentication-beta).
Then open the displayed URL, enter the one-time code, and approve only the login you initiated in pk.
Credentials are saved to `~/.pk/auth.json`. If `PK_HOME` is set, pk uses `$PK_HOME/auth.json`
instead. pk creates the parent directory with mode `0700` and writes the credential file with mode
`0600`.

Check the saved credential state without making a network request:

```sh
pk status
```

Status reports whether pk is logged in and, when it is, the account identifier and expiry. If the
login has expired, it says so and directs you to reconnect with `pk login`. Status does not print
tokens. pk refreshes its own credentials as needed. To remove pk's own saved credentials, run
`pk logout`.

By default pk does not read the Codex CLI's credentials. To explicitly use the existing Codex login
for a run, add `--use-codex`:

```sh
pk run -p "Summarize the repository" --use-codex
```

This reads the existing Codex auth file at `$CODEX_HOME/auth.json`, or `~/.codex/auth.json` if
`CODEX_HOME` is unset, without refreshing or modifying it. An alternate file can be selected with
`--codex-auth-file PATH` (it requires `--use-codex`). If that access token is expired, refresh it
with `codex login`, then retry. `pk logout` never removes Codex CLI credentials.

## Start an interactive session

Change to the project directory you want pk to work in, then launch it with no arguments:

```sh
cd /path/to/project
pk
```

The current directory becomes the workspace. Enter prompts at `pk> `; each line is a turn in the same
durable session. pk prints the new session ID to stderr. Tool start, finish, and failure notices also
go to stderr, leaving assistant replies on stdout. Enter `/help` for the prompt commands. Ctrl-D or
`/exit` leaves normally. Ctrl-C hard-stops current work and exits with status 130; save the session ID
to resume the interactive session later:

```sh
pk --session "your-session-id"
```

Replace `your-session-id` with the ID printed by pk.

To ask one prompt and exit, use the `-p` convenience form. It accepts the same flags as `pk run`:

```sh
pk -p "Summarize the project" -model gpt-6-astra -effort xhigh
```

For a one-shot follow-up instead of reopening the prompt, use `pk run`:

```sh
pk run -p "Continue from the previous result" -session "your-session-id"
```

`pk run` defaults to `gpt-6-astra`, reasoning effort `xhigh`, and the current directory as its
workspace. Change the workspace with `-workspace DIR`; add `-jsonl` for assistant and tool-call status
events as JSON lines. The convenience form `pk -p "Summarize this project"` runs one prompt and exits
with the same flags as `pk run`.

## Tools and workspace instructions

The built-in tool set is:

- **Bash** runs commands on the host with the pk process's operating-system permissions. It is not
  confined to the selected workspace and pk does not provide an OS sandbox.
- **ViewImage** lets the model inspect images through the runtime's image operation.
- **SkillUse** loads a registered skill. pk discovers skills in `~/.codex/skills` and
  `~/.agents/skills` by default. Supply one or more `-skills-dir DIR` flags to use custom skill
  directories in place of those defaults.

pk adds the workspace root's `AGENTS.md` to its instructions when the file is at most 64 KiB. For
nested paths, pk instructs the agent to inspect and follow the nearest nested `AGENTS.md`; nested
files are not loaded automatically. Tool calls can read or change files and start processes with the
permissions of the user running pk.

## Local data

pk's own auth file lives at `~/.pk/auth.json` (or `$PK_HOME/auth.json`). Session history is stored at
`~/.pk/sessions` (or `$PK_HOME/sessions`), separate from the Codex CLI auth file. Prompt and response
content, tool calls, and their results are part of that durable history. `pk` currently has no
session-list command.
