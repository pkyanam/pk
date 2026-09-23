# First live component capture

Source: `8f80856`; Luna low, one repetition of clamp and intervals, pk only. Both pristine holdout checks passed. All four phase traces have complete metric/usage correlation (11 responses). This is diagnostic, not an engine comparison or paired optimization result.

Provider totals: 29,717 input tokens, including 13,824 cached; 15,893 uncached and 774 output. Each request carried nine tool schemas totaling 4,004 JSON-value bytes. Byte counts are not tokens; cached input is included in total input.

The largest observed increases followed unsuccessful `git diff` calls in fixture directories without Git metadata. Their error previews show Git's full usage help. Clamp tool-result context rose from 616 to 8,901 bytes after its second tool result; intervals rose from 1,256 to 10,315 bytes after its third. These totals include the complete serialized results, not only help text, so they do not precisely attribute every byte to the error. The large results persisted into resumed verification.

Next candidate: provide accurate workspace Git status or concise guidance to check repository availability before diff commands. Test this against the current behavior with the same fixtures, and also exercise real repositories; do not silently discard arbitrary command errors or change fixtures merely to improve scores. Aggressive generic compaction already regressed and remains disabled by default.

Reproduce the summary:

```sh
python3 scripts/context-cost-attribution.py benchmarks/results/context-capture-luna-low-20260923 --engine pk
```

No Unreal run was performed in this cohort. The generated summary names the pinned upstream version as metadata, not evidence of a concurrent baseline. Internal provider retries remain outside adapter-call accounting.
