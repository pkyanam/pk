# Context-history compaction: design and evaluation

Context-history compaction replaces an older portion of a model-visible
conversation with a checkpoint while retaining the durable transcript. It is
different from truncating a completed tool result and should be judged by
recovery quality before token reduction.

## Source-backed patterns

- OpenAI's Responses API supports server-triggered compaction at a configured
  threshold and an explicit stateless `/responses/compact` operation. Both
  return a provider-defined compaction item; callers must carry the returned
  item forward. This is useful on supported OpenAI Responses routes, but it is
  not a portable behavior to assume for every provider or custom endpoint.
  See the [Compaction guide](https://developers.openai.com/api/docs/guides/compaction).
- Pi's documented local approach checks projected history around turn
  boundaries, summarizes older complete history, retains a recent tail, and
  records a compaction entry pointing at the first retained entry. The original
  session entries remain on disk. Its documented summary template includes
  goal, constraints, progress, decisions, next steps, and critical context.
  See [Pi's compaction reference](https://pi.dev/docs/latest/compaction) and
  [session/context guide](https://pi.dev/docs/latest/sessions).
- Anthropic's context-engineering guidance emphasizes high-signal context and
  just-in-time evidence rather than indiscriminately loading everything. That
  supports treating compaction as a relevance and recovery problem, not as
  string shortening alone. See [Effective context engineering](https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents).

These are design references, not comparative evidence that one method is
universally better. Provider-side opaque compaction and harness-managed
summaries are distinct mechanisms with different portability and observability.

## Current pk boundary

The shipped `context_policy=compact` behavior applies only to eligible large,
completed Bash output: it presents a bounded summary to later model context
while retaining the exact local capture and a durable decision sidecar. See
[`outputpolicy.go`](../../internal/runner/outputpolicy.go). It does not
summarize or discard older user/assistant/tool history.

A separate context-history engine is under development. The current design
uses a provider context budget only when verified or explicitly overridden;
otherwise it labels an operational fallback estimate rather than presenting
it as a model limit. Its working set must reserve output room, safety margin,
and compaction-summary allowance. Only complete prefix exchanges are eligible
to summarize. The stable system prompt, tool definitions, current user prompt
and attachments, and unresolved/in-flight tool calls and results remain intact.
The full transcript remains durable. A checkpoint projection is to be stored
per session and bound to the exact transcript prefix fingerprint so resume can
reuse it only when that prefix still matches. A manual `/compact` operation is
planned to record a checkpoint without inventing a user-visible conversation
turn.

These are design constraints, not a claim that automatic history compaction or
`/compact` is released. Avoid equating byte-based request measurements with
tokens. Provider-reported usage is authoritative only for the counters that
provider supplies; it does not verify factual retention.

## Quality-first evaluation

The deterministic [long-history fixture](../../benchmarks/experiments/context-compaction/README.md)
tests an early no-dependency/API contract, exact tool evidence, an untrusted
instruction inside tool output, multiple user updates, a low-signal long log,
and a latest unresolved task. Its held-out assertions check recovery and
forbidden actions rather than summary wording. The evaluation compares full
history with compacted history on matched fresh sessions and checks the
resulting workspace independently.

Keep per-assertion quality, critical constraint violations, test success,
invented claims, tool/response counts, provider usage, and latency separate.
Any lost safety constraint, tool-evidence inversion, untrusted-instruction
obedience, or false completion report is a quality failure regardless of token
or time savings. Keep the durable transcript available for audit and recovery.
