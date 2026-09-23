# Replay-compaction webhook pilot record

This is the sanitized record of the single approved 16-phase, two-repetition pk-only policy ablation. It used pk revision `226b07c` from a clean checkout, model `gpt-6-luna` at medium effort, with the same production CLI and prompts in both arms. Each task ran as a new implementation session followed by verification in the resumed session. The `clamp` task is the small control; `webhook` is the multi-file task designed to produce verbose test output. Per-phase timeout was 90 seconds.

`summary.md` has provider-reported usage, per-phase wall time, manipulation checks, and holdout results. `summary.json` and the per-phase JSONL preserve measured records; session, response, call, and operation IDs were removed. Generated model source is preserved as `.go.txt` under `artifacts/`. The pristine source snapshot is under `source-snapshot/`; verify it against `source-manifest.json`. This record contains no credentials, binaries, or failed-run logs.

Both arms passed all four holdout verification phases (2 tasks × 2 repetitions). For webhook, treatment compacted all 11 eligible large Bash results (61,492 original bytes to 22,121 stored bytes); current compacted none of its 10 eligible results. Neither arm had missing capture fallbacks. `clamp` produced no eligible result in either arm, so it is a control only.

Across all eight phases per arm, treatment reported 88,340 input tokens (45,332 uncached; 43,008 cached), 5,390 output tokens, and 144,543 ms; current reported 119,505 input (52,433 uncached; 67,072 cached), 4,759 output, and 141,739 ms. These totals are descriptive: the treatment elicited different tool/model trajectories, and two repetitions cannot establish a general efficiency or quality effect. In particular, lower total input tokens coincided with fewer cached input tokens, more output tokens, slightly higher wall time, and no failed holdouts. No pricing or superiority claim follows.

The source commit and source-tree hash are in `source-manifest.json`. This is a pk-only ablation, not a comparison with Unreal or another agent.
