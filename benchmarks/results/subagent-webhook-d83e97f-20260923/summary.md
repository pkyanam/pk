# pk coding-task pilot

- Started: 2026-09-23T16:59:46Z
- Model: `gpt-6-luna` / `low`
- Repetitions: 2
- Tasks: `webhook`
- Per-phase timeout: 3m0s
- Whole-run timeout: 15m0s
- pk source revision: `d83e97f`
- Upstream baseline: not used (pk-only policy experiment)
- Runtime: go1.27.0 darwin/arm64
- Experiment: Subagent tool schema: five direct tools vs action dispatcher
- Source tree SHA-256: `d8114bb0c2ac9a12a450ac07f97daa40b04b9eaa3f6f4997f6d40f7f1b119b4a`
- Tracked diff SHA-256: `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`

The response/token columns in the main table are parent-runner only. Child-agent usage and combined totals, when observed and fully covered, are reported separately below.
The arms use the same Luna model, effort, parent prompts, fixture contents, and holdout tests. Only the model-visible subagent tool schema changes: five direct tools versus one action dispatcher. The implementation prompt requests two parallel children on separate fixture files; action coverage passes only when at least two children start and complete with fully available input/output usage. Existing parent token columns are parent-runner only; child and combined provider usage is reported separately below. This opt-in pilot is small and stochastic; report actual provider counters and correctness, not schema bytes as token savings.

| Engine | Task | Rep | Phase | Parent responses | Parent input | Parent output | Parent cached | Child schema bytes (before / after / saved) | Children (started / completed) | Children with usage | Child input | Child output | Action coverage | Wall ms | Correct |
|---|---|---:|---|---:|---:|---:|---:|---|---:|---:|---:|---:|:---:|---:|:---:|
| pk-subagent-compact | webhook | 1 | delegation | 9 | 32727 | 1395 | 17408 | 2124 / 1317 / 807 | 2 / 2 | 2 | 17145 | 1421 | true | 57494 | true |
| pk-subagent-current | webhook | 1 | delegation | 6 | 25241 | 1442 | 14336 | 2124 / 2124 / 0 | 2 / 2 | 2 | 13630 | 1471 | true | 58414 | true |
| pk-subagent-compact | webhook | 2 | delegation | 11 | 47706 | 1157 | 29696 | 2124 / 1317 / 807 | 2 / 2 | 2 | 11795 | 1096 | true | 70167 | true |
| pk-subagent-current | webhook | 2 | delegation | 9 | 39076 | 1244 | 22528 | 2124 / 2124 / 0 | 2 / 2 | 2 | 22270 | 1517 | true | 87031 | true |

## Subagent usage (separate accounting)

Parent totals are not changed by this table. Combined values are available only when parent and child response usage is fully correlated; unavailable values are not treated as zero.

| Engine | Task | Rep | Phase | Child responses | Child input | Child output | Child cached | Child cache write | Combined responses | Combined input | Combined uncached input | Combined output | Combined cached | Combined cache write |
|---|---|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| pk-subagent-compact | webhook | 1 | delegation | 8 | 17145 | 1421 | 3072 | 0 | 17 | 49872 | 29392 | 2816 | 20480 | 0 |
| pk-subagent-current | webhook | 1 | delegation | 7 | 13630 | 1471 | 4608 | 0 | 13 | 38871 | 19927 | 2913 | 18944 | 0 |
| pk-subagent-compact | webhook | 2 | delegation | 7 | 11795 | 1096 | 1536 | 0 | 18 | 59501 | 28269 | 2253 | 31232 | 0 |
| pk-subagent-current | webhook | 2 | delegation | 9 | 22270 | 1517 | 9216 | 0 | 18 | 61346 | 29602 | 2761 | 31744 | 0 |
