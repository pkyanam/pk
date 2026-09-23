# Job queue recovery context-policy pilot

This is a paired, pk-only pilot on one task with two repetitions, two arms, and new-session implementation plus resumed-session verification. It is descriptive evidence, not a general efficiency or quality result.

## Reproduction and provenance

The run used a clean clone at commit `e6979775e2c3322fdafee9938677685952e88fa7`, model `gpt-6-luna`, effort `low`, an empty skills directory, two repetitions, a 90-second per-phase timeout, and a 20-minute whole-run cap. It started at `2026-09-23T07:50:17.095805Z`, finished at `2026-09-23T07:52:49.141259Z` (152.045 seconds). The exact command was:

```sh
go run ./cmd/pkbench -repo /tmp/pkbench-source-e697977.Kc4AcO/repo \
  -replay-compaction-ablation -replay-tasks jobqueue \
  -model gpt-6-luna -effort low -repetitions 2 -timeout 90s \
  -out /tmp/pkbench-jobqueue-replay-e697977-20260923
```

The source-tree SHA-256 was `d6e2b97028a351c5b500e07aae8a6618ec9db3baedafaad93f205f57dbdab8dc`; the tracked diff hash was the empty-diff SHA-256 `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`. `source-manifest.json` and `source-snapshot/files/` preserve and hash-check the run source. Per-phase JSONL and generated `.go.txt` artifacts are retained. A scan of published run records and generated artifacts found no credential-like values.

## Results

All four resumed verification holdouts passed (two per arm); all eight model phases exited successfully. The policy was exercised: compact mode saw 12 eligible Bash results and compacted all 12, with zero missing-capture fallbacks. Its phase records report 69,416 original result bytes and 24,132 stored bytes (45,284 fewer). Full mode saw four eligible results and compacted none, as expected.

| Measure, across 4 phases per arm | Full | Compact | Compact minus full |
|---|---:|---:|---:|
| Provider input tokens | 55,741 | 74,571 | +18,830 (+33.8%) |
| Provider uncached input tokens | 25,533 | 25,931 | +398 (+1.6%) |
| Provider cached input tokens | 30,208 | 48,640 | +18,432 (+61.0%) |
| Provider output tokens | 1,753 | 1,697 | -56 (-3.2%) |
| Model responses | 12 | 19 | +7 |
| Summed phase wall time | 82,347 ms | 63,749 ms | -18,598 ms (-22.6%) |

The compact arm reduced the size of stored Bash result text, but total provider input was higher and response count increased. Uncached input was nearly unchanged. Provider-reported cached input was 30,208 / 55,741 (54.2%) for full and 48,640 / 74,571 (65.2%) for compact. This describes the reported token mix, but does not establish lower total cost or faster execution because compact mode used substantially more total input and made more calls. One matched repetition was faster with compact mode by 19,251 ms, while the other was slower by 653 ms. The lower summed wall time in this two-repetition sample is therefore variable and not a reliable speedup claim. Output tokens were slightly lower. There is no price estimate.

This pilot found no overall token-efficiency gain. It did preserve holdout correctness on this task, but four passing checks on one fixture cannot establish that compact mode preserves quality generally. Keep `full` as the default; use the opt-in policy only with awareness that its effect depends on the task's tool-result and model-call trajectory.
