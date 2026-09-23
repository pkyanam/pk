# Job queue recovery policy pilot

This opt-in cohort compares the production `pk` runner's default full Bash
result context against its opt-in compact captured-result context on one
multi-file job-queue recovery task. Both arms use the same Luna model, effort,
task prompt, clean Git fixture, empty skills directory, and session-resume
verification phase. Repetitions are counterbalanced by arm order.

The fixture has five production Go files covering the queue API, job state,
store contract, concurrency-safe memory store, and recovery implementation.
The requested change implements expired lease recovery: requeue jobs below
their attempt limit, mark exhausted jobs dead, preserve FIFO order, and keep the
state transition atomic. The second model phase resumes the implementation
session, runs the immutable fixture tests, and may repair production code. The
holdout starts from a pristine copy of the fixture and overlays only production
Go files from the model workspace, so edits to workspace tests cannot change
correctness scoring.

There is no synthetic output padding or benchmark-specific tool limit. Reading
the fixture's architecture is part of the task; compaction is considered
exercised only when the per-phase eligibility/compaction counters show that a
completed Bash result crossed the policy threshold and had its capture. If it
does not trigger, the pilot is inconclusive about this policy.

The approved run is bounded to two repetitions of one task, two arms, and two
phases (at most eight model phases), with a 90-second phase limit and a
20-minute whole-experiment deadline:

```sh
go run ./cmd/pkbench -replay-compaction-ablation -replay-tasks jobqueue \
  -model gpt-6-luna -effort low -repetitions 2 -timeout 90s \
  -out /tmp/pkbench-jobqueue-replay-rep2
```

The harness writes each phase record before proceeding, including provider
input, uncached input, cached input, output, cache-write tokens when available,
response count, wall time, tool-result byte manipulation checks, failure state,
and holdout result. These descriptive measurements on a single task do not
establish general efficiency or quality effects. Do not publish provider
credentials, local session content, or private failed-command logs.

## Pilot results: 2026-09-23

The two-repetition run is archived at
[`../results/jobqueue-replay-e697977-20260923/`](../results/jobqueue-replay-e697977-20260923/).
All eight model phases exited successfully and all four resumed verification
holdouts passed. Compact mode triggered for 12 of 12 eligible Bash results,
with no missing-capture fallbacks; it reduced those phase-recorded result bytes
from 69,416 to 24,132. Provider input tokens were 74,571 compact versus 55,741
full; uncached input was 25,931 versus 25,533. Compact mode had 19 model
responses versus 12, and summed phase time was 63,749 ms versus 82,347 ms. One
repetition was faster and the other slightly slower. The provider-reported
cached-input share was higher in compact mode, but the larger total input and
more responses do not establish lower total cost or faster execution. These two
repetitions show no overall token-efficiency gain and do
not establish general quality or latency effects. Exact raw records, source
manifest, source snapshot, and generated-source artifacts are retained with
the result.
