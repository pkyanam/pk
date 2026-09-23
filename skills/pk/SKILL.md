---
name: pk
description: Guidance for pk architecture, identity, skills, extensions, and source development. Use when explaining or changing pk; do not imply an in-app updater exists.
---

# pk

Use this skill when the user asks how pk works, wants help with a pk-specific workflow, or asks to
change pk itself. Ground answers and edits in the active checkout and its current documentation;
installed builds and docs may lag the source.

## Identity and architecture

- pk is the application and coding-agent harness. The selected model and provider power it; do not
  present the model or provider as pk's product identity. The model ID can change with `/model`.
- The runtime is Go and is based on the pinned Unreal Agent harness. The OpenTUI client is Bun and
  TypeScript. The Go CLI composes authentication/configuration, the local stdio RPC host, runner,
  tasks, and tool integrations. Check the current `go.mod`, `cmd/pk`, `internal/runner`, and `ui/`
  before describing implementation details.
- Current built-in tools are Bash, ViewImage, and SkillUse; the interactive foreground TUI also
  registers AskUser. Bash runs with the pk process's OS permissions; choosing a workspace does not
  sandbox it. AskUser collects a foreground choice and is not permission approval. Do not imply
  headless or detached workers can block on AskUser.
- Skills are instruction files discovered from configured roots and loaded on demand with SkillUse.
  Session snapshots preserve the instructions and tool schemas from session creation. `/new` starts
  a session with the current instructions; attaching a legacy session does not rewrite its context.
- Optional CLI extension manifests are explicit and trusted code, not a sandbox or general plugin
  marketplace. Check `docs/extensions-design.md` and live `pk --help`/TUI `/help` for the exact
  current interface. Do not claim hooks, extension commands, or `/plugins` support unless current
  source and help show they have shipped.

For a prompt-level question, see `docs/prompt-reference.md`. For component boundaries, see
`docs/architecture.md`; for the extension protocol and its limits, see `docs/extensions-design.md`.

## Work on pk itself

Locate the intended source checkout rather than assuming a machine-specific path:

```sh
git rev-parse --show-toplevel
command -v pk
```

This skill's source is `skills/pk/SKILL.md` in the pk checkout. The bundled loader is being added;
until that implementation is included in an installed build, this repository file alone does not
make the skill appear in pk's live `/skills` picker.

Make requested product changes in that checkout's source. Relevant areas include `cmd/pk/` for CLI
and RPC composition, `internal/runner/` for the model/session loop, `internal/` for runtime
components, and `ui/src/` for OpenTUI behavior. Read the closest `AGENTS.md` and existing tests
before editing. Run only the checks appropriate to the change; generated `bin/pk` and `ui/dist/`
are build outputs, not source-of-truth edit targets.

The repository provides `scripts/build` and `scripts/install`. The installer builds the Go binary
and OpenTUI assets, then stages and replaces the user's installed binary and UI directory. Use it
only when the user asks to install the checkout or another instruction clearly includes installation.
It does not hot-reload an already running TUI: exit and launch `pk` again to run the newly installed
binary and frontend.

There is currently no supported in-app `pk self-update` command. Treat automated self-update as a
pending product feature, not an existing workflow. The desired flow must build and install both
binary and UI assets, report failures without leaving a half-updated install, then ask the user to
relaunch the TUI; use `/new` when changed prompt or skill context must take effect. Do not invent a
`/plugins` or `/skills` command if it is absent from the active TUI's `/help` and source. Do not push,
publish, or alter another pk checkout unless the user explicitly asks.
