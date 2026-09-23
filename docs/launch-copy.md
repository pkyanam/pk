# pk launch copy — review drafts

These drafts describe the current terminal workspace. They are for review and have not been posted.
They make no comparative performance claim and do not promise cache hits.

## Short post options

**A — Keep work moving**

Meet pk: a coding workspace for the terminal. Work in a live OpenTUI session, delegate bounded subtasks, or leave a durable task running and return to its progress.
https://github.com/pkyanam/pk

**B — Built for the long run**

Some coding work takes more than one turn. pk keeps tasks resumable, shows tool activity as it happens, and lets you steer work from the terminal.
https://github.com/pkyanam/pk

**C — Your tools, your provider**

Use skills, explicitly enabled plugins, and MCP tools in pk. Choose the built-in ChatGPT/Codex provider or configure an OpenAI-compatible endpoint.
https://github.com/pkyanam/pk

**D — Your workspace. Your choice.**

Meet pk: a terminal coding harness with no first-party usage analytics. Keep session history on your machine, choose your model provider, and connect the tools you want.

Your selected provider still receives the context needed to do the work. Privacy starts with making those boundaries clear.
https://github.com/pkyanam/pk

## Four-post thread

**1/4**

Meet pk: a coding workspace for the terminal. Follow progress and tool activity in a live OpenTUI session, or hand longer work to a durable task.

**2/4**

Tasks can run in their own workspace. Attach to follow along, send more input, cancel, or resume saved work.

**3/4**

Bring your setup: discover skills, enable plugins, connect MCP tools, or use pk through ACP. Choose the built-in ChatGPT/Codex provider or configure an OpenAI-compatible one.

**4/4**

Update and roll back from the terminal. Start here:
https://github.com/pkyanam/pk

## Demo captions

- **Foreground session:** “Progress and tool activity, together in the terminal.”
- **Long task:** “Leave a task running, attach to its progress, and steer it as it works.”
- **Subtasks:** “Delegate a bounded piece of work and follow its result.”
- **Your setup:** “Use skills, plugins, and MCP tools with the provider you choose.”

## Copy guardrails

- Go is the engine and OpenTUI is the terminal interface. Describe shipped workflows without implying a performance advantage over another agent.
- Cache counts are shown only when a provider supplies them. Do not promise a cache hit, lower cost, or token savings. The [small paired-task pilot](benchmark-findings.md) is exploratory and is not a general performance result.
- The center image in the README is concept art, not a product screenshot. Replace the screenshot-coming-soon note only after a current UI capture is reviewed.
- Plugins and MCP servers are explicitly configured; do not imply a bundled marketplace or that arbitrary plugin programs are sandboxed. Bash and extension processes use pk's operating-system permissions.
- Do not claim open-source or other licensing terms until the repository has a selected license.
- These are drafts. Do not post them or use them in an external campaign without explicit authorization.

## Privacy positioning

Lead with control and transparency, not browser comparisons or claims of anonymity. pk stores history locally and does not operate a telemetry service. Model requests, search, and configured integrations still contact their respective services. Link the [data-handling disclosure](privacy.md) beside privacy claims. Local files are protected by filesystem permissions, not encrypted by pk.
