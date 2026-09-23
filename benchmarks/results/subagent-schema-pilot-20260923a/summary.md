# pk coding-task pilot

- Started: 2026-09-23T13:33:32Z
- Model: `gpt-6-luna` / `low`
- Repetitions: 1
- Tasks: `clamp, intervals`
- Per-phase timeout: 2m0s
- Whole-run timeout: 15m0s
- pk source revision: `53f2478`
- Upstream baseline: not used (pk-only policy experiment)
- Runtime: go1.27.0 darwin/arm64
- Experiment: Subagent tool schema: five direct tools vs action dispatcher
- Source tree SHA-256: `df43ee408ed7d4e6dfe82344ba2e22e463e9b577ed0190eac16b9c64cf209969`
- Tracked diff SHA-256: `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`

The response/token columns in the main table are parent-runner only. Child-agent usage and combined totals, when observed and fully covered, are reported separately below.
The arms use the same Luna model, effort, parent prompts, fixture contents, and holdout tests. Only the model-visible subagent tool schema changes: five direct tools versus one action dispatcher. The implementation prompt requests two parallel children on separate fixture files; action coverage passes only when at least two children start and complete with fully available input/output usage. Existing parent token columns are parent-runner only; child and combined provider usage is reported separately below. This opt-in pilot is small and stochastic; report actual provider counters and correctness, not schema bytes as token savings.

| Engine | Task | Rep | Phase | Parent responses | Parent input | Parent output | Parent cached | Child schema bytes (before / after / saved) | Children (started / completed) | Children with usage | Child input | Child output | Action coverage | Wall ms | Correct |
|---|---|---:|---|---:|---:|---:|---:|---|---:|---:|---:|---:|:---:|---:|:---:|
| pk-subagent-compact | clamp+intervals | 1 | delegation | 5 | 10552 | 449 | 3072 | 2124 / 1317 / 807 | 2 / 2 | 2 | 9687 | 936 | true | 29649 | true |
| pk-subagent-current | clamp+intervals | 1 | delegation | 6 | 16942 | 888 | 9216 | 2124 / 2124 / 0 | 2 / 2 | 2 | 9229 | 685 | true | 38587 | true |

## Subagent usage (separate accounting)

Parent totals are not changed by this table. Combined values are available only when parent and child response usage is fully correlated; unavailable values are not treated as zero.

| Engine | Task | Rep | Phase | Child responses | Child input | Child output | Child cached | Child cache write | Combined responses | Combined input | Combined output | Combined cached | Combined cache write |
|---|---|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| pk-subagent-compact | clamp+intervals | 1 | delegation | 7 | 9687 | 936 | 0 | 0 | 12 | 20239 | 1385 | 3072 | 0 |
| pk-subagent-current | clamp+intervals | 1 | delegation | 7 | 9229 | 685 | 0 | 0 | 13 | 26171 | 1573 | 9216 | 0 |
