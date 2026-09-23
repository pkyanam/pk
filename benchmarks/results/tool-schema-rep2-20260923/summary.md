# pk coding-task pilot

- Started: 2026-09-23T04:56:38Z
- Model: `gpt-6-luna` / `medium`
- Repetitions: 2
- Per-phase timeout: 1m30s
- pk source revision: `6813acb`
- Upstream baseline: Unreal Agent not used (paired pk-only policy ablation)
- Runtime: go1.27.0 darwin/arm64
- Experiment: Static tool-schema descriptions: current vs compact prose
- Source tree SHA-256: `b9147fc3b22264ad455d6913497c1d0fbaedd86de5a087680000460d4c0b4763`
- Tracked diff SHA-256: `e61d01f4f46d24ea1de9b0d8177f55fc7bb3370c32eb69c946f4ae488400527f`

Both arms use the product CLI, system prompt, model and effort, task prompts, empty skills, and matching Git-initialized fixtures. The treatment shortens selected static tool descriptions while preserving tool names, translators, parameter types, constraints, and defaults. UTF-8 description-byte metrics are measured from the actual registry definitions; provider-reported usage is the token measure. Input/output/cache counters come from each model response, with uncached input shown only when input and cached counts are available. Correctness is checked against pristine holdout tests, and generated production Go files are archived as `.go.txt` files. This paired experiment measures only these tasks and this description treatment.

| Engine | Task | Rep | Phase | Responses | Input | Uncached input | Output | Cached | Description bytes (before / after) | Fields changed | Wall ms | Correct |
|---|---|---:|---|---:|---:|---:|---:|---:|---|---:|---:|:---:|
| pk-compact-schema | clamp-control | 1 | implementation | 3 | 3647 | 3647 | 505 | 0 | 573 / 452 | 5 | 15231 | — |
| pk-compact-schema | clamp-control | 1 | verification | 2 | 3640 | 568 | 182 | 3072 | 573 / 452 | 5 | 5910 | true |
| pk-current | clamp-control | 1 | implementation | 3 | 3506 | 3506 | 463 | 0 | 573 / 573 | 0 | 12494 | — |
| pk-current | clamp-control | 1 | verification | 2 | 3560 | 2024 | 203 | 1536 | 573 / 573 | 0 | 6899 | true |
| pk-compact-schema | clamp-control | 2 | implementation | 3 | 3678 | 3678 | 436 | 0 | 573 / 452 | 5 | 12271 | — |
| pk-compact-schema | clamp-control | 2 | verification | 2 | 3615 | 543 | 179 | 3072 | 573 / 452 | 5 | 6473 | true |
| pk-current | clamp-control | 2 | implementation | 3 | 3355 | 3355 | 344 | 0 | 573 / 573 | 0 | 10253 | — |
| pk-current | clamp-control | 2 | verification | 3 | 5040 | 3504 | 211 | 1536 | 573 / 573 | 0 | 8513 | true |
| pk-compact-schema | noisyrepo | 1 | implementation | 3 | 4454 | 4454 | 948 | 0 | 573 / 452 | 5 | 21859 | — |
| pk-compact-schema | noisyrepo | 1 | verification | 2 | 5395 | 3859 | 184 | 1536 | 573 / 452 | 5 | 6163 | true |
| pk-current | noisyrepo | 1 | implementation | 3 | 4358 | 4358 | 710 | 0 | 573 / 573 | 0 | 16712 | — |
| pk-current | noisyrepo | 1 | verification | 2 | 5006 | 1934 | 120 | 3072 | 573 / 573 | 0 | 5155 | true |
| pk-compact-schema | noisyrepo | 2 | implementation | 4 | 6875 | 5339 | 956 | 1536 | 573 / 452 | 5 | 25916 | — |
| pk-compact-schema | noisyrepo | 2 | verification | 2 | 5423 | 1327 | 88 | 4096 | 573 / 452 | 5 | 5978 | true |
| pk-current | noisyrepo | 2 | implementation | 3 | 4195 | 4195 | 862 | 0 | 573 / 573 | 0 | 21278 | — |
| pk-current | noisyrepo | 2 | verification | 2 | 4809 | 3273 | 125 | 1536 | 573 / 573 | 0 | 5790 | true |
