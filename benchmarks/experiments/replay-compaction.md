# Replayed shell-result compaction ablation

This benchmark tests one context-replay policy on two multi-file Go tasks. Both
arms use the production `pk` CLI, `gpt-6-luna`/`medium`, the same task prompts,
empty skills, and clean Git-initialized fixture copies. Implementation starts a
new session; verification resumes that session. Each prompt asks for normal
`go test -v ./...` evidence, without padding output or changing the tool limit.
Immutable holdout tests are restored before correctness checks.

The pinned upstream builder commits completed tool results into the conversation
prefix, then replays that prefix on subsequent model requests. The treatment
acts once while translating a completed Bash result whose text exceeds 4,096
bytes: it keeps a 768-rune head and tail, the exact existing stdout/stderr
capture paths, and the exit code. It compacts only when at least one capture
file exists; otherwise it keeps the original text and reports the fallback.
The session prefix then retains that compact representation. The full capture
file is unchanged and can be read with Bash when more detail is needed. A
capture path is only known to exist when compaction runs; this experiment does
not claim indefinite artifact retention.

The per-phase event records eligible and compacted results, original and stored
result bytes, and missing-capture fallbacks. These byte counts are manipulation
checks, not token savings. Provider-reported input, cached input, output tokens,
model-response counts, wall time, and pristine holdout results are the outcome
measures. Review cache hits and uncached input separately; a smaller serialized
result may change later prefix-cache behavior. Two repetitions are a pilot, not
a general quality or efficiency conclusion.

## Run

```sh
go run ./cmd/pkbench -replay-compaction-ablation -repetitions 2 -timeout 90s
```

Use `-replay-tasks clamp,webhook` for the separately reviewed simple-control
and multi-file webhook cohort. `-replay-tasks` is valid only with this ablation;
without it, the original `routematch,eventmerge` task set stays the default.
The webhook cohort's fixture, immutable holdout behavior, and bounded command
are documented in [`multifile-webhook.md`](multifile-webhook.md). Its results
must remain separate from the earlier replay pilot.

The run is bounded to 16 model phases at 90 seconds each, with a 30-minute
overall experiment timeout. It writes checkpointed JSONL records, a source
manifest, generated-code artifacts, and a Markdown/JSON summary under
`benchmarks/results/`. Do not compare it to the prior output-cap or tool-schema
experiments as if those treatments or task sets were controlled against this
one.

## Pilot results: 2026-09-23

The two-repetition paired run is archived at
[`../results/replay-compaction-rep2-20260923/`](../results/replay-compaction-rep2-20260923/).
Both arms passed all four pristine verification holdouts. The treatment
compacted all 12 eligible results, reducing stored Bash-result text from
79,800 bytes to 24,108 bytes (55,692 fewer bytes). This confirms the treatment
was exercised; bytes are not provider tokens.

Across the eight model phases per arm, provider usage reported 72,419 input
tokens (36,067 uncached; 36,352 cached) for compacted replay and 87,203 input
(46,755 uncached; 40,448 cached) for current replay. Output tokens were 3,543
and 3,376 respectively. The four matched task/repetition totals each had lower
input usage under the treatment, while responses increased from 24 to 27.
Summed phase wall time was 109.7 s versus 115.5 s; one of the four matched
pairs was slower under treatment. These are descriptive pilot observations:
model/tool trajectories vary, and two repetitions do not establish a general
token, cache, latency, or quality effect. In particular, lower cached tokens
reflect lower total input here, not an increase in cache hit rate. This is a
pk-only policy ablation, not a comparison with upstream Unreal Agent.
