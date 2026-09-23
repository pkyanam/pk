# pk coding-task pilot

- Started: 2026-09-23T08:57:28Z
- Model: `gpt-6-luna` / `paired:low,medium`
- Repetitions: 2
- Per-phase timeout: 1m30s
- pk source revision: `4f0c7f0`
- Upstream baseline: not used (pk-only policy experiment)
- Runtime: go1.27.0 darwin/arm64
- Experiment: Luna reasoning effort: low vs medium
- Source tree SHA-256: `fccaadde97f4fc0d23ba363f1cf21d416abb67d1d01a9496f0bbe4a8feaaa62d`
- Tracked diff SHA-256: `59eb37d3244a167a82d1e10234985c147d98c8a62264c7d682869ff0626bcdb3`

Both arms use the same Luna model, product CLI, system prompt, full tool-output context policy, prompts, empty skills, and Git-initialized fixtures. The only planned treatment is reasoning effort (low vs medium), applied consistently to each task's new session and resumed verification. Provider-reported input, cached-input, and output tokens are reported separately; cached input is included in total input and is not a separate cost measure. Correctness is checked against pristine holdout tests. This two-repetition pilot is small and stochastic; it does not establish a generally better effort setting or justify changing the product default.

| Effort arm | Task | Rep | Phase | Responses | Input | Uncached input | Output | Cached | Wall ms | Correct |
|---|---|---:|---|---:|---:|---:|---:|---:|---:|:---:|
| pk-effort-low (low) | jobqueue | 1 | implementation | 5 | 20525 | 9773 | 672 | 10752 | 22160 | — |
| pk-effort-low (low) | jobqueue | 1 | verification | 3 | 20298 | 3402 | 164 | 16896 | 8259 | true |
| pk-effort-medium (medium) | jobqueue | 1 | implementation | 4 | 15691 | 9547 | 935 | 6144 | 22741 | — |
| pk-effort-medium (medium) | jobqueue | 1 | verification | 3 | 21514 | 3594 | 276 | 17920 | 11499 | true |
| pk-effort-low (low) | jobqueue | 2 | implementation | 4 | 15318 | 10710 | 680 | 4608 | 17290 | — |
| pk-effort-low (low) | jobqueue | 2 | verification | 3 | 20452 | 3556 | 241 | 16896 | 10043 | true |
| pk-effort-medium (medium) | jobqueue | 2 | implementation | 4 | 12216 | 7096 | 946 | 5120 | 25605 | — |
| pk-effort-medium (medium) | jobqueue | 2 | verification | 2 | 10261 | 1045 | 99 | 9216 | 5278 | true |
| pk-effort-low (low) | webhook | 1 | implementation | 4 | 14950 | 10342 | 1723 | 4608 | 39950 | — |
| pk-effort-low (low) | webhook | 1 | verification | 2 | 13882 | 2618 | 91 | 11264 | 6972 | true |
| pk-effort-medium (medium) | webhook | 1 | implementation | 5 | 24709 | 10885 | 1973 | 13824 | 48008 | — |
| pk-effort-medium (medium) | webhook | 1 | verification | 2 | 16445 | 1085 | 94 | 15360 | 5480 | true |
| pk-effort-low (low) | webhook | 2 | implementation | 4 | 16537 | 10393 | 2119 | 6144 | 44451 | — |
| pk-effort-low (low) | webhook | 2 | verification | 2 | 17252 | 1892 | 90 | 15360 | 5183 | true |
| pk-effort-medium (medium) | webhook | 2 | implementation | 4 | 16686 | 12078 | 2177 | 4608 | 47404 | — |
| pk-effort-medium (medium) | webhook | 2 | verification | 3 | 27067 | 3003 | 433 | 24064 | 16249 | true |
