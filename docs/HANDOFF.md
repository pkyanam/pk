# Continue pk development with pk

This is the starting point for handing development back to pk. Keep this guide
and the project checklist available when continuing with another provider.

The latest [journal audit and performance results](../benchmarks/results/2026-09-23-journal-audit/README.md)
record recovery fixes and measured local improvements. Before extending the
journal, read its [coverage and storage limits](workspace-journal.md): the
readable-entry cap is not a disk cap, and shared-object GC is not safe alongside
concurrent writers. Preserve these qualifications in model-facing guidance.

## Start

1. Keep a clean Git checkpoint before changing the harness. Work in the actual
   `pkyanam/pk` checkout, not an immutable installed release directory.
2. Run `pk update`, then relaunch. Configure your chosen connection in `/provider`
   (`/providers` is an alias). Workers AI requires a Cloudflare account ID and API
   token; select a discovered text-generation model that supports tool calling.
3. Ask pk to load its bundled `pk` skill. It points to the [documentation index](README.md)
   and repository, and distinguishes available tools from CLI capabilities.
4. Make a focused change, run the relevant checks, commit it, then use
   `pk update --source /absolute/path/to/pk`. Reload only while idle. Use
   `pk rollback` and reload if the installed change regresses.

Keep credentials out of prompts, commits, command arguments, and logs. The guided
provider UI accepts secrets without displaying them. Updating the program does not
replace provider, skill, plugin, MCP, or session configuration.

## Remaining work

[The project checklist](project-checklist.md) is the comprehensive backlog.
Priorities after this handoff:

- Live Workers AI account/model validation, including tool calling and streaming.
- Continued native-terminal verification of copying, dragging files, and long sessions.
- The requested solid, continuously animated fluid-like wordmark, with a small CPU budget.
- Matched-model compaction and coding benchmarks. Correctness fixtures do not establish
  better summary quality, lower cost, or superiority to other harnesses.
- Remaining community-plugin compatibility and broader MCP/ACP interoperability.

See [context management](context-management.md), [provider setup](providers.md),
[self-development audit](self-development-handoff-audit.md), and
[benchmark findings](benchmark-findings.md) for implemented behavior and explicit limits.
The dated [work log](overnight-worklog.md) contains the detailed history.

## Release checkpoint

The final handoff installment is published as **v0.1.7** (`24240a5`). Its immutable tag is the source
recovery checkpoint: `git switch --detach v0.1.7` inspects it without rewriting your
working branch. Create a new branch before making further changes.

See the [release page](https://github.com/pkyanam/pk/releases/tag/v0.1.7) for publication
status and checksummed platform archives. The dated work log records final checks.
Do not equate a source checkpoint with the version currently installed on your Mac.

Codex development is paused after publication at the owner's request. The overnight
automation remains paused. Continue with pk when ready; unfinished backlog remains open.
