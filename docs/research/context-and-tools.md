# Harness research: context and tools

Research snapshot: 22 September 2026. This note informs a small Go harness for `pk`. Vendor reports are first-party operational evidence; results on one provider’s models and workloads are not universal.

## Findings supported by sources

### Keep the execution loop small; make capabilities composable

Anthropic’s architecture guidance recommends the simplest workflow or agent design that meets the task ([Building effective agents](https://www.anthropic.com/engineering/building-effective-agents), 2024-12-19). OpenAI’s API docs distinguish a managed harness, SDK runner, and manually managed conversation state ([Agents guide](https://developers.openai.com/api/docs/guides/agents), accessed 2026-09-22). A June 2026 survey groups runtime duties as observation, context, control, action, state, and verification; it is a synthesis, not a controlled experiment ([From Question Answering to Task Completion](https://arxiv.org/abs/2606.20683), 2026-06-14).

**Design implication:** keep Go’s core to a model loop, typed tool registry, policy checks, and explicit state transitions. Add orchestration only when task evidence requires it.

### Expose a small eager set and discover the long tail on demand

OpenAI documents deferred tool definitions for large catalogs and eager loading for small or frequently used sets. It recommends measuring task success, tokens, and latency; discovered tools append after the stable prefix ([Tool search](https://developers.openai.com/api/docs/guides/tools-tool-search), accessed 2026-09-22). Anthropic recommends clear tool names/descriptions and evaluation-driven iteration ([Writing effective tools for agents](https://www.anthropic.com/engineering/writing-tools-for-agents), 2025-09-11).

**Design implication:** keep common filesystem, patch, shell, and task-state tools available; discover specialized integrations on demand. Resolve only registered tools, validating schemas and permissions in Go.

### Use code orchestration when the flow is predictable and results can be reduced

OpenAI’s guidance: generated code can parallelize or filter predictable tool flows; use direct calls for a single action, adaptive decisions, approvals, and citation-sensitive work. Runtime permission checks still apply ([Programmatic Tool Calling](https://developers.openai.com/api/docs/guides/tools-programmatic-tool-calling), accessed 2026-09-22). An August 2026 preprint reports code-based calling matched/exceeded JSON calling on 11 of 14 models on BFCL v4, while acknowledging limited prior real-world evaluation ([The Bitter Lesson of Tool Calling](https://arxiv.org/abs/2608.06370), 2026-08-08; not peer reviewed).

**Design implication:** offer constrained code execution for deterministic data-heavy orchestration and typed calls for individual or adaptive actions. Apply the same permissions, validation, audit, and timeout rules to both.

### Context is a bounded working set; load evidence just in time

Anthropic recommends high-signal context and just-in-time loading through references instead of preloading all relevant material ([Effective context engineering](https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents), 2025-09-29). OpenAI describes compaction that keeps important state while removing history, a provider implementation rather than a universal guarantee ([From model to agent](https://openai.com/index/equip-responses-api-computer-environment/), 2026). Anthropic’s long-running agent report uses initialization plus durable artifacts between sessions ([Effective harnesses](https://www.anthropic.com/engineering/effective-harnesses-for-long-running-agents), 2025-11-26). A March 2026 terminal-agent preprint describes lazy discovery, adaptive compaction, and project memory, but labels the work in progress ([OPENDEV](https://arxiv.org/abs/2603.05344), v3 2026-03-13).

**Design implication:** persist goal, constraints, decisions, status, next actions, and evidence pointers outside the transcript. Fetch details as needed; compact into inspectable checkpoints at explicit boundaries.

### Stable prefixes improve cache reuse; mutations belong at the end

OpenAI documents that cache reuse requires an identical rendered prefix, including tool ordering and schemas; stable definitions and append-style discovery can preserve it ([Prompt caching](https://developers.openai.com/api/docs/guides/prompt-caching), accessed 2026-09-22). Keep stable instructions and common tools separate from changing task state, then measure cache hits in the target provider.

### Multi-agent work helps when tasks are truly parallel, at higher cost

Anthropic reports its multi-agent research setup beat its single-agent baseline by 90.2% on an internal eval, but used roughly 15× chat tokens; it cautions that coding has fewer parallel tasks than research ([Multi-agent research system](https://www.anthropic.com/engineering/multi-agent-research-system), 2025-06-13). These results are workload-specific, not proof of general advantage.

**Design implication:** add bounded fan-out only for independent tasks, with child budgets, artifacts, cancellation, and synthesis. Keep coupled or sequential work in one agent.

### Evaluate behavior and cost together

Anthropic and OpenAI both recommend evaluating tool design on representative tasks. Track completion, tool/argument correctness, retries, tokens, latency, cache reuse, and permission violations; change one variable at a time.

## Design experiments for `pk` (hypotheses, not established findings)

1. **Core vs deferred catalog:** compare a fixed compact set of tools with a searchable catalog over the same task suite. Hypothesis: discovery lowers context cost on integration-heavy tasks without reducing tool-selection success. Include discovery misses and added round-trip latency.
2. **RPC vs code orchestration:** test direct calls against constrained code orchestration on multi-record lookup, filtering, and aggregation. Hypothesis: code helps when intermediate data is large and control flow is predictable; direct calls remain clearer for adaptive or side-effecting actions. Record correctness, context tokens, latency, and authorization behavior.
3. **Checkpoint formats:** compare plain transcript continuation, structured checkpoint plus recent messages, and tool-backed just-in-time retrieval on tasks spanning multiple context windows. Score whether constraints, decisions, and unfinished work survive, along with total tokens and recovery errors.
4. **Cache-aware prompt layout:** hold stable instructions and tool schemas fixed, append volatile task state and discovered tools, then measure provider cache usage and end-to-end latency. Validate separately for each model/API; caching is implementation-specific.
5. **Delegation threshold:** compare single-agent execution with bounded parallel research for tasks labeled by independent workstreams. Measure quality against the additional token and wall-clock cost. Enable delegation only for task classes where measured benefit exceeds that cost.

## Source register

- OpenAI, [Tool search](https://developers.openai.com/api/docs/guides/tools-tool-search), live API documentation, accessed 2026-09-22.
- OpenAI, [Programmatic Tool Calling](https://developers.openai.com/api/docs/guides/tools-programmatic-tool-calling), live API documentation, accessed 2026-09-22.
- OpenAI, [Prompt caching](https://developers.openai.com/api/docs/guides/prompt-caching), live API documentation, accessed 2026-09-22.
- OpenAI, [Agents guide](https://developers.openai.com/api/docs/guides/agents), docs accessed 2026-09-22.
- Anthropic, [Building effective AI agents](https://www.anthropic.com/engineering/building-effective-agents), 2024-12-19.
- Anthropic, [Effective context engineering for AI agents](https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents), 2025-09-29.
- Anthropic, [Writing effective tools for agents — with agents](https://www.anthropic.com/engineering/writing-tools-for-agents), 2025-09-11.
- Anthropic, [How we built our multi-agent research system](https://www.anthropic.com/engineering/multi-agent-research-system), 2025-06-13.
- Anthropic, [Effective harnesses for long-running agents](https://www.anthropic.com/engineering/effective-harnesses-for-long-running-agents), 2025-11-26.
- Bui, [Building Effective AI Coding Agents for the Terminal](https://arxiv.org/abs/2603.05344), v3 2026-03-13, work in progress.
- Guo et al., [From Question Answering to Task Completion](https://arxiv.org/abs/2606.20683), 2026-06-14, survey preprint.
- OpenAI, [From model to agent: Equipping the Responses API with a computer environment](https://openai.com/index/equip-responses-api-computer-environment/), 2026, accessed 2026-09-22.
- Preprint, [The Bitter Lesson of Tool Calling](https://arxiv.org/abs/2608.06370), 2026-08-08.
