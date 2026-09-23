# Tool-schema ablation: two-repetition audit

This run compares the current static tool descriptions with the compact-description benchmark treatment. It uses `gpt-6-luna` at `medium`, the same two fixtures and prompts in both arms, a new-session implementation phase followed by resumed-session verification, two repetitions with task and arm order reversed in repetition 2, and the immutable holdout evaluator. It is a pk-only policy ablation, not a Codex or Unreal comparison.

The manipulation check succeeded in every phase: the current arm exposed 573 UTF-8 description bytes with 0 changed fields; the compact arm exposed 452 bytes with 5 changed fields. This measures schema bytes, not model tokens.

| Measure, summed over 8 phases per arm | Current | Compact descriptions | Compact minus current |
|---|---:|---:|---:|
| Provider input tokens | 33,829 | 36,727 | +2,898 (+8.6%) |
| Uncached input tokens | 26,149 | 23,415 | -2,734 (-10.5%) |
| Cached input tokens | 7,680 | 13,312 | +5,632 (+73.3%) |
| Cache-write input tokens | 0 | 0 | 0 |
| Output tokens | 3,038 | 3,478 | +440 (+14.5%) |
| Model responses | 21 | 21 | 0 |
| Summed model-phase wall time | 87.094 s | 99.801 s | +12.707 s (+14.6%) |
| Holdout verification | 4/4 passed | 4/4 passed | all 8 passed |

This small stochastic run does not show a total-input or latency reduction: total input, output, and summed model-phase wall time were higher in the compact arm. Uncached input was lower while cached input was higher, but the per-task/repetition differences varied in direction. The totals do not establish that description compaction caused the differences or improved efficiency. No price estimate is made.

`summary.json` retains all 16 phase records, `summary.md` is the compact table view, the JSONL files retain sanitized per-response and tool events, `artifacts/` retains generated production source as `.go.txt`, and `source-snapshot/` contains the manifest-verified source used for the run. No private failed-command logs were produced.

The run-time summary writer incorrectly called this a single-repetition experiment. This published `summary.md` copy removes that phrase; its repetition count already says 2. The measured JSON records and exact source snapshot are unchanged.
