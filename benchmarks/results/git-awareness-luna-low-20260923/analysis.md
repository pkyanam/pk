# Workspace Git awareness: preliminary comparison

Both clamp and intervals passed pristine holdout tests before and after the change. These are sequential, single-repetition Luna-low runs, not randomized or repeated evidence of a general gain. Baseline source `8f80856`; candidate `b7a93a2`. Both use the same two fixtures and implementation/resume prompts, with complete component capture coverage.

| Recorded measure | Before | Git-awareness candidate |
|---|---:|---:|
| Model responses | 11 | 12 |
| Input tokens | 29,717 | 21,736 |
| Cached input (included above) | 13,824 | 7,680 |
| Uncached input | 15,893 | 14,056 |
| Output tokens | 774 | 755 |
| Tool-result bytes summed across requests | 60,416 | 12,981 |
| Holdout tasks passed | 2/2 | 2/2 |

The baseline issued `git diff` in both non-Git fixture folders and received long usage errors. The candidate issued no failed Git commands; every captured completed shell operation exited zero. Tool declarations remained nine schemas / 4,004 JSON-value bytes per request. The short workspace advisory increased initial request input, while avoiding errors reduced later context in this sample. Byte sums count repeated history each time it was sent; they are not unique stored bytes or tokens.

Tradeoffs: the candidate used an extra response and took longer overall. Cache hits differed. The probes cost about 12.6–12.7 ms per fresh snapshot in two five-iteration local M3 measurements; pathological lookup is bounded to 500 ms plus process drain. Results are consistent with reduced error-context overhead but do not isolate all stochastic model differences or prove lower dollar cost. No comparison with another harness is made here.

See [baseline captures](../context-capture-luna-low-20260923/analysis.md). Existing sessions keep their frozen prompt; only fresh sessions gain the advisory. Missing Git, malformed metadata and timeouts produce no unsupported claim about repository state.
