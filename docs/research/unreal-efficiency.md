# Unreal Agent: code mechanisms behind its cost claim

**Scope:** source review at `b7c9bf1c5c2fa4127255c07727a7c8413e23944a`, the pinned checkout in `/tmp/pk-unreal-review.zJ8trx/upstream`. I did not run code or tests. I inspected the coordinator, context builder, tool/operation interfaces, local session store, provider request adapters, and Unreal Labs’ announcement. The report focuses on what the implementation does versus what the benchmark establishes.

## Assessment

Unreal Agent is a serious foundation candidate for `pk` if cost per completed task and asynchronous work matter. The code lets the model launch independent tools, keeps long-running work off the model-call path, and avoids turns spent checking whether processes finished. That can reduce model turns and repeated prompt input. Separately, stable session cache keys and provider cache hints can lower the bill for repeated context without reducing input-token counts.

The public figures are promising, but do not isolate how much async scheduling, context size, or cache hits contribute to lower dollars. I would reuse/adapt the scheduling and cache-aware request path as core architecture, with cost telemetry and a paired benchmark to verify results on `pk` workloads.

## Async tools reduce waiting turns

`Run` selects over user inputs, operation updates, model responses, and optional heartbeats. The model request runs in a goroutine, so the coordinator can process events concurrently; new user input can cancel the in-flight request and rebuild context. This keeps steering responsive ([event loop](https://github.com/unreallabsai/unreal-agent/blob/b7c9bf1c5c2fa4127255c07727a7c8413e23944a/harness/coordinator/loop.go#L118-L209), [request dispatch](https://github.com/unreallabsai/unreal-agent/blob/b7c9bf1c5c2fa4127255c07727a7c8413e23944a/harness/coordinator/loop.go#L346-L388)).

A translator validates a call and returns inert operation descriptions. Unreal persists each call with its initial operations before dispatch; later updates are persisted separately. While work runs, it can send a small placeholder to the model so it continues independent work or ends its turn to wait. An unsubmitted placeholder can be replaced by final output; once committed, the placeholder stays in history and completion appends a new result ([tool contract](https://github.com/unreallabsai/unreal-agent/blob/b7c9bf1c5c2fa4127255c07727a7c8413e23944a/harness/tool/tool.go#L13-L32), [dispatch](https://github.com/unreallabsai/unreal-agent/blob/b7c9bf1c5c2fa4127255c07727a7c8413e23944a/harness/coordinator/loop.go#L763-L827), [result reconciliation](https://github.com/unreallabsai/unreal-agent/blob/b7c9bf1c5c2fa4127255c07727a7c8413e23944a/harness/coordinator/loop.go#L710-L754), [context results](https://github.com/unreallabsai/unreal-agent/blob/b7c9bf1c5c2fa4127255c07727a7c8413e23944a/harness/contextbuilder/builder.go#L103-L137)).

When a response yields only queued operations, the coordinator gives them one second to finish before scheduling another model turn; otherwise it waits on events instead of tight polling. The optional default heartbeat interval is ten minutes, so long waits can still incur check-in turns ([grace period](https://github.com/unreallabsai/unreal-agent/blob/b7c9bf1c5c2fa4127255c07727a7c8413e23944a/harness/coordinator/loop.go#L28-L29), [heartbeat](https://github.com/unreallabsai/unreal-agent/blob/b7c9bf1c5c2fa4127255c07727a7c8413e23944a/harness/coordinator/loop.go#L282-L304), [default](https://github.com/unreallabsai/unreal-agent/blob/b7c9bf1c5c2fa4127255c07727a7c8413e23944a/cmd/internal/agentrunner/run.go#L171-L175)).

This is a credible path to fewer wait/status turns. The preamble explicitly encourages parallel independent calls; shell outputs default to a 40,000-character limit ([preamble](https://github.com/unreallabsai/unreal-agent/blob/b7c9bf1c5c2fa4127255c07727a7c8413e23944a/harness/contextbuilder/prompts/preamble.md#L1-L9), [output limit](https://github.com/unreallabsai/unreal-agent/blob/b7c9bf1c5c2fa4127255c07727a7c8413e23944a/harness/operation/output.go#L8-L36)).

## Cache-aware context is a separate lever

The builder keeps prior outputs as a prefix and new context as a suffix; it does not trim history in the inspected path. Each request gets a stable session cache key, hashed into the configured provider field/header, and deterministic request serialization. These help providers reuse a prefix but still resend the full conversation ([builder](https://github.com/unreallabsai/unreal-agent/blob/b7c9bf1c5c2fa4127255c07727a7c8413e23944a/harness/contextbuilder/builder.go#L23-L29), [build](https://github.com/unreallabsai/unreal-agent/blob/b7c9bf1c5c2fa4127255c07727a7c8413e23944a/harness/contextbuilder/builder.go#L125-L137), [cache key](https://github.com/unreallabsai/unreal-agent/blob/b7c9bf1c5c2fa4127255c07727a7c8413e23944a/harness/coordinator/loop.go#L371-L378), [provider request](https://github.com/unreallabsai/unreal-agent/blob/b7c9bf1c5c2fa4127255c07727a7c8413e23944a/harness/llm/responsesapi/adapter.go#L103-L155)).

OpenRouter uses a one-hour cache TTL and session affinity; its code comments note a cache-write pricing trade-off. This is dollar optimization, not fewer tokens. Unreal also records input, cached input, cache-write, output, and reasoning token counts, which `pk` should expose separately ([OpenRouter config](https://github.com/unreallabsai/unreal-agent/blob/b7c9bf1c5c2fa4127255c07727a7c8413e23944a/harness/llm/clients/openrouter/client.go#L45-L58), [usage fields](https://github.com/unreallabsai/unreal-agent/blob/b7c9bf1c5c2fa4127255c07727a7c8413e23944a/harness/llm/model.go#L118-L135)). Cache hints do not guarantee hits; the announcement does not break out cache-hit/write mix, so its dollar deltas cannot be attributed to caching alone.

## What the reported benchmark supports

Unreal Labs says the “up to 40%” claim compares GPT-6 Astra at xhigh, and publishes the following task rates, total dollars, and mean input/output tokens per trial ([benchmark tables](https://unreallabs.ai/blog/unreal-agent/#benchmarks)):

| Benchmark | Rate Unreal / Codex | Total $ Unreal / Codex | Input/trial Unreal / Codex | Output/trial Unreal / Codex |
|---|---:|---:|---:|---:|
| Terminal-Bench 4.0 | 57.9% / 57.9% leaderboard baseline | 1,428 / 2,350 | 1.73M / — | 32k / — |
| SWE-Atlas Codebase QnA | 65.8% / 63.3% | 936 / 1,303 | 898k / 1.69M | 15k / 17k |
| DeepSWE 1.1 | 72.4% / 69.0% | 1,367 / 1,633 | 1.60M / 2.19M | 28k / 30k |
| ALE-CLI | 30.0% pass; mean 59.7 / 29.0%; mean 58.1 | 217 / 292 | 0.76M / 1.59M | 18k / 15k |

Where paired counts are reported, Unreal has fewer mean input tokens, while output counts are similar (ALE is higher). This is evidence of lower input use on these tasks, not proof that it caused lower dollar totals. Cache mix, provider pricing, and job factors are not separated in the announcement. Terminal-Bench lacks paired Codex token counts and uses a leaderboard rate baseline; ALE-CLI is outside Harbor. Unreal describes rate differences as marginal and possibly benchmark variance. These are promising signals, not independent causal evidence.

## Reuse recommendation

For `pk`, I would favor Unreal as a reuse foundation for async scheduling, durable operations, sessions, and provider adapters. First map `pk`’s security and tool requirements to its Bash/ViewImage/skills defaults; preserve seams for storage and sandbox changes. The inspected builder resends full history and supplies no compaction, so context-growth controls still need a `pk` design.

Validate the economics by logging per-task token classes, billed dollars, turns, tool calls, and success, then compare matched runs with cache on/off and async/waiting baselines. This source review found no causal attribution for the reported savings.
