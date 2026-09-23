# pk coding-task pilot

- Started: 2026-09-23T14:05:38Z
- Model: `gpt-6-luna` / `low`
- Repetitions: 2
- Tasks: `clamp, intervals`
- Per-phase timeout: 2m0s
- Whole-run timeout: 30m0s
- pk source revision: `79c468b`
- Upstream baseline: not used (pk-only policy experiment)
- Runtime: go1.27.0 darwin/arm64
- Experiment: Subagent tool schema: five direct tools vs action dispatcher
- Source tree SHA-256: `b0d1c6bd25f3a4875c5d17bbac92394b138261bb813b3b135e182ad9efe60b5c`
- Tracked diff SHA-256: `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`

The response/token columns in the main table are parent-runner only. Child-agent usage and combined totals, when observed and fully covered, are reported separately below.
The arms use the same Luna model, effort, parent prompts, fixture contents, and holdout tests. Only the model-visible subagent tool schema changes: five direct tools versus one action dispatcher. The implementation prompt requests two parallel children on separate fixture files; action coverage passes only when at least two children start and complete with fully available input/output usage. Existing parent token columns are parent-runner only; child and combined provider usage is reported separately below. This opt-in pilot is small and stochastic; report actual provider counters and correctness, not schema bytes as token savings.

| Engine | Task | Rep | Phase | Parent responses | Parent input | Parent output | Parent cached | Child schema bytes (before / after / saved) | Children (started / completed) | Children with usage | Child input | Child output | Action coverage | Wall ms | Correct |
|---|---|---:|---|---:|---:|---:|---:|---|---:|---:|---:|---:|:---:|---:|:---:|
| pk-subagent-compact | clamp+intervals | 1 | delegation | 7 | 15855 | 483 | 7168 | 2124 / 1317 / 807 | 2 / 2 | 2 | 10833 | 884 | true | 23992 | true |
| pk-subagent-current | clamp+intervals | 1 | delegation | 6 | 15420 | 810 | 8192 | 2124 / 2124 / 0 | 2 / 2 | 2 | 11332 | 810 | true | 36651 | true |
| pk-subagent-compact | clamp+intervals | 2 | delegation | 5 | 10696 | 456 | 3072 | 2124 / 1317 / 807 | 2 / 2 | 2 | 14084 | 963 | true | 33483 | true |
| pk-subagent-current | clamp+intervals | 2 | delegation | 6 | 17107 | 981 | 5632 | 2124 / 2124 / 0 | 2 / 2 | 2 | 11876 | 713 | true | 39766 | true |

## Subagent usage (separate accounting)

Parent totals are not changed by this table. Combined values are available only when parent and child response usage is fully correlated; unavailable values are not treated as zero.

| Engine | Task | Rep | Phase | Child responses | Child input | Child output | Child cached | Child cache write | Combined responses | Combined input | Combined uncached input | Combined output | Combined cached | Combined cache write |
|---|---|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| pk-subagent-compact | clamp+intervals | 1 | delegation | 8 | 10833 | 884 | 0 | 0 | 15 | 26688 | 19520 | 1367 | 7168 | 0 |
| pk-subagent-current | clamp+intervals | 1 | delegation | 8 | 11332 | 810 | 1536 | 0 | 14 | 26752 | 17024 | 1620 | 9728 | 0 |
| pk-subagent-compact | clamp+intervals | 2 | delegation | 10 | 14084 | 963 | 1536 | 0 | 15 | 24780 | 20172 | 1419 | 4608 | 0 |
| pk-subagent-current | clamp+intervals | 2 | delegation | 9 | 11876 | 713 | 0 | 0 | 15 | 28983 | 23351 | 1694 | 5632 | 0 |
