# Benchmark findings

These are small pilot observations from Luna on macOS ARM64 (medium for earlier cohorts; low for the latest job-queue cohort). They are not product claims, statistically powered comparisons, or evidence of general savings. Full methodology and raw records remain beside each experiment.

- The single-repetition pk vs. Unreal Agent v0.1.1 pilot passed all six pristine correctness holdouts. pk generally used more input/output tokens and took longer on the observed phases. The harnesses have different prompts and tool presentation, so this does not isolate implementation efficiency: [`pilot results`](../benchmarks/results/pilot-20260923T033523Z/summary.md).
- In a two-repetition pk-only replay-compaction ablation, all four verification holdouts passed in both arms. Compact replay had 72,419 total input tokens vs. 87,203 for current replay, but the sample covered only two tasks; total wall time was 109,683 vs. 115,543 ms, with one task/repetition pair slower under treatment. The lower cached-token sum is not evidence of a better cache-hit rate. Treat the result as a hypothesis for larger matched trials: [`replay-compaction results`](../benchmarks/results/replay-compaction-rep2-20260923/summary.md).
- A two-repetition tool-description ablation covered two tasks and passed its pristine verification holdouts. It reduced selected description text from 573 to 452 UTF-8 bytes, but per-phase token counts varied and no general token reduction is established: [`tool-schema results`](../benchmarks/results/tool-schema-rep2-20260923/summary.md).
- The one-repetition Bash output-cap ablation did not exercise the treatment: no model call omitted `max_output_length`, so both arms applied zero defaults. It is inconclusive: [`output-cap results`](../benchmarks/results/output-cap-rep1-20260923/summary.md).

- The broader two-repetition replay pilot adds a four-file webhook implementation and a small clamp control. All four holdout checks passed in each arm. Treatment compacted 11 eligible webhook results and reported 88,340 input tokens versus 119,505 for control (uncached: 45,332 versus 52,433). Output increased to 5,390 from 4,759, and aggregate wall time increased to 144,543 from 141,739 ms. Clamp had no eligible output to compact. Different tool trajectories and only two repetitions limit the conclusion; default production policy remains unchanged: [`webhook replay results`](../benchmarks/results/replay-webhook-rep2-20260923/README.md).
- The two-repetition Luna/low job-queue recovery pilot exercised compact replay on all 12 eligible results, reducing stored result text by 45,284 bytes. All four verification holdouts passed. However, compact mode used 74,571 provider input tokens versus 55,741 for full context; uncached input was 25,931 versus 25,533, output was 1,697 versus 1,753, and responses increased from 12 to 19. Its provider-reported cached-input share was higher (65.2% versus 54.2%), but that did not establish lower total cost or faster execution: compact mode made more calls and used substantially more total input. Trace review found repeated source reads after compaction in both compact implementation runs; this suggests context loss but cannot rule out sampling variation. A 16 KiB cutoff would not exercise compaction on this fixture, so it is not a useful next pilot. Keep the product default at `full`; use a future fixture with real large build/test logs and meaningful error lines to evaluate whether selective compaction preserves useful source context: [`job-queue pilot analysis`](../benchmarks/results/jobqueue-replay-e697977-20260923/analysis.md).
- A two-repetition Luna effort pilot compared low with medium on webhook and jobqueue, with new-session implementation followed by same-session verification. All eight holdouts passed. Low used 139,214 input tokens (86,528 cached), 5,780 output tokens, and 27 responses; medium used 144,589 input (96,256 cached), 6,933 output, and 27 responses. Combined input-plus-output was 4.3% lower at low, but uncached input was 9.0% higher and the task effects reversed: webhook used 66,644 combined tokens at low versus 89,584 at medium, while jobqueue used 78,350 versus 61,938. The small, stochastic result does not support changing the production medium default: [`Luna effort pilot`](../benchmarks/results/luna-effort-4f0c7f0-20260923/README.md).

- A newer matched Luna/low comparison ran clamp and intervals twice on current pk and pinned Unreal v0.1.1. All 16 phases completed and all eight pristine holdouts passed. pk used 43,889 input tokens (28,017 uncached, 15,872 cached), 1,752 output tokens, and 22 responses. Unreal used 20,011 input tokens (all uncached), 1,333 output tokens, and 22 responses. pk therefore used more total and uncached input on this small cohort; caching did not reverse that result. Fixed pk-first ordering and overlapping local release checks confound wall-time comparisons. Exact source files and sanitized outputs are preserved: [`matched two-repetition results`](../benchmarks/results/pk-unreal-clamp-intervals-low-2rep-20260923/summary.md).

Next useful evidence is more matched repetitions, with holdouts, success, provider-reported input/output/cache counts, and wall time retained per task. Do not compare cached tokens alone as cost or savings, and do not infer cache hits from session labels.

## Aggressive replay compaction: measured regression

The two-repetition Luna/low [webhook and jobqueue cohort](../benchmarks/results/replay-aggressive-webhook-jobqueue-2rep-20260923/summary.md) tested a 1,024-byte threshold and 256-rune head/tail excerpts. All four pristine holdouts per arm passed, but aggressive compaction reported 544,819 input tokens (97,331 uncached), 10,448 output, and 90 responses versus 127,054 input (55,374 uncached), 4,704 output, and 25 responses for full output. Smaller stored tool output did not yield lower usage. Cached tokens are part of input, not independent savings.

This is a descriptive result for these fixtures. Shared-machine UI testing overlapped the run, so no latency comparison is claimed. Production full-context defaults remain unchanged. The complete source snapshot and sanitized run records accompany the result.

## Delegation accounting boundary

New headless CLI JSONL runs expose child start and usage metadata separately
from parent events. pkbench keeps its historical parent columns and adds child
and combined fields/tables. Combined token totals require complete per-response
usage coverage; explicit zero counts are valid, missing or malformed counts are
unavailable. Child identities namespace response IDs so different children do
not collide. Sanitized traces retain these accounting events for reparsing.

Earlier reports with only parent usage must not be used as whole-delegation
cost evidence. The recorded clamp/intervals comparison used no child launches;
new delegation experiments must include child accounting. The benchmark-only
flat subagent dispatcher saves 807 serialized schema bytes in local tests, but
has not been selected by a live benchmark policy or shown lower token cost.
