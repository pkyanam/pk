# Subagent schema webhook ablation

This is a two-repetition, pk-only policy experiment on macOS ARM64 using Luna
at low effort. It compares five direct subagent tools with a single
`Subagent` action dispatcher. Both arms use the same webhook task and pristine
holdout checks; the tool schema is the only intended treatment. The archived
`summary.md`, JSONL records, source manifest, and source snapshot preserve the
run evidence.

## Validity gates

All four arms recorded Luna/low for the parent and children, started and
completed two children, had child usage for both, reported complete combined
usage, marked action coverage true, and passed the independent holdout. The
comparison is scoreable under the experiment's correctness and instrumentation
rules.

## Paired results

| Pair | Direct tools: combined input + output | Dispatcher: combined input + output | Change | Direct → dispatcher wall time |
|---|---:|---:|---:|---:|
| 1 | 41,784 | 52,688 | +26.1% | 58,414 → 57,494 ms |
| 2 | 64,107 | 61,754 | −3.7% | 87,031 → 70,167 ms |
| Total | 105,891 | 114,442 | +8.1% | 145,445 → 127,661 ms |

| Measure | Direct tools | Dispatcher | Change |
|---|---:|---:|---:|
| Combined uncached input | 49,529 | 57,661 | +16.4% |
| Combined responses | 31 | 35 | +4 |
| Combined output | 5,674 | 5,069 | −10.7% |
| Subagent schema JSON bytes | 2,124 | 1,317 | −807 bytes (−38.0%) |

The dispatcher fails the preregistered efficiency gates: aggregate
input-plus-output needed to be at least 5% lower, uncached input could not rise
more than 5%, and response count could not increase beyond the one-response
tolerance. It instead used 8.1% more combined input/output, 16.4% more
uncached input, and four more responses. Wall time was lower in both dispatcher
pairs, but two pairs are insufficient evidence for a general latency claim.
Schema JSON byte reduction does not imply token reduction. Keep the current
five-tool production default.

The paired token and wall-time changes vary substantially: the dispatcher
regressed tokens in pair 1 and improved them modestly in pair 2. This suggests
stochastic task behavior; it does not support a product efficiency claim.
