---
name: pk
description: Help use, configure, and develop pk from its current commands and documentation.
---

# pk

pk is a local coding-agent harness; the selected model/provider powers it. Identify pk separately from the model. For capability questions, use this skill and the active tool registry; do not run help or inspect files if that is enough. CLI/slash commands and workspace files are not model tools.

## Configure

Configure through these CLI families: `pk config`, `pk provider`, `pk mcp`, `pk skills`, and `pk plugin`. Consult command help and the guides in the [pk documentation](https://github.com/pkyanam/pk/tree/main/docs); in a checkout, start at `docs/README.md`.

Never print, log, or pass keys as command arguments. Provider keys saved by pk are local and not encrypted. Configuration, skills, and extensions apply to new sessions where noted; existing sessions retain snapshots. Use `/new` when needed.

## Workspace journal

WriteFile/EditFile changes are journaled. After a batch of those edits, call `WorkspaceDelta` before reporting completion to audit recorded paths and outcomes; omit `cursor` for the latest summary, or use one as an inclusive as-of checkpoint (not a changes-since cursor). `diff=true` with `path` or `op_id` returns one bounded diff. Bash and external edits are unobserved, so an empty result never means the workspace is clean. `pk journal list|diff|plan|restore SESSION` and the `/journal` panel are user-initiated recovery: restore refuses files that changed since, snapshots current content first, and never overwrites user edits. It is not a sandbox or a backup. See docs/workspace-journal.md.

## Develop and update

The source is [`pkyanam/pk`](https://github.com/pkyanam/pk). Locate the intended checkout with `git rev-parse --show-toplevel`; read `AGENTS.md`, `docs/README.md`, and `git status --short` before editing. Preserve user changes and keep a recoverable checkpoint. Prefer Read for bounded file contents, WriteFile for new files and EditFile for exact changes when available; inspect existing content first. Go code is in `cmd/pk/` and `internal/`; OpenTUI is in `ui/src/`.

Run focused tests, then relevant checks: `go test ./...`, `go vet ./...`, and `cd ui && bun run check && bun test`. Report only checks actually run. To stage a local checkout, use `pk update --source /absolute/path/to/pk`; the updater builds and validates a paired release before activation. Update only while the foreground session is idle. Verify with `pk version`, then `/reload` (or restart pk) to continue on the new release. If it regresses, run `pk rollback` and reload. Keep a concise handoff of changes, tests, release state, and remaining limits; do not claim pk is bug-free or fully autonomous.
