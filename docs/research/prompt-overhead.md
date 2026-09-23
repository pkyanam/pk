# Prompt overhead: pk vs Unreal v0.1.1

## What the measurements say

The matched clamp/intervals pilot recorded 43,889 input tokens for `pk` and
20,011 for pinned Unreal Agent v0.1.1, with 22 model responses per engine and
all four holdout checks passing. This is a 2.19x difference in that small
cohort, not a measurement of prompt overhead alone: the engines have different
system prompts, tool sets, execution traces, and cached-input accounting. The
pilot is stochastic and does not establish a general cost or quality result.
See the [benchmark record](../results/pk-unreal-clamp-intervals-low-2rep-20260923/summary.md).

## Deterministic request-size inspection

I captured the initial request locally by running the production `runner.Run`
with a fake adapter that records the request and returns a fixed final answer.
The test used empty temporary home/config paths and made no provider call. It
activated the same extension/subagent setup as the headless CLI, with no
configured MCP server, plugin, or discovered skill. Counts below are UTF-8
bytes of request fields, not token estimates.

| Captured component | Bytes | Notes |
|---|---:|---|
| Full `pk` system message | 2,766 | Includes the common harness preamble, `pk` instructions, and workspace/platform fields. |
| Serialized `Bash` + `ViewImage` definitions | 924 | Core tool pair. |
| Those two plus `SkillUse` | 1,168 | `SkillUse` was registered although this fixture had no discovered skills. |
| All eight serialized tool definitions | 3,297 | Adds five subagent tools to the prior set. |
| System message plus all tool definitions | 6,063 | Excludes conversation, dynamic tool results, and provider framing. |

The pinned Unreal v0.1.1 context-builder preamble is 1,371 bytes (253 words).
Its default system prompt is 199 bytes; the benchmark runner supplies that
default unless explicitly overridden. The `pk` system message is 2,766 bytes
in the capture, so its additional workspace-agent instructions and dynamic
fields account for 1,395 bytes beyond the shared preamble. Unreal's benchmark
configuration exposed `Bash` and `ViewImage`; `SkillUse` is conditional on
discovered skills. The extra subagent schemas account for 2,129 bytes in this
capture. Exact request composition can vary with platform, workspace path,
skills, plugins, MCP, and enabled providers.

The most obvious request-size choice is exposing `SubagentStart`, `Status`,
`Send`, `Wait`, and `Cancel` on every headless run. The benchmark traces used
`Bash` calls and do not show these tools being needed for either fixture. A
lazy or opt-in subagent capability is therefore a plausible experiment, but
it changes what the model can do and should be measured as a product treatment,
not assumed to be a behavior-preserving deletion. Keep the normal core tools
and their current semantics in any such comparison.

## Safe candidates for a measured trim

The workspace instructions repeat some general runtime guidance. A concise
candidate could combine “inspect before editing,” “keep changes focused,” and
“report verified results truthfully” into one sentence while retaining the
plan/update rule for substantial tasks. Any edit must preserve these explicit
requirements:

- identify `pk` as the local coding agent and use the supplied workspace;
- inspect relevant code, make focused changes, verify them, and report only
  checks actually run;
- give a brief plan and meaningful progress updates for substantial work,
  without timer-based or progress-only filler;
- never expose hidden reasoning, credentials, or other secrets;
- follow the nearest nested `AGENTS.md`, while loading only the root file
  automatically;
- ask when essential information or a consequential choice is missing.

The foreground `AskUser` interaction is not available in this headless
benchmark path. Do not trim the “ask” instruction into an implied permission
tool or claim the headless agent can pause for a user answer; it should request
missing information through its normal response. No permission policy is
added by this analysis.

There is already a small tool-description ablation. It cut 121 UTF-8 bytes
from five descriptions per request without changing tool schemas or behavior.
In two repetitions it did not reduce total provider-reported input; aggregate
input was higher in the compact arm, with substantial cached/uncached
variation. This is a useful warning that byte savings do not directly predict
token savings or quality. See the [ablation report](../experiments/tool-schema.md).

## Recommended next experiment

Keep production defaults unchanged. Run a counterbalanced, repeated ablation
that changes only the redundant workspace-prompt phrasing, then separately
test subagent-tool availability as an explicit capability treatment. Record
actual provider input/cached-input counters and holdout correctness. Report
the treatment's byte delta as a manipulation check, never as an exact token
prediction. Retain the identity, progress, persistence, `AGENTS.md`, secret,
and user-question requirements listed above in every arm.
