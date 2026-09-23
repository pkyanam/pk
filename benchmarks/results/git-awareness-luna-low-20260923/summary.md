# pk coding-task pilot

- Started: 2026-09-23T12:18:05Z
- Model: `gpt-6-luna` / `low`
- Repetitions: 1
- Tasks: `clamp, intervals`
- Per-phase timeout: 1m30s
- Whole-run timeout: 8m0s
- pk source revision: `b7a93a2`
- Upstream baseline: Unreal Agent v0.1.1
- Runtime: go1.27.0 darwin/arm64

The same fixtures and two prompts are used for each engine. Implementation creates a new session; verification resumes it and runs/fixes tests. These describe session boundaries only: either phase can include both cached and uncached provider tokens. Cached counts come from provider usage, not an inferred cold/warm classification. Response counts are observed model responses, not hidden HTTP retry attempts. Missing token fields stay marked unavailable; no dollar totals are estimated. The pk and upstream runners have different system prompts and tool/output presentation; results compare these real product configurations and do not isolate one code change. Correctness is checked against pristine fixture tests in a separate holdout directory, and model-produced production Go files are archived under `artifacts/` as `.go.txt` files.

| Engine | Task | Rep | Session mode | Phase | Responses | Input | Output | Cached | Cache write | Wall ms | Correct |
|---|---|---:|---|---|---:|---:|---:|---:|---:|---:|:---:|
| pk | clamp | 1 | new_session | implementation | 4 | 6144 | 235 | 0 | 0 | 12666 | — |
| pk | clamp | 1 | resumed_session | verification | 2 | 3805 | 55 | 3072 | 0 | 5042 | true |
| pk | intervals | 1 | new_session | implementation | 4 | 6969 | 422 | 1536 | 0 | 13911 | — |
| pk | intervals | 1 | resumed_session | verification | 2 | 4818 | 43 | 3072 | 0 | 7191 | true |
