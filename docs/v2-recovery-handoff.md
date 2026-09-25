# v0.2.0 M1 recovery handoff

## Current state (2026-09-24)

The old handoff below is superseded. M1 was implemented and committed as `90cac3f` (`v2 (M1): daemon skeleton, stdio JSON-RPC, provider-catalog State, pk daemon start|stop|status`) in `/Users/preetham/Code/pk-v2-worktree`, then `origin/main` was merged cleanly in `33c49c2`. The branch is `feat/v2-daemon`, tracking `origin/feat/v2-daemon`, five commits ahead at that merge commit. The worktree was clean at inspection. Preserve this state; do not recreate M1 or reset/clean/merge it again.

The latest pk task/session is `3f4adaf36075973f4c2927f46e208238`. It stopped after saying it would rerun verification and investigate the build/release path; no verification commands followed that statement. Its read-only child investigation (`a87468efc953a4921c1fbb1243c717aa`) was launched but returned no findings. Its log ends with `when_idle` / `input stream closed`, followed 2 ms later by `hard` / `interrupted`; the logs do not identify who initiated that lifecycle shutdown. Resume the parent session with `pk --session 3f4adaf36075973f4c2927f46e208238` from the v2 worktree. The current CLI supports `--session ID` to resume a saved session; `-r`, `-resume`, and `--resume` open the picker instead.

Copy and paste this into the resumed session:

```text
Continue the existing v0.2.0 task in `/Users/preetham/Code/pk-v2-worktree` on `feat/v2-daemon`, starting from its current clean merged HEAD (`33c49c2`). M1 is already complete in `90cac3f` and `origin/main` is already merged; do not recreate M1, amend it, reset/clean the worktree, or redo the merge. First inspect current status and the merge diff. Then finish the verification you already announced: verify the daemon and resume dispatch coexist, run the relevant Go tests and `go build ./...`, and inspect any failures before making a narrowly scoped fix. The prior read-only child investigating build/install/release for a possible `pk2` binary was interrupted without returning findings. If that investigation remains in scope, restart only that read-only investigation and wait for its result; do not assume its findings, and do not let parent idleness silently discard pending child work. Preserve all existing work. Do not commit, push, tag, release, or publish without an explicit review of the resulting diff. Report exact checks and outcomes.
```

The parent session ended at an assistant turn boundary (`when_idle`) rather than a recorded user cancellation. It had already consumed the runtime's one continuation for the long interactive run, so a later future-tense-only response after the user's “Please don't stop” could not trigger another recovery. Main now scopes the bounded continuation allowance to each external user turn; the internal recovery input does not reset its own allowance. The child lifecycle shutdown coincided with parent idleness, but the logs do not prove the parent directly canceled it. Ensure future parent work waits for started children before returning idle.

## Superseded original M1 handoff

The separate checkout `/Users/preetham/Code/pk-v2-worktree` is on `feat/v2-daemon`, tracking `origin/feat/v2-daemon` at `b204a8a`. Its only local change is the untracked `docs/v2-architecture.md`. There is no `internal/v2/` directory or daemon scaffold yet. The earlier M1 attempt stopped before editing: the child reported its file-writing tool was confined to `/private/tmp`, then its session was interrupted. The architecture decisions and M1 request were saved in `~/.pk/sessions/917747e6cbc9205d1fa244bc0c5cb907.session.jsonl` and `~/.pk/sessions/3a5d726e4ecb88efeff45423d6f9501d.session.jsonl`.

Copy and paste this into pk when resuming the v0.2.0 work:

```text
Resume the original v0.2.0 first milestone (M1) in the existing isolated checkout `/Users/preetham/Code/pk-v2-worktree`, branch `feat/v2-daemon`. First inspect `git status`, `git diff`, and `docs/v2-architecture.md`; preserve every existing change, including that untracked architecture document. Do not reset, clean, overwrite, or merge this worktree, and do not edit `/Users/preetham/Code/pk` main. The prior attempt did not create implementation files: its file tool was confined to `/private/tmp` and it was interrupted. Use tools that can write to the actual worktree, verify every file path before editing, and keep all M1 implementation work on this branch. Do not commit, push, tag, release, or publish; report a reviewable compiling result for the owner.

Agreed architecture from `docs/v2-architecture.md`: one auto-starting daemon owns state; CLI/TUI/agents are stateless clients; JSON-RPC 2.0 over stdio first (Unix socket/local IPC later); deterministic State values rebuild from ordered transformations; Bun/JS/TS extensions may reload live, while Go core changes use honest daemon restart-with-replay. The first milestone is only the daemon skeleton, stdio JSON-RPC, provider-catalog State with transformations, and one Bun plugin hot-reload demonstration.

Implement the original M1 scope surgically:
1. `internal/v2/state/state.go`: generic `Transform[T]`, `State[T]` with base, ordered transforms and current value; `New`, `Add`, deterministic `Rebuild`, `Value`, and update subscribers.
2. `internal/v2/state/catalog.go`: `Model`, `Catalog`, empty constructor, loader transform, provider-disable transform, halve-limits transform (must not compound between rebuilds), and cleanup-missing transform. Use a small deterministic local fixture; no network fetch.
3. `internal/v2/jsonrpc/jsonrpc.go`: minimal newline-delimited JSON-RPC 2.0 stdio server, including parse/request/method/params/internal errors and notification handling.
4. `cmd/pk/daemon.go` plus only the minimal existing `cmd/pk/main.go` dispatch/help wiring: `pk daemon start` serves `ping`, `catalog/get`, and `catalog/reload`; `stop` and `status` use a private pidfile under PK_HOME. Keep v2 independent from v0.1 internals other than the command entry point.
5. Add focused tests beside State/JSON-RPC and for daemon request flow. Run the scoped Go tests and `go build ./...` from the v2 worktree; report commands and outcomes. If any detail in the current architecture doc or worktree conflicts with this handoff, inspect and reconcile it rather than replacing existing work.

The full original M1 specification is in the saved session log above; use it if a detail is needed. The essential acceptance check is a clean, compiling isolated branch with implementation and tests present, ready for review. No release is part of M1.
```

## Separate follow-up: approval mode

Do not add this to M1 without a separate reviewed design. For pk, use a `Shift+Tab` cycle across **Manual** (ask before side effects), **Auto Accept Edits** (allow file edits and a narrow set of safe filesystem operations while continuing to ask before shell/network side effects), **Plan** (inspect and plan without mutations), and opt-in **Full Access** (bypass approval prompts). Full Access must require an explicit enabling setting or launch flag; never make it the default. Do not copy Claude Code's Auto mode, which depends on a server-side classifier, as if pk had equivalent enforcement. Claude documents the corresponding Manual/default, `acceptEdits`, `plan`, and `bypassPermissions` tradeoffs in its [permission modes guide](https://code.claude.com/docs/en/permission-modes); its [CLI reference](https://code.claude.com/docs/en/cli-usage) confirms `Shift+Tab` mode cycling and explicit opt-in for the bypass mode.

The approval runtime must gate each side effect immediately before execution, create durable approval IDs, and support allow-once or deny-with-reason. A timeout must never approve an action. Define cancellation, reconnect/recovery, subagent permission propagation, and unattended-task behavior before implementation. Approval mode is a user-consent workflow, not an OS sandbox; tool processes still run with the host process's filesystem and network permissions. Keybinding audit: no Shift+Tab permission handler exists today. The composer currently handles Tab for completion; before adding the mode cycle, ensure Shift+Tab is intercepted first and does not also trigger completion. `dontAsk` (which denies actions that would prompt) is a separate unattended policy, not a substitute for an interactive approval mode.
