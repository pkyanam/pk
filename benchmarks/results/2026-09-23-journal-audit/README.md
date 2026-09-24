# Journal audit and local performance pass

Baseline: `f740a3b` (v0.1.18 source plus release-workflow fix). Same synthetic fixtures, Apple M3, macOS arm64, Go 1.27. Five runs per workload; medians below. No model/network calls. Raw output is alongside this report. Timings were collected on a working laptop and are noisy, not statistically established service-level improvements. Allocated bytes are cumulative per operation, not peak heap/RSS.

| Workload | Time before → after | Allocated bytes before → after | Allocs before → after | Time reduction |
| --- | ---: | ---: | ---: | ---: |
| Compaction cut, 500 entries | 1.683 → 0.022 ms | 17,898,080 → 155,833 | 2,151 → 56 | 98.7% |
| Context accounting, mixed ~1 MiB | 0.750 → 0.729 ms | 1,008,290 → 12,094 | 492 → 251 | 2.7% |
| Session list, cold metadata | 1.893 → 1.519 ms | 383,092 → 171,465 | 3,185 → 1,009 | 19.8% |
| Session list, warm metadata | 1.869 → 1.347 ms | 312,753 → 120,993 | 3,174 → 1,004 | 28.0% |
| Session load, cold metadata | 0.853 → 1.006 ms | 1,408,799 → 1,403,369 | 317 → 256 | -17.9% |

Cold session loading regressed 17.9% in the final run; no speedup is claimed there. Its identity checks were retained for correctness. Context-accounting timing improved only 2.7%, within likely machine noise; the allocation reduction is the stronger result. UI append micro-optimizations are reported separately and are not evidence of faster provider responses.

## Mechanisms

- Compaction cut selection: lower-bound search over monotone safe suffix estimates; O(n log n) rather than O(n²), with reference-algorithm parity tests. It preserves the existing nonempty-tail policy.
- Request accounting: JSON encoder writes to a counting sink rather than allocating a serialized copy of every value. Exact byte parity is tested; still O(input bytes), not O(1).
- Session listing: resolve the shared root once and pin reads/cache identities to it. Per-file invalidation remains in place. Listing still scales with session count.

## Reproduce

```sh
go test ./internal/runner -run '^$' -bench '^BenchmarkHistoryCutLongTask$' -benchmem -count=5
go test ./internal/benchcontext -run '^$' -bench '^BenchmarkMeasureRequest1MiBMixed$' -benchmem -count=5
go test ./internal/sessionmanager -run '^$' -bench 'Benchmark(List.*SessionLogs|GetLargeSessionLogCold)' -benchmem -count=5
```

The context benchmark was added during this pass; use the identical fixture with the baseline implementation when reproducing before measurements. Both saved context runs use the same fixed SetBytes denominator; MB/s is a nominal fixture rate. No matched model-token savings or end-to-end response-speed claim is made.

## Journal audit

Fixed torn-tail recovery; all-target conflict/object preflight before restore; bounded aggregate preimage memory; explicit GC fail-closed on incomplete scans; accurate restored-path reporting; active-session leases; durable model-facing restore notes; initial tool catalog visibility; cursor/diff semantics and strict argument parsing; concise model usage cues. UI previews the original mutation before reversing it.

Remaining limitations: only WriteFile/EditFile observed; no automatic Bash preimages; no enforced disk cap/raw-log compaction; no cross-process object-store/GC coordination; late OS failures may partially apply; model usage is guidance rather than enforced or behavior-benchmarked.

## Rejected UI performance experiment

At 120×40 with 300 transcript entries, a one-run append-path experiment measured
37.22 → 37.04 ms median over 12 appends, with heap delta 11,748,947 → 11,753,175
bytes and unchanged render-tree size (914 nodes). Initial ingestion was
46.42 → 46.58 ms. This is not a convincing rendering or memory improvement;
the append optimization was not retained. Raw experimental output is included
for transparency. The journal diff-preview UI change is a usability fix, not a
rendering-speed claim.
