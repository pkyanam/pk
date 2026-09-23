# pk coding-task pilot

- Started: 2026-09-23T07:50:17Z
- Model: `gpt-6-luna` / `low`
- Repetitions: 2
- Per-phase timeout: 1m30s
- pk source revision: `e697977`
- Upstream baseline: Unreal Agent not used (paired pk-only policy ablation)
- Runtime: go1.27.0 darwin/arm64
- Experiment: Large Bash result context replay: current vs compact captured result
- Source tree SHA-256: `d6e2b97028a351c5b500e07aae8a6618ec9db3baedafaad93f205f57dbdab8dc`
- Tracked diff SHA-256: `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`

Both arms use the production CLI, prompt, model and effort, empty skills, and identical Git-initialized task fixtures. The treatment compacts completed Bash result text over the 4096-byte threshold once, at translation, to a 768-rune head and tail, exact existing stdout/stderr capture paths, and exit code. It falls back to the original result if no capture file exists. The fixtures request ordinary verbose Go test output; they do not pad streams or modify tool output limits. Per-run eligible/compacted counts verify treatment exposure. Stored result bytes are a manipulation check only: provider-reported input, cached-input, output tokens and wall time are the efficiency measures. Holdout tests are restored from pristine fixtures after each run. Small stochastic results are descriptive and do not establish general task quality or cache savings.

| Engine | Task | Rep | Phase | Responses | Input | Uncached input | Output | Cached | Eligible / compacted | Result bytes (before / stored) | Missing captures | Wall ms | Correct |
|---|---|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|:---:|
| pk-compact-replayed-output | jobqueue | 1 | implementation | 6 | 17420 | 7180 | 718 | 10240 | 3 / 3 | 14439 / 6033 | 0 | 25977 | — |
| pk-compact-replayed-output | jobqueue | 1 | verification | 3 | 14806 | 3030 | 140 | 11776 | 3 / 3 | 14439 / 6033 | 0 | 7209 | true |
| pk-current | jobqueue | 1 | implementation | 4 | 14411 | 8267 | 588 | 6144 | 1 / 0 | 10460 / 10460 | 0 | 45637 | — |
| pk-current | jobqueue | 1 | verification | 2 | 12547 | 1283 | 96 | 11264 | 1 / 0 | 10460 / 10460 | 0 | 6800 | true |
| pk-compact-replayed-output | jobqueue | 2 | implementation | 8 | 28941 | 13581 | 711 | 15360 | 3 / 3 | 20269 / 6033 | 0 | 24727 | — |
| pk-compact-replayed-output | jobqueue | 2 | verification | 2 | 13404 | 2140 | 128 | 11264 | 3 / 3 | 20269 / 6033 | 0 | 5836 | true |
| pk-current | jobqueue | 2 | implementation | 4 | 15003 | 13467 | 923 | 1536 | 1 / 0 | 6754 / 6754 | 0 | 24091 | — |
| pk-current | jobqueue | 2 | verification | 2 | 13780 | 2516 | 146 | 11264 | 1 / 0 | 6754 / 6754 | 0 | 5819 | true |
