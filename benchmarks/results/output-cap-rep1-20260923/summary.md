# pk coding-task pilot

- Started: 2026-09-23T04:12:36Z
- Model: `gpt-6-luna` / `medium`
- Repetitions: 1
- Per-phase timeout: 1m30s
- pk source revision: `9a16944`
- Upstream baseline: Unreal Agent not used (paired pk-only policy ablation)
- Runtime: go1.27.0 darwin/arm64
- Experiment: Bash omitted-output-limit default: current vs 4096 characters
- Source tree SHA-256: `efba38dfdb5a1780f126926d92afae963a5073d966fcf9b22e6f21519fc7b09f`
- Tracked diff SHA-256: `dec956dd9fb183ac566994538602181ef7c320234a4a7d17cd33898734a9fb65`

Both arms use the product CLI, system prompt, model, task prompt, empty skills, and same fixture with a clean initial Git commit. The treatment changes the advertised Bash default to 4096 characters and injects that value only when a valid JSON Bash call omits `max_output_length`; explicit values pass through unchanged. Exact omission/default counts are recorded by a build-tagged benchmark hook, not inferred from truncated previews. Input/output/cache counters are provider-reported; `uncached_input_tokens` is input minus provider-reported cached tokens when both are available. Tool output byte totals and truncated-operation counts come from durable operation state. Commands are retained only for failed Bash operations in a separate private state directory; event excerpts are bounded and sanitized. Correctness is checked against pristine holdout tests, and generated production Go files are archived under `artifacts/` as `.go.txt` files. This small, stochastic paired test measures only these fixtures and this policy.

| Engine | Task | Rep | Phase | Responses | Input | Uncached input | Output | Cached | Bash caps (explicit / omitted / defaulted) | Returned bytes (out / err) | Raw bytes (out / err) | Truncated ops (out / err) | Wall ms | Correct |
|---|---|---:|---|---:|---:|---:|---:|---:|---|---|---|---|---:|:---:|
| pk-current | clamp-control | 1 | implementation | 3 | 3663 | 3663 | 504 | 0 | 2 / 0 / 0 | 975 / 0 | 975 / 0 | 0 / 0 | 13120 | — |
| pk-current | clamp-control | 1 | verification | 2 | 3699 | 627 | 218 | 3072 | 1 / 0 / 0 | 1021 / 0 | 1021 / 0 | 0 / 0 | 7360 | true |
| pk-output-cap-4k | clamp-control | 1 | implementation | 3 | 3590 | 3590 | 406 | 0 | 3 / 0 / 0 | 1152 / 0 | 1152 / 0 | 0 / 0 | 12710 | — |
| pk-output-cap-4k | clamp-control | 1 | verification | 2 | 3694 | 2158 | 214 | 1536 | 3 / 0 / 0 | 1417 / 0 | 1417 / 0 | 0 / 0 | 6588 | true |
| pk-current | noisyrepo | 1 | implementation | 3 | 4271 | 4271 | 780 | 0 | 3 / 0 / 0 | 2617 / 0 | 2617 / 0 | 0 / 0 | 24998 | — |
| pk-current | noisyrepo | 1 | verification | 2 | 5083 | 2011 | 212 | 3072 | 2 / 0 / 0 | 2655 / 0 | 2655 / 0 | 0 / 0 | 7567 | true |
| pk-output-cap-4k | noisyrepo | 1 | implementation | 3 | 4271 | 4271 | 651 | 0 | 2 / 0 / 0 | 2143 / 0 | 2143 / 0 | 0 / 0 | 15490 | — |
| pk-output-cap-4k | noisyrepo | 1 | verification | 2 | 4980 | 1908 | 178 | 3072 | 1 / 0 / 0 | 3296 / 0 | 3296 / 0 | 0 / 0 | 6137 | true |
