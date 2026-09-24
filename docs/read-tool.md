# Read

`Read` retrieves local UTF-8 text without starting a shell. Prefer it for file
contents; use Bash for searches, directory listings, and commands, and
ViewImage for images.

```json
{"path":"internal/runner/runner.go","offset":120,"limit":80}
```

- `path`: workspace-relative or absolute local file path. Reading uses the
  user's ordinary filesystem permissions; it is not a workspace sandbox.
- `offset`: first line, starting at 1; defaults to 1.
- `limit`: positive line count, up to 2,000; defaults to 200.
- Output is numbered and capped at 16 KiB. Follow the returned continuation
  offset to read more. Do not infer omitted content from a partial page.
- Each call scans at most 64 MiB, including skipped lines; distant ranges beyond
  that bound receive shell guidance. Unsafe terminal controls are rejected.
- Binary files and unsupported encodings produce errors rather than misleading
  decoded text. Directories and special files are not text reads.
- Lines that cannot fit receive explicit guidance rather than silently losing
  the rest of the line. Reading is cancellable and does not journal mutations.

The implementation streams a bounded window rather than loading, splitting,
counting, or hashing the whole file just to show its beginning. It has no
content cache that could silently serve stale edits. Deep line offsets still
require scanning preceding bytes; this is not an O(1) random-access claim.
Separate calls are fresh reads, not a snapshot transaction across pages.

The model sees a short description and a system-prompt cue to prefer Read over
cat/sed. New sessions include it in their initial tool catalog. Existing saved
sessions retain their frozen tool schemas; use `/new` after updating to obtain
the new tool.

## Design references

Reviewed September 24, 2026:

- [OpenCode Read](https://github.com/anomalyco/opencode/blob/dev/packages/opencode/src/tool/read.ts): line ranges, numbered output, byte bounds, binary detection, and explicit continuation.
- [Pi Read](https://github.com/badlogic/pi-mono/blob/main/packages/coding-agent/src/core/tools/read.ts): compact path/offset/limit arguments and actionable oversized-line guidance.

pk uses smaller default output bounds and a native Go streaming implementation.
This comparison does not establish end-to-end speed or token superiority over
those harnesses.

## Local microbenchmark

Apple M3, macOS arm64, 10 iterations, 10,000-line fixture; offset 5,000,
200 selected lines. Run `go test ./internal/filetools -run '^$' -bench
'Benchmark(ReadTextPage|SedSamePage)$' -benchtime=10x -benchmem`.

| Retrieval | Time/op | Go allocations bytes/op |
|---|---:|---:|
| Native Read, numbered and bounded | 0.781 ms | 77,512 |
| `sed -n 5000,5199p` subprocess | 3.086 ms | 80,454 |

This small local sample includes subprocess startup for sed, which scans to
EOF; Read stops after its requested page. Go allocation accounting excludes
child-process memory, so this is **not** a total-memory comparison. Neither
model latency nor model-token savings was measured.
