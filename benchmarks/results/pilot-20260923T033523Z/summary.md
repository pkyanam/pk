# pk coding-task pilot

- Started: 2026-09-23T03:35:23Z
- Model: `gpt-6-luna` / `medium`
- Repetitions: 1
- Per-phase timeout: 1m30s
- pk source revision: `b544505` (working-tree changes are not represented by this revision)
- Upstream baseline: Unreal Agent v0.1.1
- Runtime: go1.27.0 darwin/arm64

The same fixtures and two prompts are used for each engine. Implementation creates a new session; verification resumes it and runs/fixes tests. These describe session boundaries only: either phase can include both cached and uncached provider tokens. Cached counts come from provider usage, not an inferred cold/warm classification. Response counts are observed model responses, not hidden HTTP retry attempts. Missing token fields stay marked unavailable; no dollar totals are estimated. The pk and upstream runners have different system prompts and tool/output presentation; results compare these real product configurations and do not isolate one code change. Correctness is checked against pristine fixture tests in a separate holdout directory, and model-produced production Go files are archived under `artifacts/` as `.go.txt` files.

| Engine | Task | Rep | Session mode | Phase | Responses | Input | Output | Cached | Cache write | Wall ms | Correct |
|---|---|---:|---|---|---:|---:|---:|---:|---:|---:|:---:|
| pk | clamp | 1 | new_session | implementation | 3 | 4064 | 370 | 0 | 0 | 14697 | — |
| pk | clamp | 1 | resumed_session | verification | 2 | 4786 | 80 | 3072 | 0 | 4213 | true |
| unreal-v0.1.1 | clamp | 1 | new_session | implementation | 3 | 2281 | 192 | 0 | 0 | 7437 | — |
| unreal-v0.1.1 | clamp | 1 | resumed_session | verification | 2 | 2109 | 40 | 0 | 0 | 3809 | true |
| pk | csvcount | 1 | new_session | implementation | 4 | 5844 | 869 | 0 | 0 | 25404 | — |
| pk | csvcount | 1 | resumed_session | verification | 2 | 5042 | 78 | 3072 | 0 | 4352 | true |
| unreal-v0.1.1 | csvcount | 1 | new_session | implementation | 3 | 4496 | 656 | 0 | 0 | 19586 | — |
| unreal-v0.1.1 | csvcount | 1 | resumed_session | verification | 2 | 6392 | 47 | 5120 | 0 | 4842 | true |
| pk | intervals | 1 | new_session | implementation | 5 | 13825 | 695 | 0 | 0 | 22363 | — |
| pk | intervals | 1 | resumed_session | verification | 2 | 10986 | 47 | 9216 | 0 | 4055 | true |
| unreal-v0.1.1 | intervals | 1 | new_session | implementation | 3 | 2503 | 438 | 0 | 0 | 12393 | — |
| unreal-v0.1.1 | intervals | 1 | resumed_session | verification | 2 | 2528 | 40 | 0 | 0 | 3387 | true |
