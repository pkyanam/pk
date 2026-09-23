---
name: pk
description: Guidance for pk workflows and source development. Check the active release and session snapshot before describing capabilities.
---

# pk

Use this skill for pk-specific questions and changes. Ground operational advice in the installed `pk --help`, the active TUI `/help`, and current source/docs; an installed release may lag the checkout.

## Product and architecture

- pk is the local coding-agent harness; the selected model/provider powers it. Identify the app as pk and the model separately.
- The runtime is Go on the pinned Unreal Agent foundation, with a Bun/TypeScript OpenTUI frontend. Check `go.mod`, `cmd/pk/`, `internal/runner/`, and `ui/` for current boundaries.
- Bash runs with pk's operating-system permissions; the workspace is a working location, not a sandbox. AskUser is a foreground question/choice flow, not a permission gate; detached/headless runs must not depend on blocking questions.
- Skills load on demand. The bundled `pk` skill is source-controlled at `skills/pk/SKILL.md`; `/skills` lists available instructions. Session snapshots retain their original prompt and tool/skill context. Use `/new` to adopt changed instructions; attaching an older session does not rewrite its snapshot.
- Keep model tools distinct from TUI slash commands, CLI commands, and tool names mentioned in workspace files. When asked what tools are available, report only the functions in the current session's tool schema; mention an extension only if it was successfully registered for that session.
- `/plugins` lists and toggles known explicit extension manifests. To register a manifest, use `/plugin enable "/absolute/path/to/manifest.json"`, then `/new`. Extensions are trusted local programs, not sandboxed plugins. Check `docs/extensions-design.md` for protocol limits.
- Do not infer capabilities from source alone. Confirm the command is present in the installed `pk --help` and active TUI `/help`, exercise the protocol, and check its documentation before describing support. MCP configuration or a saved server catalog does not by itself mean a server is connected or its tools are available.

In a pk checkout, consult `docs/architecture.md`, `docs/prompt-reference.md`, and `docs/extensions-design.md` for detailed boundaries.

## Develop and update pk

Locate the intended checkout instead of assuming a machine-specific path:

```sh
git rev-parse --show-toplevel
command -v pk
```

Make source changes in that checkout. `cmd/pk/` composes CLI/RPC, `internal/runner/` owns model/session behavior, other runtime packages live in `internal/`, and `ui/src/` contains the TUI. Read applicable `AGENTS.md` files and nearby tests before editing. Build outputs such as `bin/pk` and `ui/dist/` are generated.

The installed CLI supports managed updates from a selected pk checkout:

```sh
pk update --source /path/to/pk
pk version
pk rollback
```

The updater validates the source, runs Go and UI checks/builds in a staged copy, then activates a paired immutable Go/UI release. The TUI also supports `/update [--source PATH]`, `/rollback`, and `/reload`; bare `/update` and `pk update` without `--source` fetch the official [`pkyanam/pk` GitHub repository](https://github.com/pkyanam/pk) on `main`. Update and rollback require an idle foreground session. After an update, `/reload` requests a session-preserving restart on the new release. If the terminal does not restart cleanly, exit and launch `pk` again. Use `/attach SESSION_ID` to continue an existing saved session or `/new` for current prompt and skill context.

`scripts/install` remains useful for building/installing directly from a checkout. Do not install, publish, push, or modify another checkout unless the user requested it. Validate the installed binary separately from source-level changes before claiming an in-app capability shipped.
