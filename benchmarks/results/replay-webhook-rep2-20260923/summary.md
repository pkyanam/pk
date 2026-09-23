# pk coding-task pilot

- Started: 2026-09-23T06:51:30Z
- Model: `gpt-6-luna` / `medium`
- Repetitions: 2
- Per-phase timeout: 1m30s
- pk source revision: `226b07c`
- Upstream baseline: Unreal Agent not used (paired pk-only policy ablation)
- Runtime: go1.27.0 darwin/arm64
- Experiment: Large Bash result context replay: current vs compact captured result
- Source tree SHA-256: `fe1cfdf0aa8b825b712d4c280f7c996180a50984d5d3a58315679c987f7a9d0f`
- Tracked diff SHA-256: `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`

Both arms use the production CLI, prompt, model and effort, empty skills, and identical Git-initialized task fixtures. The treatment compacts completed Bash result text over the 4096-byte threshold once, at translation, to a 768-rune head and tail, exact existing stdout/stderr capture paths, and exit code. It falls back to the original result if no capture file exists. The fixtures request ordinary verbose Go test output; they do not pad streams or modify tool output limits. Per-run eligible/compacted counts verify treatment exposure. Stored result bytes are a manipulation check only: provider-reported input, cached-input, output tokens and wall time are the efficiency measures. Holdout tests are restored from pristine fixtures after each run. Small stochastic results are descriptive and do not establish general task quality or cache savings.

| Engine | Task | Rep | Phase | Responses | Input | Uncached input | Output | Cached | Eligible / compacted | Result bytes (before / stored) | Missing captures | Wall ms | Correct |
|---|---|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|:---:|
| pk-compact-replayed-output | clamp | 1 | implementation | 3 | 4204 | 4204 | 286 | 0 | 0 / 0 | 0 / 0 | 0 | 9306 | — |
| pk-compact-replayed-output | clamp | 1 | verification | 2 | 3553 | 2017 | 81 | 1536 | 0 / 0 | 0 / 0 | 0 | 5971 | true |
| pk-current | clamp | 1 | implementation | 3 | 4518 | 4518 | 299 | 0 | 0 / 0 | 0 / 0 | 0 | 11982 | — |
| pk-current | clamp | 1 | verification | 2 | 3885 | 2349 | 81 | 1536 | 0 / 0 | 0 / 0 | 0 | 4287 | true |
| pk-compact-replayed-output | clamp | 2 | implementation | 3 | 4168 | 4168 | 322 | 0 | 0 / 0 | 0 / 0 | 0 | 9483 | — |
| pk-compact-replayed-output | clamp | 2 | verification | 2 | 3607 | 2071 | 81 | 1536 | 0 / 0 | 0 / 0 | 0 | 5375 | true |
| pk-current | clamp | 2 | implementation | 3 | 4529 | 4529 | 321 | 0 | 0 / 0 | 0 / 0 | 0 | 11524 | — |
| pk-current | clamp | 2 | verification | 2 | 4026 | 954 | 90 | 3072 | 0 / 0 | 0 / 0 | 0 | 5283 | true |
| pk-compact-replayed-output | webhook | 1 | implementation | 4 | 12006 | 7910 | 1496 | 4096 | 2 / 2 | 10956 / 4022 | 0 | 32260 | — |
| pk-compact-replayed-output | webhook | 1 | verification | 3 | 18707 | 3859 | 201 | 14848 | 3 / 3 | 16274 / 6033 | 0 | 12645 | true |
| pk-current | webhook | 1 | implementation | 5 | 22757 | 8933 | 1408 | 13824 | 2 / 0 | 11582 / 11582 | 0 | 38086 | — |
| pk-current | webhook | 1 | verification | 2 | 14832 | 8176 | 84 | 6656 | 2 / 0 | 11582 / 11582 | 0 | 5360 | true |
| pk-compact-replayed-output | webhook | 2 | implementation | 6 | 25710 | 11374 | 2392 | 14336 | 2 / 2 | 11196 / 4022 | 0 | 52894 | — |
| pk-compact-replayed-output | webhook | 2 | verification | 2 | 16385 | 9729 | 531 | 6656 | 4 / 4 | 23066 / 8044 | 0 | 16609 | true |
| pk-current | webhook | 2 | implementation | 5 | 23198 | 17054 | 1949 | 6144 | 2 / 0 | 11404 / 11404 | 0 | 47903 | — |
| pk-current | webhook | 2 | verification | 4 | 41760 | 5920 | 527 | 35840 | 4 / 0 | 21738 / 21738 | 0 | 17314 | true |
