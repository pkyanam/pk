---
name: pk
description: Guidance for pk workflows and source development. Check the active release and session snapshot before describing capabilities.
---

# pk

Use this skill for pk-specific questions and changes. Ground operational advice in the installed `pk --help`, the active TUI `/help`, and current source/docs; an installed release may lag the checkout.

## Product and architecture

- pk is the local coding-agent harness; the selected model/provider powers it. Identify the app as pk and the model separately.
- The runtime is Go, with a Bun/TypeScript OpenTUI frontend. Check `go.mod`, `cmd/pk/`, `internal/runner/`, and `ui/` for current boundaries.
- Bash runs with pk's operating-system permissions; the workspace is a working location, not a sandbox. AskUser is a question/choice flow, not a permission gate. New detached tasks persist questions for attach-time answers; Escape dismisses their UI without stopping the worker. One-shot headless runs have no question host. See `docs/task-questions.md`.
- Skills load on demand. The bundled `pk` skill is source-controlled at `skills/pk/SKILL.md`; `/skills` lists available instructions. Session snapshots retain their original prompt and tool/skill context. Use `/new` to adopt changed instructions; attaching an older session does not rewrite its snapshot.
- `pk config show` reports `context_policy` (`full` by default). `pk config set context-policy compact` or `pk run --context-policy compact` opts into compacting large completed Bash results in model-visible context while preserving local captures. Use `full` for complete results. A session snapshot binds its policy; changing it requires a new session. This is a behavior option, not a guaranteed token-saving mode.
- Manage installed skills with `pk skills search QUERY`, `pk skills list`, `pk skills add SOURCE [SKILL]`, and `pk skills remove NAME`. New sessions load managed skills under `$PK_HOME/skills`; consult `docs/skills-management.md` for source and safety limits.
- Keep model tools distinct from TUI slash commands, CLI commands, and tool names mentioned in workspace files. When asked what tools are available, report only the functions in the current session's tool schema; mention an extension only if it was successfully registered for that session.
- `/plugins` manages installed extensions. `/plugins discover OWNER/REPO` previews repository candidates; review a candidate before installing it. CLI source management is available through `pk plugin`; inspect its help for exact arguments. Start `/new` to activate changes. Extensions are trusted local programs, not sandboxed plugins; see `docs/extensions-design.md`.
- Extension lifecycle hooks are metadata-only observers (`run_start`, `response_complete`, `run_end`). They cannot modify prompts, tool calls, or transcript content; do not describe them as general-purpose hooks.
- Extension workers may send negotiated `tool_progress` updates. The TUI shows these transiently on the active tool row; they are best-effort, can be dropped, and are not model context or saved history. Do not treat them as completed output.
- Use `/usage` for saved session token totals and the latest request composition. Provider input/output/cached-input counters are tokens; the category grid measures JSON-value bytes, not token attribution or context-window occupancy. Unknown counters/capacity stay unavailable. This local view makes no provider request; see `docs/usage.md`.
- v0.1.5 adds a context manager: `/usage` shows model-limit sources separately from its operational input budget, and `C` or `/compact` writes a reversible checkpoint while idle. Estimates are heuristic; unknown models use a configurable operational fallback. The full transcript remains saved. Manual compaction does not support forked sessions and refuses an over-budget history when no safe completed-turn cut is available. See `docs/context-management.md` and `docs/research/context-compaction.md`.
- Attach only files the user explicitly selects. PDF inputs use bounded local text extraction; there is no native PDF upload or OCR. The optional `pdftoppm` scanned-page preview depends on a release that includes the fallback and on Poppler being installed. See `docs/attachments-design.md` before describing supported PDF behavior.
- `WebSearch` and `WebFetch` are available in newly started sessions when `pk web status` reports a configured TinyFish route. pk prefers `TINYFISH_API_KEY` or a pk-stored direct key; otherwise it can use an installed Monid CLI with an active key. Monid credentials stay in Monid's store. The Monid bridge calls only TinyFish Search/Fetch, checks explicit zero-price metadata and zero billed usage, and has no paid-provider fallback. Configure a direct key with `pk web configure`, configure Monid in its own CLI, and use `/new` after changing configuration. See `docs/web-search.md` for limits, privacy details, and observed live validation.
- Where `/image` is available, it configures the optional ImageGen tool for new TUI sessions. It uses a separate Codex image worker and existing ChatGPT authentication; the chat model remains unchanged. Use `/image enable`, then `/new`, and confirm ImageGen in `/tools` before promising generation. `/image disable` changes future sessions; saved sessions retain their original driver.
- Do not infer capabilities from source alone. Confirm the command is present in the installed `pk --help` and active TUI `/help`, exercise the protocol, and check its documentation before describing support. MCP configuration or a saved server catalog does not by itself mean a server is connected or its tools are available.

In a pk checkout, consult `docs/architecture.md`, `docs/prompt-reference.md`, and `docs/extensions-design.md` for detailed boundaries.

## Develop and update pk

Locate the intended checkout instead of assuming a machine-specific path:

```sh
git rev-parse --show-toplevel
command -v pk
```

Make source changes in that checkout. `cmd/pk/` composes CLI/RPC, `internal/runner/` owns model/session behavior, other runtime packages live in `internal/`, and `ui/src/` contains the TUI. Read applicable `AGENTS.md` files and nearby tests before editing. Build outputs such as `bin/pk` and `ui/dist/` are generated.

Normal updates use checksum-verified GitHub Release binaries. For source development, select the checkout explicitly:

```sh
pk update --source /path/to/pk
pk version
pk rollback
```

Source updates validate and build in a staged copy, then activate a paired immutable Go/UI release. Bare `pk update` and `/update` prefer the latest compatible published release from [`pkyanam/pk`](https://github.com/pkyanam/pk); source fallback occurs only when no compatible asset exists, not after download/checksum errors. Bun is required for the TUI. See `docs/updates.md`. The TUI also supports `/update --source PATH`, `/rollback`, and `/reload`. Update and rollback require an idle foreground session. After an update, `/reload` requests a session-preserving restart on the new release. If the terminal does not restart cleanly, exit and launch `pk` again. Use `/attach SESSION_ID` to continue an existing saved session or `/new` for current prompt and skill context.

`scripts/install` remains useful for building/installing directly from a checkout. Do not install, publish, push, or modify another checkout unless the user requested it. Validate the installed binary separately from source-level changes before claiming an in-app capability shipped.
