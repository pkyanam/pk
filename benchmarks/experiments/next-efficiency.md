# Flat subagent schema on a multi-file feature

## Evidence and hypothesis

The largest high-confidence trim that preserves the same runtime actions is the
five default subagent declarations. A local request capture showed the current
tool schema at 4,004 JSON-value bytes; the benchmark-only action dispatcher
changes the five `SubagentStart/Status/Send/Wait/Cancel` definitions into one
`Subagent` definition and keeps the same manager operations. Its direct,
loopback wire-capture test reduced the schema from 2,124 to 1,317 bytes (807
bytes). That is a size manipulation check, not a token prediction. See
[`scripts/benchmark-capture-smoke.py`](../../scripts/benchmark-capture-smoke.py)
and the [request capture](../results/context-capture-luna-low-20260923/summary.md).

There is already one two-repetition live Luna/low trial on clamp plus intervals.
Both arms completed two children and passed immutable holdouts. Combined
parent-and-child input/output was 54,254 tokens for the flat dispatcher and
59,049 for the five direct tools (8.1% lower in aggregate). Combined uncached
input was 39,692 versus 40,375 (1.7% lower), and the treatment used one extra
response. One pair was nearly tied while the other supplied most of the
difference, so this is a promising but unconfirmed result, not a product claim:
[`counterbalanced live result`](../results/subagent-schema-counterbalanced-20260923/summary.md).

The next question is whether that effect holds on one realistic feature whose
files divide naturally between two child agents. It tests the schema treatment
and actual end-to-end delegation cost, including child usage, not just JSON
bytes.

## Matched task and setup

Use the existing four-file webhook fixture at
[`benchmarks/tasks/webhook`](../tasks/webhook/). Give both arms the same
implementation request: implement the signed, idempotent receiver according
to the fixture contract; start exactly two children before waiting; child one
owns `event.go` and `signature.go`, child two owns `memory_store.go`, and the
parent owns integration in `handler.go`. Children may edit only their assigned
production files. Preserve the current webhook requirements: validate one JSON
object and required identity fields; verify HMAC-SHA256 over the exact raw body
in constant time; enforce POST and body-size limits; reject malformed,
trailing, or unknown fields; return the specified 400/401/413/503 statuses;
insert only the first event atomically; honor cancellation; copy payload bytes;
and preserve the documented response bodies/status codes. The benchmark evaluator runs the pristine `webhook_test.go` holdout after the
implementation; the model workspace omits those tests.

Use Luna low for the parent and both children, empty skill directories, the
same configured provider/auth mode, the production `pk` system prompt, and
fresh Git-initialized fixture copies. Keep context policy at `full`. The only
treatment is five direct subagent tools versus the flat `Subagent` action
dispatcher. Counterbalance the arm order between two repetitions. Save
sanitized parent and child JSONL, immutable holdout results, actual source
manifest, tool-schema bytes, child start/completion and model/effort records,
provider input/cached/output counters, response counts, and wall time.

Run after the benchmark selector lands in a clean committed checkout:

```sh
go run ./cmd/pkbench \
  -subagent-schema-ablation \
  -subagent-task webhook \
  -repetitions 2 \
  -timeout 180s \
  -total-timeout 15m \
  -model gpt-6-luna \
  -effort low \
  -out /tmp/pkbench-subagent-webhook-2rep
```

The task selector and configurable whole-run timeout are benchmark-only
extensions. Do not repeat a timed-out or partial experiment automatically; retain
its failure records. The planned cohort contains four arms at most.

## Decision rule

Score a paired run only if both children started and completed, their model and
effort records are `gpt-6-luna`/`low`, parent and child response-usage coverage
is complete, and the independent webhook holdout passes. Otherwise publish the
failure as a quality or instrumentation result and do not call the comparison
an efficiency win.

Compare combined parent-plus-child input, uncached input, output, response
count, and wall time per successful task. Keep cached input visible as a
provider counter but do not treat it as a separate cost, infer prices, or use
schema bytes as savings. A promising follow-up requires both holdouts to pass,
at least 5% lower combined input-plus-output tokens in aggregate, no more than
5% higher aggregate uncached input, and no increase in response count beyond
the one-response tolerance already seen in the prior pilot. If those checks
fail, retain both the result and current production schema; the two-repetition
pilot alone is not enough to change the product default.

This is a pk-only policy ablation, not a fresh comparison against Unreal Agent.
The earlier pk-vs-Unreal result had different prompts and tool presentation,
so it cannot attribute their full token gap to any one feature.

File ownership here is a prompted coordination rule, not a sandbox. Final
holdout correctness and child lifecycle records do not prove which process
authored every edit. The parent intentionally integrates `handler.go`; report
this as a delegation experiment, not a verified child-only implementation.

## Results: preregistered gates not met

The two-repetition Luna/low webhook run completed all four arms. Both children
started and completed in every arm, both children had fully available usage,
action coverage was true, the recorded model/effort was Luna/low, and every
independent holdout passed. The archived [raw records and pinned source
snapshot](../results/subagent-webhook-d83e97f-20260923/analysis.md) retain the
per-arm evidence.

| Pair | Five direct tools: combined input + output | Flat dispatcher: combined input + output | Token change | Wall time: direct → dispatcher |
|---|---:|---:|---:|---:|
| 1 | 41,784 | 52,688 | +26.1% | 58,414 → 57,494 ms |
| 2 | 64,107 | 61,754 | −3.7% | 87,031 → 70,167 ms |
| Total | 105,891 | 114,442 | +8.1% | 145,445 → 127,661 ms |

Combined uncached input was 49,529 tokens for direct tools and 57,661 for the
dispatcher (+16.4%). Combined response count was 31 versus 35, exceeding the
one-response tolerance. Thus both the aggregate combined-token gate (requires
at least 5% lower) and uncached-input/response-count gates fail. Although wall
time was lower in both dispatcher pairs, two observations do not support a
general latency claim. The 807-byte schema reduction is a representation-size
measurement, not a token saving. Keep the current five-tool production schema;
do not change defaults based on this pilot.
