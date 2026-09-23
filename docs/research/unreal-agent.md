# Unreal Agent as a foundation for `pk`

**Research date:** 2026-09-22 (America/New_York). Retrieved 2026-09-22 21:35 EDT / 2026-09-23 01:35 UTC; live repository metadata showed an update at 01:34 UTC, so timestamps below reflect that retrieval. Sources reviewed: project announcement, public GitHub repository/API and release metadata, and linked public Harbor benchmark job pages. No downloaded code was executed.

## Finding

Unreal Agent is a relevant Go harness to study, but I recommend a **clean `pk` implementation that selectively adopts its design ideas**, rather than a fork. It is MIT licensed and provides unusually clear seams for asynchronous tools, resumable sessions, and provider adapters. Its announcement and first releases are only dated September 22, 2026; the available benchmark evidence is a small, self-reported comparison and does not establish general SOTA or reliable superiority to Codex. The upstream repository’s commit history includes work dated before its September 21 GitHub creation, so the public release is new but the code was not necessarily written in those two days.

## Release timing and license

GitHub metadata reports the repository created **2026-09-21 23:14 UTC**. Releases **v0.1.0** and **v0.1.1** were published **2026-09-22 16:18 UTC** and **17:09 UTC**. Unreal Labs’ announcement is dated **September 22, 2026**. Thus it was brand-new as a public project/release at research time, with no long public maintenance record to assess. The repo’s commit history includes earlier August and September timestamps, which cautions against interpreting GitHub creation as project inception.

The repository declares the **MIT License**. It permits use, modification, distribution, sublicensing, and sale, subject to retaining the copyright and permission notice in copies or substantial portions; it disclaims warranties and liability. A fork is legally feasible on that basis, with normal notice retention.

Sources: [GitHub repository](https://github.com/unreallabsai/unreal-agent), [releases API](https://api.github.com/repos/unreallabsai/unreal-agent/releases), [LICENSE](https://github.com/unreallabsai/unreal-agent/blob/main/LICENSE), [announcement](https://unreallabs.ai/blog/unreal-agent/).

## What it is, and what is reusable

It is an async-first **Go** library and runner, with a Harbor benchmark adapter. The README describes a coordinator, deduplicating session inbox, append-only session store with recovery/forks, context builder, provider adapter, tool registry/translators, and durable operation manager. The central idea is that a tool call is translated synchronously into serializable work, recorded, then executed asynchronously; results and operation state are persisted separately. This keeps long operations and user steering out of a polling loop managed by the model.

Good ideas to consider for `pk`:

- Keep provider protocol/authentication behind a narrow adapter, and tools behind a registry/interface.
- Separate model tool-call validation/translation from I/O and execution.
- Make long-running operations cancellable and resumable, with explicit operation states and stable IDs.
- Persist canonical session events append-only; record tool-call acceptance and the initial operation atomically to avoid recovery ambiguity.
- Make context assembly expose what was omitted, truncated, or compacted, and keep it free of storage/network dependencies.
- Preserve user steering while tool work runs; make cache-preserving context updates and tool-output size a first-class cost concern.

These design directions are visible in the [README architecture](https://github.com/unreallabsai/unreal-agent/blob/b7c9bf1c5c2fa4127255c07727a7c8413e23944a/README.md), [coordinator interface](https://github.com/unreallabsai/unreal-agent/blob/b7c9bf1c5c2fa4127255c07727a7c8413e23944a/harness/coordinator/coordinator.go), [operation model](https://github.com/unreallabsai/unreal-agent/blob/b7c9bf1c5c2fa4127255c07727a7c8413e23944a/harness/operation/operation.go), and [session-store contract](https://github.com/unreallabsai/unreal-agent/blob/b7c9bf1c5c2fa4127255c07727a7c8413e23944a/harness/sessionstore/sessionstore.go). The announcement flags provider-specific rejections involving the in-progress/final tool-result pattern, so validate protocol compatibility before adopting that mechanism.

The architecture is more than a minimal agent loop: versioned serializable operations, local and remote operation machinery, durable logs, replay/fork support, and a fairly broad provider API surface. That can be valuable if `pk` needs durable asynchronous jobs and recovery from day one; otherwise it is substantial machinery to inherit and understand. Current README says the built-in registry centers on Bash, ViewImage, and skill use; it does not by itself supply all the product, sandbox, permission, or UX decisions `pk` may need.

## Benchmark claims: what the data says

Unreal Labs reports tests with **GPT-6 Astra at xhigh** against Codex and Pi. The published figures are:

| Evaluation | Unreal Agent | Codex | Total cost (Unreal Agent / Codex) |
|---|---:|---:|---:|
| Terminal-Bench 4.0 | 57.9% | 57.9% leaderboard baseline | $1,428 / $2,350 |
| SWE-Atlas Codebase QnA | 65.8% | 63.3% | $936 / $1,303 |
| DeepSWE 1.1 | 72.4% | 69.0% | $1,367 / $1,633 |
| Agents’ Last Exam / ALE-CLI | 30.0% full pass; mean 59.7 | 29.0%; mean 58.1 | $217 / $292 |

The headline claim is “up to 40%” lower cost without performance loss. These are **vendor-reported results**, not independent evidence of SOTA or general superiority to Codex. Terminal-Bench uses a Codex leaderboard baseline; the announcement calls rate differences marginal but gives no uncertainty estimates for these comparisons. Public Harbor records are available for the Terminal-Bench, SWE-Atlas, and DeepSWE Unreal Agent runs; ALE-CLI is outside Harbor.

Sources: [Unreal Labs benchmark tables and caveats](https://unreallabs.ai/blog/unreal-agent/#benchmarks), [Terminal-Bench Harbor run](https://hub.harborframework.com/jobs/27133053-2015-46f3-8c72-6b6910859da6), [SWE-Atlas Harbor run](https://hub.harborframework.com/jobs/3d2fa057-e11c-4be0-a962-e8be81b87709), [DeepSWE Unreal Agent run](https://hub.harborframework.com/jobs/2311ca63-2929-49eb-ba0c-0084ef31ba8b), [DeepSWE Codex run](https://hub.harborframework.com/jobs/e20ecafd-695a-417c-90f5-35fd36d2786f).

## Fork or clean implementation?

**Recommendation: clean implementation; use Unreal Agent as a design reference and benchmark comparator.** For `pk`, this preserves freedom to choose the simplest session model, tool UX, security boundaries, and provider compatibility while avoiding early coupling to a young upstream’s storage format, operation protocol, prompt behavior, and release cadence. Reuse the strongest concepts—especially async operation scheduling, stable IDs/idempotency, persisted event history, and context-build accounting—only where `pk` requirements call for them. Revisit forking if `pk`’s intended scope already matches its durable async runtime closely and upstream compatibility is itself a goal; MIT makes that route available, but ownership of divergence and future merges would then be part of the cost.

This is an architecture and evidence assessment, not a code-quality audit: I inspected primary-source documentation, selected interfaces, metadata, and benchmark records without running the software.
