# Build pk on Unreal first

Follow-up investigation, 22 September 2026. Source pinned to [v0.1.1 / b7c9bf1](https://github.com/unreallabsai/unreal-agent/tree/b7c9bf1c5c2fa4127255c07727a7c8413e23944a). Three GPT-6 Luna research agents inspected the implementation; the primary agent ran build, race tests, and an external-module composition spike.

## Revised decision

**Build pk on Unreal, initially as a pinned Go library dependency.** My earlier preference for an independent implementation was premature. Reading the actual contracts and exercising the packages shows reusable infrastructure that closely matches pk's needs. Its coordinator, session journal/replay, asynchronous operations, provider adapters, and tool-result handling are substantial work to retain.

The user is also right to treat efficiency as a meaningful advantage in its own right. The published SWE-Atlas input counts are **898k versus Codex's 1.69M per trial**; ALE-CLI reports **0.76M versus 1.59M per task**. Those figures support the user's point about substantially fewer input tokens on those workloads. The announcement's separate headline is *up to 40% dollar savings*. Input-token reduction and dollar savings differ because output tokens and cache pricing also matter. See [the published tables](https://unreallabs.ai/blog/unreal-agent/#benchmarks) and our [mechanism analysis](unreal-efficiency.md). We have not rerun the paid benchmarks.

## What to preserve

The model requests useful work; the runtime owns waiting, asynchronous completion, and delivery. Submitted context is kept as a committed prefix, while new inputs and results accumulate in a suffix. This lets the agent do independent work without repeatedly managing tool polling. Keeping submitted history stable also avoids needlessly changing potential cache prefixes. These are mechanisms visible in code; the contribution of each mechanism to observed savings still needs ablation.

Keep the existing output-artifact behavior and progressive skill loading too. Unreal already truncates shell output with access to complete captures, and loads skill bodies on demand. Adding these again would not differentiate pk.

## What pk should own

1. **Host and user experience:** configuration, session ownership, interaction, observations, and eventually a UI. Upstream's reusable harness is public, but its runner support package is under `cmd/internal` and cannot be imported by an external pk module.
2. **Context policy:** a combined allowance for newly delivered tool outputs and a measured compaction policy. The current builder retains history; compaction types and handling exist, but not a complete automatic trigger/replacement policy.
3. **Efficiency telemetry:** uncached/cached input, output, reasoning, tool-output size, model turns, and task outcomes. This makes regressions in the behavior we want to preserve visible.
4. **Streaming and extensions where needed:** the current `llm.Adapter` returns a completed response, despite using SSE internally. The built-in registry is fixed, though its interface is replaceable. These are concrete seams to extend, not reasons to discard the runtime.

See the [integration map](unreal-integration.md) for constructor-level details and [experiments](unreal-experiments.md) for changes tied to exact source locations.

## Validation and next step

The upstream build and static checks pass on macOS arm64 with Go 1.27. Its race suite passes in UTC. A local-timezone failure was isolated to an HTTP-date test fixture and verified with a one-line correction; the [validation record and patch](unreal-validation.md) preserve both outcomes.

An [isolated pk module](../../experiments/unreal-composition/README.md) now imports the released library and passes tests for client construction and asynchronous context-prefix preservation. It performs no model calls and is not yet a complete harness.

Next, implement a thin pk host around these packages, with a fake-provider end-to-end run, persisted resume, and cancellation checks. Then compare unchanged Unreal against one pk context-policy change under the same model and budgets. If streaming or custom durable operations require coordinator changes, make a narrow, attributed fork then. Upstream currently says it cannot review/merge PRs, so any fork should assume pk will maintain its own changes ([contribution policy](https://github.com/unreallabsai/unreal-agent/blob/b7c9bf1c5c2fa4127255c07727a7c8413e23944a/CONTRIBUTING.md)).
