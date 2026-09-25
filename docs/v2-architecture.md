# pk v0.2.0 Architecture (feat/v2-daemon)

Working spec for the v0.2.0 redesign. **Not released** — developed on the
isolated worktree/branch `feat/v2-daemon` (maintained on GitHub at
`origin/feat/v2-daemon`), never merged to `main` until the owner decides.

Inspired by the OpenCode 2 reload design (https://anoma.ly/notes/opencode-reloaded/)
and the analysis of what that means for a Go core.

## Goal

Streamline and optimize the entire harness around a **single long-running
daemon process** that owns all state. Clients (CLI flagship, TUI, subagents,
external processes) are **stateless protocol clients** that can connect from
anywhere. The system hot-reloads itself: configuration, plugins, skills,
MCP servers, and tools take effect live across every session — and harness
code changes are applied with minimal disruption.

## Agreed decisions (2026-09-25)

| Concern | Decision |
|---|---|
| Process model | One daemon; auto-spawn on first `pk` invocation; idle auto-shutdown; `pk daemon status|stop|logs` |
| Client model | Stateless clients; CLI is flagship; TUI and agents are clients |
| Transport | JSON-RPC 2.0 over stdio (CLI/TUI) + Unix domain socket for local IPC; HTTP/SSE later for remote/web |
| State model | OpenCode-style `State` + registered transformation functions; States rebuilt deterministically (empty base → run each transformation once, in order) |
| Core (Go) code changes | Daemon **restart-with-replay**: persist state, restart fast, reconstruct States from transformations, clients auto-reconnect. Go cannot hot-swap compiled code; this is the honest equivalent |
| Extension layer | JS/TS plugins under **Bun** (opencode-compatible). Tools/plugins/skills/config hot-reload live, same-turn invocation, no model-cache bust |
| Bun dependency | Required for extensions; graceful degradation to core tools when Bun is absent |
| First milestone | Daemon skeleton + JSON-RPC over stdio + auto-spawn + provider-catalog State with transformations + one Bun plugin hot-reload demo (write a tool → invoke it the same turn) |

## Architecture

```
+-------------+   JSON-RPC 2.0   +------------------------------+
|   CLI (pk)  |<--- stdio ------>|                              |
+-------------+                   |      pk DAEMON (Go)         |
|   TUI (ui/) |<-- stdio/UDS ---->|  owns ALL state:            |
+-------------+                   |   - sessions                |
| subagents / |<--- UDS -------->|   - provider catalog        |
| external    |                   |   - tool registry           |
+-------------+                   |   - plugins/skills/MCP      |
                                  |   - context/usage           |
                                  +-----------+- --------------+
                                              |
                              +---------------+----------------+
                              |   Extension layer (Bun/JS-TS)  |
                              |   States + transformations     |
                              |   (opencode-compatible)        |
                              +--------------------------------+
```

### Components

1. **Daemon (Go)** — `internal/v2/daemon/`
   - Owns all mutable state; single writer, no cross-process races.
   - JSON-RPC 2.0 server over stdio + Unix domain socket.
   - Auto-spawn via PID/lockfile guard (`internal/v2/daemon/lifecycle.go`).
   - Idle auto-shutdown; explicit `stop`/`logs` commands.
   - Restart-with-replay: on start, load persisted state, rebuild all States
     from registered transformations, resume sessions, serve clients.

2. **State + transformations (Go)** — `internal/v2/state/`
   - Generic `State[T]` holding a value and an ordered list of transformations.
   - `Rebuild()` = empty base → apply each transformation once, in order →
     publish result. Deterministic; re-running never compounds.
   - Sources (config, plugins, MCP, catalog) all become States.
   - Reload = drop registrations of removed source → re-run changed source →
     rebuild affected States → publish across all sessions.

3. **Extension layer (Bun/JS-TS)** — `internal/v2/ext/`
   - JS/TS plugin files watched for create/edit/delete; live reload.
   - Plugins register transformations + hooks (opencode-compatible shape:
     `tool`, `event`, `tool.execute.before/after`, etc.).
   - Graceful degradation: no Bun → core tools only.

4. **Provider catalog State** — first real State.
   - Sources: models.dev refresh, local models, provider policy, limits.
   - Each registers a transformation; reload rebuilds deterministically.
   - Exactly the OpenCode post's example.

5. **Clients (stateless)**
   - `cmd/pk` becomes thin: resolve/spawn daemon, JSON-RPC over stdio.
   - TUI (`ui/`) becomes a client of the same protocol.
   - Subagents/external processes talk JSON-RPC over the socket.

## Key boundaries / constraints

- **Go core cannot hot-swap its own code.** Harness code changes apply via
  daemon restart-with-replay. True live reload is reserved for the
  extension layer (Bun), where the code is interpreted.
- **State determinism is the safety invariant.** Same base + same data +
  same transformation sequence ⇒ same result. Never mutate a State in place.
- **No model-cache bust on reload.** Extension/config reloads don't touch
  live sessions' context; daemon restart replays persisted session/context
  state (context is rebuilt from disk, not carried in-process).
- **Single-writer daemon.** No other process may mutate shared state.
- v0.1.x machinery (immutable releases, frozen per-session plugin manifests,
  one-use reload handoff) is the baseline we are redesigning; v2 replaces
  the per-invocation model, not the user data (PK_HOME stays).

## Open questions (for future milestones)

- Protocol schema: method names, framing (newline-delimited JSON?), auth on
  the Unix socket, HTTP/SSE for remote clients.
- Session State design: how sessions/context become a State.
- Migration path from v0.1.x releases; coexistence or replacement.
- Bun packaging: bundled runtime vs. system Bun; version pinning.
- Multi-user / multi-machine (remote daemon) story.

## Milestones

- **M1 (this branch, current):** daemon skeleton + JSON-RPC over stdio +
  auto-spawn + provider-catalog State with transformations + Bun plugin
  hot-reload demo (write a tool → invoke it the same turn).
- M2: sessions/context as States; TUI as a client.
- M3: full extension layer (skills, MCP, config as States).
- M4: daemon restart-with-replay + upgrade path; `pk daemon` full surface.
