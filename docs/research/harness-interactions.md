# Structured questions and progress events

This note compares upstream interaction contracts with pk's current surfaces. It is a design
reference, not a claim that pk implements every upstream interaction.

## Questions and approvals are different

Codex's app-server models `requestUserInput` as a server request associated with the active turn; the
client returns a structured answer and the turn continues. Codex also has separate approval requests
for tool execution. See the [app-server protocol](https://github.com/openai/codex/blob/main/codex-rs/app-server/README.md)
and [event types](https://github.com/openai/codex/blob/main/sdk/typescript/src/events.ts).

OpenCode similarly has a structured [`question` tool](https://github.com/anomalyco/opencode/blob/dev/packages/opencode/src/tool/question.ts)
with an answer/reject lifecycle, while its [permission system](https://opencode.ai/docs/agents)
separately decides whether tools are allowed, denied, or require approval. A product decision should
not be represented as a tool permission prompt.

pk's planned `AskUser` interaction is scoped to foreground sessions and is separate from tool
permissions. Detached tasks can accept explicit follow-up text from their host, but do not have a
persisted `awaiting_input` state or typed question/approval reply path. Keep detached task prompts
self-contained; pk does not promise that an unattended worker can pause for a model-generated
question. Non-interactive question waits are a known source of stuck runs in other coding-agent CLIs;
see [OpenCode issue #11899](https://github.com/anomalyco/opencode/issues/11899).

This interaction does not change pk's execution permissions. Bash still runs with the pk process's
user permissions and is not confined to the selected workspace.

## Progress event reference

Codex's JSONL event contract is a useful reference for chronological progress: stable turn IDs,
item-level start/update/completion, explicit failure, and final usage. See the
[Codex TypeScript event definitions](https://github.com/openai/codex/blob/main/sdk/typescript/src/events.ts).
pk's local RPC and runner events are its own contract; they are not Codex JSONL-compatible. See
[pk architecture](../architecture.md) for the current implementation boundary.
