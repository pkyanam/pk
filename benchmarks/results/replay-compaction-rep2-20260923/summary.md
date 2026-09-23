# pk coding-task pilot

- Started: 2026-09-23T05:58:47Z
- Model: `gpt-6-luna` / `medium`
- Repetitions: 2
- Per-phase timeout: 1m30s
- pk source revision: `8e2783d`
- Upstream baseline: Unreal Agent not used (paired pk-only policy ablation)
- Runtime: go1.27.0 darwin/arm64
- Experiment: Large Bash result context replay: current vs compact captured result
- Source tree SHA-256: `4dbf008eeaac5a791fbef3d5851f498f8177a675d964d3927500cee330f795d8`
- Tracked diff SHA-256: `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`

Both arms use the production CLI, prompt, model and effort, empty skills, and identical Git-initialized task fixtures. The treatment compacts completed Bash result text over the 4096-byte threshold once, at translation, to a 768-rune head and tail, exact existing stdout/stderr capture paths, and exit code. It falls back to the original result if no capture file exists. The fixtures request ordinary verbose Go test output; they do not pad streams or modify tool output limits. Per-run eligible/compacted counts verify treatment exposure. Stored result bytes are a manipulation check only: provider-reported input, cached-input, output tokens and wall time are the efficiency measures. Holdout tests are restored from pristine fixtures after each run. Small stochastic results are descriptive and do not establish general task quality or cache savings.

| Engine | Task | Rep | Phase | Responses | Input | Uncached input | Output | Cached | Eligible / compacted | Result bytes (before / stored) | Missing captures | Wall ms | Correct |
|---|---|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|:---:|
| pk-compact-replayed-output | eventmerge | 1 | implementation | 6 | 13892 | 8260 | 815 | 5632 | 1 / 1 | 7618 / 2009 | 0 | 24722 | — |
| pk-compact-replayed-output | eventmerge | 1 | verification | 2 | 9153 | 1985 | 50 | 7168 | 2 / 2 | 15238 / 4018 | 0 | 3511 | true |
| pk-current | eventmerge | 1 | implementation | 5 | 14753 | 8609 | 956 | 6144 | 1 / 0 | 7618 / 7618 | 0 | 27112 | — |
| pk-current | eventmerge | 1 | verification | 2 | 13983 | 4767 | 78 | 9216 | 2 / 0 | 15238 / 15238 | 0 | 4202 | true |
| pk-compact-replayed-output | eventmerge | 2 | implementation | 5 | 10701 | 6605 | 797 | 4096 | 1 / 1 | 7618 / 2009 | 0 | 21881 | — |
| pk-compact-replayed-output | eventmerge | 2 | verification | 2 | 8071 | 1927 | 50 | 6144 | 2 / 2 | 15238 / 4018 | 0 | 3270 | true |
| pk-current | eventmerge | 2 | implementation | 4 | 8802 | 7266 | 739 | 1536 | 1 / 0 | 7620 / 7620 | 0 | 18246 | — |
| pk-current | eventmerge | 2 | verification | 2 | 12669 | 3453 | 50 | 9216 | 2 / 0 | 15240 / 15240 | 0 | 5762 | true |
| pk-compact-replayed-output | routematch | 1 | implementation | 4 | 7704 | 6168 | 901 | 1536 | 1 / 1 | 5680 / 2009 | 0 | 21485 | — |
| pk-compact-replayed-output | routematch | 1 | verification | 2 | 7644 | 2524 | 50 | 5120 | 2 / 2 | 11362 / 4018 | 0 | 3808 | true |
| pk-current | routematch | 1 | implementation | 4 | 8945 | 8945 | 782 | 0 | 1 / 0 | 5680 / 5680 | 0 | 21919 | — |
| pk-current | routematch | 1 | verification | 2 | 11381 | 4213 | 49 | 7168 | 2 / 0 | 11362 / 11362 | 0 | 5112 | true |
| pk-compact-replayed-output | routematch | 2 | implementation | 4 | 7616 | 6080 | 830 | 1536 | 1 / 1 | 5680 / 2009 | 0 | 27395 | — |
| pk-compact-replayed-output | routematch | 2 | verification | 2 | 7638 | 2518 | 50 | 5120 | 2 / 2 | 11362 / 4018 | 0 | 3611 | true |
| pk-current | routematch | 2 | implementation | 3 | 6443 | 6443 | 672 | 0 | 1 / 0 | 5680 / 5680 | 0 | 28350 | — |
| pk-current | routematch | 2 | verification | 2 | 10227 | 3059 | 50 | 7168 | 2 / 0 | 11362 / 11362 | 0 | 4840 | true |


## Aggregate interpretation

All four matched task/repetition pairs had lower provider-reported input tokens
under compacted replay. Across eight model phases per arm, compacted/current
input was 72,419/87,203 tokens, uncached input 36,067/46,755, output
3,543/3,376, and cached input 36,352/40,448. Summed phase wall time was
109,683/115,543 ms and response count was 27/24. One pair was slower under
treatment; the observed total wall-time difference was 5,860 ms (5.1%).
The lower cached-token total accompanies lower total input; it is not a better
cache-hit-rate result. These small, stochastic pilot observations do not
establish a general effect. All four pristine verification holdouts passed in
each arm.

The exported JSONL retains tool and usage evidence but strips run-local
correlation IDs. See `README.md` for the sanitization note and exact source
provenance.
