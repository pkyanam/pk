# pk documentation

Start with [getting started](getting-started.md) for installation and the TUI. Use the guides below for setup, operation, and source development.

## Configure and operate

- [Providers](providers.md) — provider presets, credentials, model discovery, and protocol limits.
- [MCP](mcp.md) — configure stdio or remote servers, authentication, and connection boundaries.
- [Managed skills](skills-management.md) — search, review, install, and remove skills.
- [Extensions](extensions-design.md) — plugin sources, tools, commands, progress, lifecycle observers, and host limits.
- [Available tools](toolbelt.md) — model-visible tools and their behavior.
- [Tasks and questions](task-questions.md) — durable tasks and foreground or detached questions.
- [Attachments](attachments-design.md) — explicit file inputs, supported formats, and bounds.
- [Context management](context-management.md) — context budgets, `/usage`, automatic compaction, and `/compact`.
- [Workspace journal](workspace-journal.md) — journaled file-tool changes, `WorkspaceDelta`, and user-initiated restore.
- [Token usage](usage.md) — provider counters and request composition.
- [Web search](web-search.md) — TinyFish configuration, data flow, and limits.
- [Privacy](privacy.md) — local storage and network boundaries.

## Develop and verify

- [Development handoff](HANDOFF.md) — recovery checkpoint, self-update workflow, and remaining priorities.

- [Architecture](architecture.md) and [prompt reference](prompt-reference.md) — runtime boundaries and the saved prompt/tool schema.
- [Self-development audit](self-development-handoff-audit.md) — tested source-update boundaries and remaining validation limits.
- [Updates](updates.md) — source builds, paired releases, reload, and rollback.
- [Validation](validation.md) — test and release checks.
- [ACP](acp.md) — supported Agent Client Protocol subset.
- [Subagents](subagents.md) — bounded child-agent behavior and limitations.
- [Extension compatibility](plugin-compatibility.md) — supported imports and unsupported plugin components.
- [Unreal foundation audit](unreal-foundation-audit.md) — inherited runtime behavior and local integration evidence.

Performance and benchmark material is exploratory: [findings](benchmark-findings.md), [measurement notes](performance.md), and [context-compaction research](research/context-compaction.md). It does not establish universal speed, cost, or token savings.
