# Routing questions through AskUser

## Primary-source comparison

- Codex CLI defines `request_user_input` as a structured batch of one to three questions. Each
  question carries an ID, short header, prompt, and option labels/descriptions; the client can add a
  free-form choice. Its tool description focuses on waiting for the user's response. See the
  [Codex tool schema](https://github.com/openai/codex/blob/main/codex-rs/core/src/tools/handlers/request_user_input_spec.rs)
  and [protocol events](https://github.com/openai/codex/blob/main/codex-rs/docs/protocol_v1.md).
- Pi's official example registers a single `question` with `options` and describes it as input
  needed to proceed. See [Pi's question extension](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/examples/extensions/question.ts).
- OpenCode exposes a `question` tool whose payload contains a `questions` array and describes
  clarifying ambiguity, gathering preferences, and deciding implementation choices. Its prompt
  explicitly says when the answer is recommended. See [tool implementation](https://github.com/anomalyco/opencode/blob/dev/packages/opencode/src/tool/question.ts)
  and [tool guidance](https://github.com/anomalyco/opencode/blob/dev/packages/opencode/src/tool/question.txt).
- OpenAI's [Responses async-tool guide](https://developers.openai.com/api/docs/guides/async-tool-calling)
  documents an app-defined asynchronous question tool that returns the user's answer on the same
  tool call. This is a distinct API pattern from Codex CLI's local `request_user_input` and pk's
  blocking foreground tool.

## pk contract

pk keeps its existing `AskUser` tool name and single-question payload: `question`, optional string
`choices`, and optional `kind` (`question` or `confirmation`). The description is now 174 UTF-8 bytes,
down from 213 bytes (39 fewer):

> Ask only when the answer matters. Route each needed question, including follow-ups, through this
> tool—not prose. Make safe assumptions otherwise. This is not tool approval.

The UI still receives one `Question` at a time. When a user prompt explicitly requests questions
one at a time, the model should call `AskUser` again for each follow-up after receiving the previous
answer. Ordinary task prompts should proceed on reasonable assumptions; the tool description does
not ask for routine check-ins. No `runner.go` system-prompt change is required. The new description
is part of the saved tool snapshot, so start a new session to use it; resumed older sessions retain
their original snapshot.

This change does not alter permissions. `AskUser` handles questions and confirmation decisions; it
is not an approval gate for Bash or other tools. It remains foreground-only.

Package tests check the unchanged single-question schema and sequential follow-up lifecycle. For
live model verification, use a fresh session: request two questions one at a time and confirm both
arrive as `AskUser` events, then send a concrete, unambiguous coding prompt and verify that the model
does not ask an unnecessary question. Root ran both checks in separate fresh Luna sessions: the
weekend-planning request produced two `AskUser` events, including the follow-up, without mentioning
the tool; a direct `Create check.txt` task wrote the expected `PK_DIRECT_OK` content with zero
questions. These are bounded observations of the tested prompts and model configuration, not a
guarantee that every prompt will route questions correctly. The first harness attempt accidentally
resent the request after a `ready` event; root corrected the evaluator and reran the direct-task case
separately, with no product defect found.
