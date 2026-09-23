# pk launch copy — review drafts

These drafts describe implemented pk behavior at `0e6947c`. They are for review only and have not been posted.

## Short post options

Each option is under 280 characters including the GitHub link and line break.

**A — Keep work moving**

Meet pk: a Go coding workspace with an OpenTUI terminal. Follow progress and tool activity, delegate bounded subtasks, or leave a durable task running in its own folder and return later.

https://github.com/pkyanam/pk

**B — Long-running work**

Some coding work takes more than one turn. pk keeps a task running in a separate workspace. Attach to follow progress, steer it, cancel it, or resume interrupted work.

https://github.com/pkyanam/pk

**C — Bring your tools**

Explore skills, enable plugins, configure MCP servers, or connect an OpenAI-compatible provider. pk also works as an ACP agent for compatible clients.

https://github.com/pkyanam/pk

**D — Your workspace, your choice**

pk has no first-party telemetry service and keeps its own session history locally. Model, search, and configured integration requests still go to their providers.

https://github.com/pkyanam/pk

## Four-post thread

**1/4**

Meet pk: a Go coding workspace with an OpenTUI terminal. See assistant progress and tool activity together as work moves forward.

**2/4**

For longer jobs, start a durable task in its own workspace. Attach to follow along, send more input, cancel, or resume interrupted work.

**3/4**

Shape your setup with skills, explicitly enabled plugins, MCP servers, and OpenAI-compatible providers. Use pk from compatible clients through ACP.

**4/4**

Search the web with TinyFish directly or through Monid's TinyFish route. Direct TinyFish needs an API key; pk accepts Monid results only when the response confirms zero cost. Start here:

https://github.com/pkyanam/pk

## Demo captions

- **Foreground work:** “Assistant progress and tool results, in one terminal timeline.”
- **Durable task:** “Start work in a separate folder. Attach, steer, or return later.”
- **Subtasks:** “Delegate a bounded task and review its result.”
- **Search:** “Search and fetch through TinyFish; the Monid route fails closed unless it confirms zero cost.”
- **Your setup:** “Choose skills, plugins, MCP connections, and provider.”

## Copy guardrails

- Describe shipped workflows without implying pk outperforms another agent. The pilots are small and mixed; the latest job-queue compaction test used 33.8% more total input tokens in compact mode, so the shipped default remains `full`. See [benchmark findings](benchmark-findings.md).
- Cache counts appear only when the provider reports them. Do not promise cache hits, lower cost, or token savings.
- pk's own storage and integrations have different boundaries. pk operates no first-party telemetry service; model/search/MCP/plugin traffic still goes to configured providers. Do not call pk anonymous or claim external providers collect nothing. See the [data-handling disclosure](privacy.md).
- TinyFish direct access requires a key. The Monid route accepts only responses that explicitly report zero cost and zero billed units; one live validation is not a guarantee of future pricing. See [web search](web-search.md).
- Plugins and MCP servers require explicit configuration. Plugin programs run with pk's operating-system permissions; do not imply that they are sandboxed or bundled in a marketplace.
- Do not claim a license until the repository contains one. Do not describe concept art as a product screenshot.
- These drafts are not authorization to post or run an external campaign.
