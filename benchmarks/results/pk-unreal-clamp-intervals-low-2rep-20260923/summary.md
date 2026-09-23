# pk coding-task pilot

- Started: 2026-09-23T09:37:42Z
- Model: `gpt-6-luna` / `low`
- Repetitions: 2
- Tasks: `clamp, intervals`
- Per-phase timeout: 1m30s
- Whole-run timeout: 15m0s
- pk source revision: `0069ac3`
- Source tree SHA-256: `6df3033c59b2fcb20c2c017935c902cd12f14ce3a38f87eed57d2cbdf12ecf67`
- Tracked benchmark-source diff SHA-256: `0e59ac3fff0ab15ebe447934c430fb37c6de41b335f645fd6a8c95eb7abcd4e8`
- Upstream baseline: Unreal Agent v0.1.1
- Runtime: go1.27.0 darwin/arm64

The same fixtures and two prompts are used for each engine. Implementation creates a new session; verification resumes it and runs/fixes tests. These describe session boundaries only: either phase can include both cached and uncached provider tokens. Cached counts come from provider usage, not an inferred cold/warm classification. Response counts are observed model responses, not hidden HTTP retry attempts. Missing token fields stay marked unavailable; no dollar totals are estimated. The pk and upstream runners have different system prompts and tool/output presentation; results compare these real product configurations and do not isolate one code change. Correctness is checked against pristine fixture tests in a separate holdout directory, and model-produced production Go files are archived under `artifacts/` as `.go.txt` files. The exact source tree and tracked diff are preserved in `source-snapshot/` and `source-manifest.json`.

Across the eight phase records per engine, pk reported 43,889 input tokens (28,017 uncached; 15,872 cached) and 1,752 output tokens. Unreal reported 20,011 input tokens (all reported uncached) and 1,333 output tokens. Each engine had 22 recorded model responses and all four holdout checks passed. Summed process wall time was 68.7s for pk and 60.9s for Unreal; these are descriptive totals, not a latency comparison. Provider usage counters are reported as supplied, with no dollar estimate.

This was one small, stochastic cohort with all 8 task/engine/repetition combinations passing holdout tests. During the run, Go/UI validation and release staging shared the same machine, and engine order was fixed with pk first within each task/repetition. Those conditions confound wall-time comparisons; do not interpret this run as a latency ranking or a general quality/cost result.

| Engine | Task | Rep | Session mode | Phase | Responses | Input | Output | Cached | Cache write | Wall ms | Correct |
|---|---|---:|---|---|---:|---:|---:|---:|---:|---:|:---:|
| pk | clamp | 1 | new_session | implementation | 2 | 2689 | 144 | 0 | 0 | 6394 | — |
| pk | clamp | 1 | resumed_session | verification | 3 | 5138 | 178 | 1536 | 0 | 7988 | true |
| unreal-v0.1.1 | clamp | 1 | new_session | implementation | 3 | 2192 | 158 | 0 | 0 | 7612 | — |
| unreal-v0.1.1 | clamp | 1 | resumed_session | verification | 2 | 1924 | 39 | 0 | 0 | 3389 | true |
| pk | clamp | 2 | new_session | implementation | 3 | 6214 | 354 | 0 | 0 | 10353 | — |
| pk | clamp | 2 | resumed_session | verification | 2 | 7089 | 52 | 5120 | 0 | 4082 | true |
| unreal-v0.1.1 | clamp | 2 | new_session | implementation | 3 | 2279 | 190 | 0 | 0 | 7694 | — |
| unreal-v0.1.1 | clamp | 2 | resumed_session | verification | 2 | 2105 | 40 | 0 | 0 | 4913 | true |
| pk | intervals | 1 | new_session | implementation | 4 | 6770 | 420 | 1536 | 0 | 14474 | — |
| pk | intervals | 1 | resumed_session | verification | 2 | 4404 | 43 | 3072 | 0 | 3515 | true |
| unreal-v0.1.1 | intervals | 1 | new_session | implementation | 4 | 2791 | 372 | 0 | 0 | 11688 | — |
| unreal-v0.1.1 | intervals | 1 | resumed_session | verification | 2 | 2148 | 40 | 0 | 0 | 4075 | true |
| pk | intervals | 2 | new_session | implementation | 4 | 6783 | 512 | 1536 | 0 | 16465 | — |
| pk | intervals | 2 | resumed_session | verification | 2 | 4802 | 49 | 3072 | 0 | 5397 | true |
| unreal-v0.1.1 | intervals | 2 | new_session | implementation | 4 | 3530 | 454 | 0 | 0 | 13465 | — |
| unreal-v0.1.1 | intervals | 2 | resumed_session | verification | 2 | 3042 | 40 | 0 | 0 | 8041 | true |
