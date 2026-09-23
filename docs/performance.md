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
