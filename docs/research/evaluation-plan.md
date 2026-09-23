# How pk should earn “better than Pi”

Research date: 2026-09-22. This is a proposed experiment plan; no pk runtime or measured results exist yet.

## Two comparisons, kept separate

1. **Harness-controlled:** same model snapshot, reasoning settings, task set, time/token budgets, environment, and equivalent capabilities. Compare pk, Pi, and Unreal Agent at pinned commits. This isolates harness choices as far as practical.
2. **Product-default:** each product's recommended setup. This measures the experience users actually get, but cannot attribute differences to the harness alone.

Record system prompts, tool schemas, skills, compaction policy, provider request parameters, dependency versions, CPU allocation and hard limits, memory allocation and hard limits, network policy, cache state, and trial ordering. Interleave runs so one candidate does not systematically get quieter API hours. Publish failures as well as successes.

Agent evals should inspect actual outcomes and traces, with repeat trials for stochastic behavior. See [Anthropic's evaluation guidance](https://www.anthropic.com/engineering/demystifying-evals-for-ai-agents). Resource enforcement is a confounder: Anthropic reported a six-percentage-point spread on Terminal-Bench 2.0 between extreme resource configurations with the model and harness held constant. Match and disclose both reserved resources and hard ceilings ([study](https://www.anthropic.com/engineering/infrastructure-noise)).

## Initial task bank

Start with 20–30 small, independently verifiable tasks; this is a debugging suite, not enough evidence for a broad superiority claim. Keep a held-out set separate from development tasks.

| Task family | Examples | Outcome checks |
| --- | --- | --- |
| Repository work | Bug fix, multi-file change, unfamiliar dependency | Hidden correctness tests, existing tests, scope review |
| Context pressure | Large logs, stale file observations, long interrupted task | Relevant evidence retained; final patch correct |
| Tool orchestration | Slow commands, mixed parallel reads, conflicting writes | Dependencies respected; no lost results or file races |
| Recovery | Crash after dispatch, disconnect, cancellation during child execution | No unreported duplicate effects; explicit uncertain state; no orphan process |
| Extension use | Discover an unfamiliar capability from a large catalog | Correct tool found, valid arguments, bounded discovery cost |
| Instruction boundaries | Untrusted repository/tool content attempts to redirect task | Original task and capability restrictions preserved |

Use deterministic outcome checks where possible. Have humans review a blinded patch sample for maintainability and unnecessary edits. Do not treat the agent's claim that tests passed as evidence. Expand to a pinned public terminal benchmark using [Harbor's custom-agent integration](https://docs.harborframework.com/) once the CLI works.

## Metrics and reporting

- Quality: per-task success, regressions, human acceptance, and failure categories.
- Cost: provider-reported input/output/cached tokens, total spend, spend per successful task, with failures included.
- Latency: end-to-end task time, provider time, tool time, scheduler queue time, cancellation time, p50/p95 across repeated runs.
- Host efficiency: cold startup, idle and peak RSS, process-tree CPU time, allocations, open descriptors, goroutine count, disk bytes and replay time.
- Reliability: leaked processes, duplicate side effects, corrupted/truncated sessions, retries, context-overflow failures.

Report per-task paired differences and uncertainty; bootstrap at the task level rather than pretending repeated trials on one task are independent tasks. Predeclare the comparison and sample size before the held-out run. Small pilot samples identify failures; they do not establish non-inferiority.

For local runtime costs, replay recorded provider streams without network inference, then run a separate live-model suite. Include child processes in resource accounting while also reporting harness-only measurements. High CPU utilization is not itself useful efficiency; measure resources per successful outcome.

## Ablations in order

1. Sequential vs bounded parallel tool scheduling.
2. Full tool catalog vs on-demand schema discovery.
3. Raw outputs vs bounded summaries with retrievable artifacts.
4. Rolling compaction vs structured checkpoints with source references.
5. One agent vs a bounded delegated worker on tasks that can be split.

Change one policy at a time. Preserve the baseline configuration and trajectories. Disable an optimization when quality loss or added complexity exceeds its measured benefit.

## Proposed first decision gate

Choose the implementation foundation after a narrow Unreal integration spike and a minimal independent Go loop can both run the same recovery and orchestration fixtures. Prefer the option with fewer correctness gaps and less adaptation work. Then require credible held-out quality parity before selling lower memory or latency as an overall improvement. Numerical release thresholds should be set from the baseline measurements, not invented before a prototype exists.
