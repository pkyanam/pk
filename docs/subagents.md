# Subagents

The parent agent can delegate a bounded task to a child through `SubagentStart`, then inspect it with `SubagentStatus`, steer it with `SubagentSend`, wait with `SubagentWait`, or stop it with `SubagentCancel`. Children use the same workspace and configured runner tools as the parent. Nested child spawning is disabled.

Every start must choose one scope explicitly. For investigation or other work with no exclusive file claim, set `task_only: true` and omit `files`. Otherwise leave `task_only: false` and provide 1–64 unique workspace-relative file paths. File claims are coordination hints used to reject overlapping active or queued work; they do not restrict filesystem access, remove write permissions, or create an OS sandbox. Children can still access the same tools and permissions as the parent, so coordinate work that may overlap.

At most two children run at once by default. Up to eight more can wait in FIFO order. A full queue rejects new work. Canceling queued work removes it immediately and releases its file claims. File claims account for lexical parent/child overlaps and resolve existing symlink aliases where possible; they are advisory and cannot guarantee detection of every filesystem alias.

Child tasks are limited to 16 KiB. Tool definitions and child prompts repeat these limits so a model can choose the task-only path without inventing placeholder filenames.
