# Subagents

Children run asynchronously: `SubagentStart` returns an ID without waiting for completion. Continue independent, non-overlapping work; use `SubagentStatus` for an occasional nonblocking check and `SubagentWait` only when a result is needed or independent work is exhausted. Do not poll, sleep, duplicate delegated work, or wait immediately after every launch.

The parent agent can delegate a bounded task to a child through `SubagentStart`, then inspect it with `SubagentStatus`, steer it with `SubagentSend`, wait with `SubagentWait`, or stop it with `SubagentCancel`. Children use the same workspace and configured runner tools as the parent. Nested child spawning is disabled.

Every start must choose one scope explicitly. For investigation or other work with no exclusive file claim, set `task_only: true` and omit `files`. Otherwise leave `task_only: false` and provide 1–64 unique workspace-relative file paths. File claims are coordination hints used to reject overlapping active or queued work; they do not restrict filesystem access, remove write permissions, or create an OS sandbox. Children can still access the same tools and permissions as the parent, so coordinate work that may overlap.

At most two children run at once by default. Up to eight more can wait in FIFO order. A full queue rejects new work. Canceling queued work removes it immediately and releases its file claims. File claims account for lexical parent/child overlaps and resolve existing symlink aliases where possible; they are advisory and cannot guarantee detection of every filesystem alias.

Child tasks are limited to 16 KiB. Tool definitions and child prompts repeat these limits so a model can choose the task-only path without inventing placeholder filenames.

Child tool calls and intermediate commentary stay in the child session, not the
parent transcript or final report. The parent chat shows a compact lifecycle row
and one final reply. `SubagentWait` returns that final reply plus completion/error
metadata; it does not return the accumulated investigation chatter. Usage events
remain available for accounting. Full child history can be inspected through its
session ID. Children should finish with findings, files changed, checks and blockers.

Design reference: [OpenCode Task](https://github.com/anomalyco/opencode/blob/dev/packages/opencode/src/tool/task.ts)
separates background launch from final results and instructs the caller to continue
non-overlapping work. pk retains its explicit Status/Wait tools and fixed child-depth
limit; it does not promise automatic parent-model notification without a wait.
