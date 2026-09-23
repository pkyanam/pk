# Startup performance baseline

This baseline covers installed process startup, the RPC handshake, RPC tool
catalog preview, and first OpenTUI frame. It does not measure model latency or
tool execution. No model requests or external services were used.

## Installed macOS baseline

Measured on macOS arm64 through the installed launcher at
`/Users/preetham/.local/bin/pk`, release
`20260923T072128.857770000Z-db4019efc1d6-cd891689`. The run used 29 warm
subprocess samples per command after one first sample, plus 15 pseudo-terminal
launches for the UI. `HOME`, `PK_HOME`, and the workspace were isolated in
temporary directories. The TUI ran at 80×24 and was stopped with Ctrl-C after
its first frame displayed “Ready when you are.” Percentiles use nearest rank.

| Installed path | First sample | Warm median | Warm p95 |
| --- | ---: | ---: | ---: |
| `pk --help` | 10.80 ms | 10.31 ms | 11.69 ms |
| `pk version` | 9.63 ms | 9.93 ms | 10.50 ms |
| RPC `start` → `ready` → `shutdown` | 10.32 ms | 9.84 ms | 10.47 ms |
| RPC `start` → tool catalog preview → `shutdown` | 10.29 ms | 10.05 ms | 10.89 ms |
| TUI first ready frame | 227.05 ms | 214.44 ms | 220.22 ms |

The UI's median startup is roughly 200 ms, substantially more than the
launcher/RPC path at about 10 ms. The audit found no small, isolated change
that would safely reduce OpenTUI initialization time.

The existing UI stress test that caps a transcript after more than 1,000 rows
passed (`bun test src/app.test.tsx -t 'caps a transcript fed more than 1000
rows while keeping the newest row responsive'`; 136.16 ms in this run).

## Measurement scope

The RPC `ready` event follows workspace/config selection and release status
lookup. The `tools` request returns the saved-session metadata preview; it does
not initialize the live runner registry. Provider clients, extension workers,
MCP servers, and the runner's tool registry are initialized only when a prompt
starts. Measuring that path requires a fake adapter and a complete runner turn
so the audit remains credential-free.

Reproduce the installed measurements with:

```sh
python3 scripts/perf-startup.py /path/to/pk --samples 29 --tui-samples 15
```

The script prints the selected executable, version, OS/architecture, sample
counts, and medians/p95s. It accepts either the stable installed launcher or a
direct binary path.

## Session-list allocation benchmark

This model-independent microbenchmark isolates `sessionmanager.Manager.List`
over 32 local session logs, each containing a 128 KiB assistant response. The
fixture is created before the timer; no model or network calls are made. On an
Apple M3 running macOS arm64 with Go 1.27.0, one five-iteration run produced:

| Revision | Time/op | Allocated/op | Allocs/op |
| --- | ---: | ---: | ---: |
| `e9821c4` (before streaming preview normalization) | 36.29 ms | 61,358,078 B | 5,356 |
| `7436784` (streaming preview normalization) | 32.88 ms | 44,625,003 B | 5,237 |

In this run, allocated bytes fell 27.3% and time/op fell 9.4%. These are
single-run synthetic-fixture measurements, not an estimate of general CLI or
session-list performance. The change stops whitespace normalization once the
96- or 320-rune display preview is determined, rather than materializing the
entire assistant body.

Reproduce at either revision with:

```sh
go test ./internal/sessionmanager -run '^$' -bench '^BenchmarkListLargeSessionLogs$' -benchtime=5x -benchmem
```

The benchmark does not remove the separate full-log decoding cost: session
listing still calls upstream `Inspect` and `Items`, each of which reads and
decodes the complete session log. A combined upstream metadata-and-items API
would be needed to remove that duplicate decode without reimplementing the
session format and its replay validation.

## Assistant-history preview assembly

`projectHistory` now retains only the existing per-entry tail limit while
joining assistant message parts. A three-sample microbenchmark using 32 valid
assistant message parts totaling about 2.7 MiB measured 2,761,670 B/op and
1.65 ms/op for join-then-truncate, versus 16,384 B/op and 0.541 ms/op for
bounded tail assembly. This isolates string assembly only; it is a synthetic
fixture and does not measure end-to-end RPC history loading. In particular,
the upstream session store still decodes the whole session log, and tool-result
redaction still processes full operation output before its display preview is
cut. Short malformed UTF-8 text retains the old behavior; over-limit text uses
the existing UTF-8-safe tail normalization.

