# pk coding-task pilot

- Started: 2026-09-23T12:11:35Z
- Model: `gpt-6-luna` / `low`
- Repetitions: 1
- Tasks: `clamp, intervals`
- Per-phase timeout: 1m30s
- Whole-run timeout: 8m0s
- pk source revision: `8f80856`
- Upstream baseline: Unreal Agent v0.1.1
- Runtime: go1.27.0 darwin/arm64

The same fixtures and two prompts are used for each engine. Implementation creates a new session; verification resumes it and runs/fixes tests. These describe session boundaries only: either phase can include both cached and uncached provider tokens. Cached counts come from provider usage, not an inferred cold/warm classification. Response counts are observed model responses, not hidden HTTP retry attempts. Missing token fields stay marked unavailable; no dollar totals are estimated. The pk and upstream runners have different system prompts and tool/output presentation; results compare these real product configurations and do not isolate one code change. Correctness is checked against pristine fixture tests in a separate holdout directory, and model-produced production Go files are archived under `artifacts/` as `.go.txt` files.

| Engine | Task | Rep | Session mode | Phase | Responses | Input | Output | Cached | Cache write | Wall ms | Correct |
|---|---|---:|---|---|---:|---:|---:|---:|---:|---:|:---:|
| pk | clamp | 1 | new_session | implementation | 3 | 6102 | 183 | 0 | 0 | 10795 | — |
| pk | clamp | 1 | resumed_session | verification | 2 | 6813 | 52 | 5120 | 0 | 4010 | true |
| pk | intervals | 1 | new_session | implementation | 4 | 8596 | 496 | 1536 | 0 | 15007 | — |
| pk | intervals | 1 | resumed_session | verification | 2 | 8206 | 43 | 7168 | 0 | 4457 | true |
