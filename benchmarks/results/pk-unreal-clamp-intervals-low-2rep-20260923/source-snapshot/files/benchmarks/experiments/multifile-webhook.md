# Multi-file webhook fixture

This fixture is available as an opt-in task in the pk-only replay-policy pilot;
fixture construction made no provider calls. It models a small but recognizable
integration change rather than a single-function exercise.

The implementation crosses four production files: event validation, exact-body
HMAC verification, a concurrency-safe idempotency store, and an HTTP handler
that joins them. The handler must cap request size, reject invalid signatures
and malformed/unknown/trailing JSON, distinguish first delivery from duplicate,
and avoid exposing internal persistence errors. The store must be atomic under
concurrent duplicate requests and must copy mutable payload bytes. Tests include
concurrent delivery and failure cases so a one-file happy-path implementation
cannot pass.

`benchmarks/tasks/webhook/webhook_test.go` is benchmark-owned. During a run the
agent receives a disposable copy and may edit its tests, but holdout verification
starts from the pristine fixture directory and overlays only non-test Go source
from the agent workspace. Thus deleting or weakening tests in the workspace
cannot change the scored verification. The new-session implementation phase
does not run tests; the resumed verification phase runs them and may repair the
implementation. Both phases use an empty skills directory, gpt-6-luna/medium,
and a 90-second per-phase timeout. The small `clamp` task is a matched simple
control. Task and engine order are counterbalanced across two repetitions.

The bounded command for this two-task cohort is:

```sh
go run ./cmd/pkbench -replay-compaction-ablation \
  -replay-tasks clamp,webhook -repetitions 2 -timeout 90s \
  -out /tmp/pkbench-webhook-replay-rep2
```

This covers 16 model phases at most (2 tasks × 2 arms × 2 repetitions × 2
phases) and uses the existing 30-minute whole-experiment deadline. It has not
been run. Before running, commit and review the fixture and harness changes;
then build from that exact clean revision. The output manifest hashes the pk
CLI, benchmark runner, full internal implementation packages, bundled skills,
fixtures, benchmark docs, `go.mod`, and `go.sum`, and stores a verified text
snapshot beside the results. The holdout implementation artifacts and
provider-reported per-phase usage remain auditable.

The handler tests are substantive correctness checks for exact-body signatures,
strict JSON, request limits, concurrent idempotency, copy semantics, and
unavailable storage. This is not designed to force a large Bash output. If the
eligible/compacted result counters are zero, the replay policy was not exercised
on this cohort and no efficiency conclusion follows.

This task set is distinct from the earlier two-task replay pilot. Do not pool
their results or present the new task fixture as a controlled extension of that
sample.
