# pk coding-task pilot

- Started: 2026-09-23T10:10:29Z
- Model: `gpt-6-luna` / `low`
- Repetitions: 2
- Tasks: `webhook, jobqueue`
- Per-phase timeout: 1m30s
- Whole-run timeout: 15m external SIGINT watchdog (internal selected-task cap: 20m)
- pk source revision: `04503a3`
- Upstream baseline: not used (pk-only policy experiment)
- Runtime: go1.27.0 darwin/arm64
- Experiment: Large Bash result context replay: current vs compact captured result
- Source tree SHA-256: `4946d5f67806fa82a0462cad7c39512a54ca759ea7d96c1247552434c96cdc66`
- Tracked diff SHA-256: `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`
- Replay compaction threshold: 1024 bytes
- Replay head/tail excerpt: 256 runes each

Both arms use the production CLI, prompt, model and effort, empty skills, and identical Git-initialized task fixtures. The run used `-replay-tasks webhook,jobqueue -repetitions 2 -model gpt-6-luna -effort low -timeout 90s`; a 15-minute external SIGINT watchdog was armed and did not fire. The treatment compacts completed Bash result text over the configured 1024-byte threshold once, at translation, to a 256-rune head and tail, exact existing stdout/stderr capture paths, and exit code. It falls back to the original result if no capture file exists. The fixtures request ordinary verbose Go test output; they do not pad streams or modify tool output limits. Per-run eligible/compacted counts verify treatment exposure. Stored result bytes are a manipulation check only: provider-reported input, cached-input, output tokens and wall time are the efficiency measures. Holdout tests are restored from pristine fixtures after each run. Small stochastic results are descriptive and do not establish general task quality or cache savings.

| Engine | Task | Rep | Phase | Responses | Input | Uncached input | Output | Cached | Eligible / compacted | Result bytes (before / stored) | Missing captures | Wall ms | Correct |
|---|---|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|:---:|
| pk-compact-replayed-output-aggressive | jobqueue | 1 | implementation | 15 | 59900 | 14332 | 1508 | 45568 | 12 / 12 | 54223 / 11845 | 0 | 50769 | — |
| pk-compact-replayed-output-aggressive | jobqueue | 1 | verification | 9 | 78208 | 9088 | 665 | 69120 | 18 / 18 | 91975 / 17767 | 0 | 25193 | true |
| pk-current | jobqueue | 1 | implementation | 4 | 14927 | 10319 | 611 | 4608 | 4 / 0 | 14300 / 14300 | 0 | 17766 | — |
| pk-current | jobqueue | 1 | verification | 2 | 12851 | 1587 | 95 | 11264 | 4 / 0 | 14300 / 14300 | 0 | 5237 | true |
| pk-compact-replayed-output-aggressive | jobqueue | 2 | implementation | 22 | 109394 | 21330 | 1844 | 88064 | 16 / 16 | 70961 / 15792 | 0 | 66355 | — |
| pk-compact-replayed-output-aggressive | jobqueue | 2 | verification | 6 | 57450 | 5226 | 326 | 52224 | 19 / 19 | 90814 / 18753 | 0 | 15376 | true |
| pk-current | jobqueue | 2 | implementation | 4 | 15624 | 11016 | 892 | 4608 | 4 / 0 | 14459 / 14459 | 0 | 22778 | — |
| pk-current | jobqueue | 2 | verification | 2 | 14133 | 2869 | 215 | 11264 | 5 / 0 | 16081 / 16081 | 0 | 7164 | true |
| pk-compact-replayed-output-aggressive | webhook | 1 | implementation | 5 | 15617 | 11521 | 2169 | 4096 | 5 / 5 | 35500 / 4936 | 0 | 48382 | — |
| pk-compact-replayed-output-aggressive | webhook | 1 | verification | 17 | 146415 | 16879 | 1661 | 129536 | 18 / 18 | 126995 / 17769 | 0 | 59456 | true |
| pk-current | webhook | 1 | implementation | 4 | 15342 | 10734 | 1258 | 4608 | 4 / 0 | 14950 / 14950 | 0 | 33432 | — |
| pk-current | webhook | 1 | verification | 2 | 14917 | 6725 | 85 | 8192 | 5 / 0 | 17279 / 17279 | 0 | 5725 | true |
| pk-compact-replayed-output-aggressive | webhook | 2 | implementation | 12 | 46738 | 15506 | 1983 | 31232 | 9 / 9 | 45719 / 8884 | 0 | 57574 | — |
| pk-compact-replayed-output-aggressive | webhook | 2 | verification | 4 | 31097 | 3449 | 292 | 27648 | 13 / 13 | 64153 / 12832 | 0 | 12716 | true |
| pk-current | webhook | 2 | implementation | 4 | 16632 | 9464 | 1333 | 7168 | 3 / 0 | 14918 / 14918 | 0 | 31567 | — |
| pk-current | webhook | 2 | verification | 3 | 22628 | 2660 | 215 | 19968 | 4 / 0 | 17247 / 17247 | 0 | 8519 | true |


## Observed cohort totals

The aggressive arm reported 544,819 input tokens (97,331 uncached; 447,488 cached), 10,448 output tokens, and 90 model responses. The current arm reported 127,054 input tokens (55,374 uncached; 71,680 cached), 4,704 output tokens, and 25 responses. All four pristine holdouts per arm passed. The treatment compacted all 110 eligible Bash results (580,340 original bytes to 108,578 stored bytes); the current arm had 33 eligible results and no compaction. Eligibility counts differ because tool/model trajectories differed. Despite smaller stored result text, this cohort shows a substantial usage regression in the aggressive arm; the cached count is a subset of input and is not a savings measure.

The TUI agent ran a full UI test suite on the shared machine during the cohort. Phase wall times are therefore confounded, and this result makes no latency claim. This two-repetition pk-only ablation is descriptive for these fixtures and is not a general quality or efficiency result. The 335-file source snapshot has been verified against its manifest.