Reproduce with:

```sh
go test ./cmd/pk -run '^$' -bench '^BenchmarkAssistantHistoryPreviewLarge$' -benchmem -count=3
```

## Installed startup checkpoint (06:40 EDT)

A small local check of installed `b5aa90b` on Darwin arm64 measured a 509 ms first TUI ready frame and 490–508 ms across two later launches. RPC ready/catalog warm medians were 11.0/10.5 ms (three samples each). No provider request was made. These small samples are not cold-cache or comparative claims; other development work shared this machine. Raw results: [`startup-b5aa90b-20260923/result.json`](../benchmarks/results/startup-b5aa90b-20260923/result.json).

The startup script now changes the PTY child to the supplied temporary workspace before exec. Earlier runs accidentally inherited the invoking directory for the TUI leg, so those TUI timings are not a controlled before/after baseline.

```sh
python3 scripts/perf-startup.py ~/.local/bin/pk --samples 3 --tui-samples 3
```

## Repeated session lists

Revision `c7f0c0d` caches up to 256 session summaries in memory. Log and context-sidecar file identity, size, mode, and modification time must match; reads are checked again before insertion. Active-run and lease status are checked on every listing. Changed or damaged logs are read normally. This avoids repeatedly decoding unchanged transcripts without adding persistent cache files.

On Apple M3 with Go 1.27, the 32-session fixture measured 28.64 ms/op and 44.6 MB/op before this change. A five-iteration root verification after the change measured 9.83 ms/op and 9.16 MB/op including the initial miss, and 1.90 ms/op and 222 KB/op with the cache already populated. These small, shared-machine samples describe local list processing, not provider latency or token savings.

```sh
go test ./internal/sessionmanager -run '^$' -bench 'BenchmarkList.*LargeSessionLogs$' -benchmem -benchtime=5x
```

## Cold single-session lookup

`Manager.Get` now reuses its first validated snapshot instead of calling `Inspect` twice. A cache miss still validates and reads the session through the upstream store; it does not introduce a separate parser. A five-iteration 128 KiB fixture measured 2.305 ms/op and 2,097,492 B/op before, versus 1.531 ms/op and 1,415,161 B/op in root verification afterward. Allocation count increased from 312 to 372 per operation. This isolates metadata lookup, not the entire TUI attachment flow.

```sh
go test ./internal/sessionmanager -run '^$' -bench '^BenchmarkGetLargeSessionLogCold$' -benchmem -benchtime=5x
```

## Installed startup checkpoint (`264b3c7`)

Release `264b3c7` rendered its first 80×24 TUI ready frame in 500 ms; two later launches measured 498–502 ms. RPC ready and tool-catalog preview warm medians were 12.1 and 10.7 ms (three samples each). These are small shared-machine measurements in a temporary workspace, without provider calls, not a comparative speedup. Raw results: [`startup-264b3c7-20260923/result.json`](../benchmarks/results/startup-264b3c7-20260923/result.json).

## Large Markdown transcript diagnostic

The renderer test uses three 16 KiB answers per fresh app, prewarms Markdown
parsers, alternates format order, and destroys each renderer before the next
sample. On the same development Mac, three samples measured initial Markdown
rendering at 151.7/154.4/149.0 ms, versus plain text at 37.0/35.7/35.8 ms.
Unrelated status updates measured 42.0/36.0/36.5 ms and 36.5/35.4/34.9 ms,
respectively. The composer remained visible through resize and wheel scrolling.

This synthetic test includes renderer scheduling/flush overhead. It establishes
initial Markdown work as a cost, not an explanation of the reported freeze or
a general performance ratio. Existing memoization skips unchanged transcript
rows on unrelated updates; a status event here is not a direct clock-tick test.
No transcript content was truncated or rendering behavior changed.

```sh
cd ui && bun test src/app.large-transcript.test.tsx
```
