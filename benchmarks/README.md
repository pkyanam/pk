# Coding-task benchmark pilot

`cmd/pkbench` compares the current `pk` CLI with the unchanged Unreal Agent
v0.1.1 `unreal-agent-runner` built from the pinned Go module. Both use
`gpt-6-luna` at `medium` effort and share the same three tiny Go tasks, task
prompts, workspace contents, empty skills directory, and two-phase workflow.
The upstream baseline uses the same provider credentials and model, but keeps
its own system prompt and tool contract; this is a product comparison, not a
single-variable comparison of every prompt or UI difference.

The implementation phase starts a new session and is instructed not to run
tests. The verification phase resumes that session and runs `go test ./...`,
repairing failures. Those labels describe session boundaries only; either
phase can contain cached and uncached requests. Provider-reported input,
output, cached-input, and cache-write counts remain separate. Missing provider
fields are marked unknown, and no dollar estimate is made. Response counts are
persisted model responses; hidden transport retry attempts are not observable
from the session event stream.

The command runs one repetition by default. A repetition includes all 3 tasks
on each enabled engine and both phases (12 bounded phase runs with the default
upstream baseline). Each phase defaults to a 90-second hard timeout and the
whole pilot has a 20-minute cap. Increase to two repetitions only after
reviewing a first pilot. The command prints per-phase progress and writes a
summary plus sanitized JSONL events. It stores generated production Go source
for audit and validates it against pristine tests in a separate holdout copy;
model-edited tests never affect the correctness result. Every run also writes
the manifest-listed UTF-8 source under `source-snapshot/files/`, hash-checked
against `source-manifest.json`; no binary or credential files are included.

Run from the repository root:

```sh
go run ./cmd/pkbench -repetitions 1 -timeout 90s
```

Use `-unreal=false` to run only pk, `-out DIR` to select a private result
directory, or `-repetitions 2` for a second pilot. The command builds both
runners itself. Before running tasks, it verifies upstream flags, request
fields, and provider credential loading with a local invalid-session request
that exits before any model call. It resolves the existing pk credential
through pk's auth loader, never prints or copies credential contents, and gives
the upstream process a path to that same protected auth file. Unreal's pinned
Codex adapter does not refresh expired credentials; an expired credential
makes preflight fail before the pilot starts.

For the output-default ablation, use `-output-cap-ablation`. For the
benchmark-only compact tool-description ablation, use `-tool-schema-ablation`;
its exact scope and token-measurement limits are documented in
[`experiments/tool-schema.md`](experiments/tool-schema.md). These paired
experiments use the same two fixtures and Luna-medium settings but are
separate runs.

Fixtures are separate Go modules under `benchmarks/tasks/`. Their TODO
implementations intentionally fail tests, so repository `go test ./...` does
not include them. `cmd/pkbench` unit tests cover event usage parsing, missing
counter availability, credential redaction, and raw event sanitization.

## Interpreting results

The pilot can show whether session continuation reports cached tokens, and it
can compare completion, response counts, token usage, and wall time for pk and
the pinned runner on these specific tasks. It does not establish a general
quality or cost ranking: each task is small, model output is stochastic, and
system prompts/tool-output policies differ. Repeat successful and failed
cases before treating a difference as actionable. Inspect the archived source
and holdout results alongside `summary.json`; keep provider usage as the source
of truth rather than inferring cache savings from a stable cache key.
