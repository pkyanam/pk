# Context-history compaction: design and evaluation

Context-history compaction replaces an older portion of a model-visible
conversation with a checkpoint while retaining the durable transcript. It is
different from truncating a completed tool result and should be judged by
recovery quality before token reduction.

## Source-backed patterns

- OpenAI's Responses API supports server-triggered compaction at a configured
  threshold and an explicit stateless `/responses/compact` operation. For the
  standalone endpoint, the complete returned compacted window is canonical:
  pass it to the next request as-is and do not prune it. Server-side compaction
  has a different chaining rule: input-array callers append response output,
  while `previous_response_id` callers send only the new user message. This is
  specific to supported Responses routes, not portable behavior to assume for
  every provider or custom endpoint. See the
  [Compaction guide](https://developers.openai.com/api/docs/guides/compaction).
- Pi's documented local approach checks projected history around turn
  boundaries, summarizes older complete history, retains a recent tail, and
  records a compaction entry pointing at the first retained entry. The original
  session entries remain recoverable, and a tool result is not cut away from
  its tool call. Its documented summary template includes goal, constraints,
  progress, decisions, next steps, and critical context. See [Pi's compaction
  reference](https://pi.dev/docs/latest/compaction), [session/context
  guide](https://pi.dev/docs/latest/sessions), and the [Pi project compaction
  docs](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/compaction.md).
- OpenCode's V2 session spec describes a pre-request budget check, a completed
  structured checkpoint plus bounded recent context, and keeping the full
  transcript durable. The current main-branch implementation groups history
  at user-message boundaries and separately prunes older completed tool
  results. The V2 spec and implementation are evolving; these are design
  references, not a claim that every detail is released behavior. See the
  [V2 compaction docs](https://opencode.ai/v2/docs/compaction), [session
  spec](https://github.com/anomalyco/opencode/blob/dev/specs/v2/session.md#automatic-compaction), and [implementation](https://github.com/anomalyco/opencode/blob/dev/packages/opencode/src/session/compaction.ts).
- Codex's public Rust source builds replacement history from an initial
  context, a token-bounded selection of recent user messages (up to its
  20,000-token cap), and a compaction summary. Its overflow path also removes
  older history items to retry within budget. This is Codex implementation
  behavior, not a feature guaranteed by pk's current model backend. See
  [Codex `compact.rs`](https://github.com/openai/codex/blob/main/codex-rs/core/src/compact.rs).
- Hermes' current compressor docs describe a staged local approach: clear
  older large tool-result payloads outside the protected tail, summarize the
  middle, keep a token-budgeted tail, and retain pre-compaction turns as
  searchable/recoverable archived storage in its default in-place mode. See
  [Hermes Context Compression and Caching](https://hermes-agent.nousresearch.com/docs/developer-guide/context-compression-and-caching).
- Anthropic's context-engineering guidance emphasizes high-signal context and
  just-in-time evidence rather than indiscriminately loading everything. That
  supports treating compaction as a relevance and recovery problem, not as
  string shortening alone. See [Effective context engineering](https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents).

These are concrete design references, not comparative evidence that one method
is universally better. Provider-side opaque compaction and harness-managed
summaries are distinct mechanisms with different portability and observability.

## Current pk boundary

The shipped `context_policy=compact` behavior applies only to eligible large,
completed Bash output: it presents a bounded summary to later model context
while retaining the exact local capture and a durable decision sidecar. See
[`outputpolicy.go`](../../internal/runner/outputpolicy.go). It does not
summarize or discard older user/assistant/tool history.

A separate context-history engine is implemented in the current source and
pending release. It uses a provider context budget only when available from a
supported catalog or explicit override; otherwise it labels an operational
fallback rather than presenting it as a model limit. Its working set reserves
output room, a safety margin, and compaction-summary allowance. Only complete
prefix exchanges are eligible to summarize. System instructions, tool
definitions, the current user prompt and attachments, and unresolved/in-flight
tool calls and results remain intact. The full transcript remains durable. A
private checkpoint projection is bound to the exact transcript-prefix
fingerprint and is reused only while that prefix matches. Manual `/compact`
records a checkpoint without inventing a conversation turn. See the user
[context-management guide](../context-management.md) for defaults, limits, and
refusal cases.

These source behaviors are not a claim that a release containing them has been
published. Avoid equating byte-based request measurements with tokens.
Provider-reported usage is authoritative only for the counters that provider
supplies; it does not verify factual retention.

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
