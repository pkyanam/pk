# Community plugin compatibility

Reviewed 2026-09-23. “Plugin” is not one portable executable format. Codex,
Claude Code, OpenCode, Pi, and pk package different mixtures of instruction
files, MCP configuration, prompt commands, and host-specific programs. This
page separates what pk can load today from a safe adapter plan.

## What pk supports today

| Component | Current pk path | Boundary |
|---|---|---|
| pk extension | `pk plugin discover` previews and installs an explicitly selected `manifest.json` using `pk.extensions/v1`. Workers can expose model tools and namespaced slash commands; a missing declared Go worker can be built from a repository Go module. | Discovery reads metadata and does not start workers. An enabled worker is trusted executable code running with pk's OS permissions. |
| Agent Skill | `pk skills search`, `pk skills add SOURCE [SKILL]`, and `/skills` import a selected GitHub/skills.sh skill folder containing `SKILL.md` and bounded supporting files. | This is a separate skill import path, not a general plugin-package importer. It copies files and does not execute bundled scripts. New sessions load installed skills; saved snapshots are unchanged. |
| MCP server | `pk mcp` configures a user-selected stdio or remote Streamable HTTP server. | Plugin MCP declarations are not imported automatically. Configuration does not connect until a new run loads it; authentication and connection remain explicit. |
| Lifecycle observer | A pk extension may declare `run_start`, `response_complete`, or `run_end` with `mode: "observe"` and negotiate `lifecycle_notifications`. | Notifications contain bounded run metadata and are best-effort. Transforming prompts, intercepting tools, mutating events, or adding transcript content is unsupported. |

The plugin source catalog currently installs valid pk extension manifests. It
reports some non-pk markers, such as Claude Code plugin manifests and Pi
extension/package layouts, as unsupported; that is detection, not compatibility.
Codex plugin manifests and OpenCode configurations are not translated by the
current importer. A repository may expose several component types, but each
must be imported through its supported path or recreated explicitly.

## Ecosystem formats and the current gap

| Ecosystem | Primary-source package shape | What pk does with it today |
|---|---|---|
| Codex plugins | The portable package uses root `plugin.json`, optional `skills/`, and `mcp.json`; OpenAI-specific metadata can map registered MCP apps and lifecycle hooks. `.codex-plugin/plugin.json` is a compatibility overlay/fallback. | Not a plugin-package import format. A `SKILL.md` skill can be imported separately, and an MCP server can be recreated with `pk mcp`; Codex hook commands and app mappings are not translated. |
| Claude Code plugins | A plugin may contain `.claude-plugin/plugin.json` and root-level `skills/`, legacy `commands/`, `agents/`, `hooks/`, `.mcp.json`, `.lsp.json`, monitors, and settings. A separate marketplace catalog points to plugins. | The importer recognizes Claude manifest/marketplace markers as unsupported. Individual skills may use pk's separate skill importer. Commands, agents, hooks, LSP and monitor declarations are not installed by the plugin importer. |
| OpenCode | Plugins are JavaScript/TypeScript modules loaded from plugin directories or npm configuration and can register event hooks and custom tools. Commands are Markdown templates; skills are `SKILL.md` folders; MCP is configured separately. | No OpenCode plugin runtime or npm package loader. Static skills and simple command text can be adapted, but plugin code, hooks, shell substitutions, and OpenCode-specific tools are not portable. |
| Pi | A package may use `package.json` with a `pi` resource manifest for extensions, skills, prompts, and themes, or conventional resource directories. Extensions are TypeScript modules with tools, commands, event handlers, providers, state, and TUI APIs. Packages install from npm, Git, or local paths. | The importer recognizes Pi extension/package markers as unsupported; it does not install npm packages or execute Pi modules. A selected standard skill can be imported through pk's separate skill path. Pi hooks can change behavior in ways pk's observer hooks cannot. |

These ecosystems have overlapping vocabulary, not interchangeable behavior.
For example, a Claude/OpenCode Markdown command is a prompt template, while a
pk extension slash command calls a worker. A Pi or OpenCode hook can execute
code at host lifecycle points, while pk lifecycle hooks are observation-only.

## Adapter design

Build adapters in small, explicit stages. Every stage should preview a pinned
source revision and selected components before writing managed state. Catalog
browsing must remain metadata-only; it must never import modules, run hooks,
install package dependencies, or start an MCP server.

1. **Classify and review.** Detect supported formats from exact files and
   schemas: Codex root/compatibility manifests, Claude plugin and marketplace
   manifests, Pi's `pi` package declaration, and known OpenCode resource paths.
   Show the source revision, selected component names, files to copy, required
   runtimes, and unsupported features. Unknown JSON or directory names remain
   unknown rather than being guessed into a supported format.
2. **Import static skills.** Reuse the existing `SKILL.md` parser and bounded
   data copier. Preserve the skill body and relative references; validate
   frontmatter and safe paths; reject symlinks, traversal, oversized trees, and
   collisions. Supporting files stay beside the imported skill. Do not execute
   scripts bundled with a skill. Start a new session to load it.
3. **Adapt prompt commands as prompt commands.** Add a distinct pk prompt
   template type rather than disguising a Markdown command as an executable
   extension. Initially support plain Markdown bodies, descriptions, and
   bounded argument placeholders. Treat shell interpolation (for example
   Claude's `!` command expansion or OpenCode's `!` output injection), file
   expansion, model overrides, agent routing, and approval metadata as
   unsupported until each has explicit pk semantics. Show those fields in
   preview; never run a command while importing or listing it.
4. **Create MCP configuration drafts.** Translate only known configuration
   shapes into pk's existing stdio or Streamable HTTP model. Display executable,
   arguments, URL, environment-variable names, and requested transport. Do not
   copy secret values from plugin files or interpolate them. Require the user
   to review the server and enter/select credentials with pk's MCP setup. Do
   not connect until the user starts a new session. SSE, custom auth providers,
   UI resources, and unknown transports should be reported as unsupported.
5. **Keep executable extensions native.** Do not execute or transpile arbitrary
   Pi/Claude/OpenCode/Codex plugin code as a compatibility shortcut. A source
   that exposes an MCP server can be configured through the explicit MCP flow;
   otherwise it needs a separately authored `pk.extensions/v1` worker. That
   protocol can add declared tools/commands and observe-only lifecycle events,
   but it is not a general hook, provider, session-state, or TUI component API.

For each install, retain the original source/revision and selected paths in
provenance. Make removal scoped to the imported components. Never overwrite a
managed component silently; let the user compare revisions and explicitly
replace or keep the installed copy. Plugin executables remain trusted local
programs rather than sandboxed packages.

## Primary sources

- [Codex plugin architecture](https://developers.openai.com/plugins/concepts/plugins), [package format](https://developers.openai.com/plugins/build/plugins), and [skill format](https://developers.openai.com/plugins/concepts/skills).
- [Claude Code plugins](https://code.claude.com/docs/en/plugins), [marketplaces](https://code.claude.com/docs/en/plugin-marketplaces), and [skills](https://code.claude.com/docs/en/skills).
- [OpenCode plugins](https://opencode.ai/docs/plugins/), [commands](https://opencode.ai/docs/commands/), and [Agent Skills](https://opencode.ai/docs/skills/).
- [Pi extensions](https://pi.dev/docs/latest/extensions) and [Pi packages](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/packages.md).
- pk's current contracts: [`docs/extensions-design.md`](extensions-design.md), [`docs/skills-management.md`](skills-management.md), and [`docs/mcp.md`](mcp.md).
