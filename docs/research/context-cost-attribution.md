# Context-cost attribution from the matched 22-response traces

The matched two-repetition clamp/intervals pilot contains eight phase traces per engine and 22 provider responses per engine. The run used `gpt-6-luna` at low effort. It is the best available fixed-response-count comparison, but it does not isolate a single implementation difference: the runners supplied different system prompts and tool/output presentation, and engine order was fixed. See the [pilot report](../../benchmarks/results/pk-unreal-clamp-intervals-low-2rep-20260923/summary.md) for source and run caveats.

Across the eight traces, pk reported 43,889 input tokens, including 15,872 cached, and 1,752 output tokens. Unreal v0.1.1 reported 20,011 input tokens, no cached input, and 1,333 output tokens. At the same 22-response count, pk's recorded input was 23,878 tokens higher (2.19×); its uncached input was 8,006 higher. This comparison describes these traces only. The cached count is included in total input and is not a separate saving.

The first response in each implementation trace starts a new session; the first response in each verification trace resumes the implementation session. The following table groups provider-reported input by request ordinal within those phase traces. `n` is the number of phase traces that reached that ordinal.

| Phase / response | pk n | pk mean input | Unreal n | Unreal mean input |
|---|---:|---:|---:|---:|
| New implementation #1 | 4 | 1,302.5 | 4 | 547.0 |
| New implementation #2 | 4 | 1,481.0 | 4 | 717.2 |
| New implementation #3 | 3 | 2,314.7 | 4 | 836.5 |
| New implementation #4 | 2 | 2,189.0 | 2 | 1,194.5 |
| Resumed verification #1 | 4 | 2,397.8 | 4 | 1,127.5 |
| Resumed verification #2 | 4 | 2,490.5 | 4 | 1,177.2 |
| Resumed verification #3 | 1 | 1,880.0 | 0 | — |

The first implementation request averaged 755.5 more input tokens under pk. The resumed verification request averaged 1,270.3 more. Later requests are also larger on average in this cohort, but response counts and tool trajectories differ at later ordinals. The traces do not establish that repeated history, system prompt, tool schemas, or tool output caused any specific portion of the gap.

Within each trace, total input grew from that trace's first response as follows (mean delta; the first request in a verification trace is already resumed history):

| Phase / response | pk delta | Unreal delta |
|---|---:|---:|
| Implementation #2 | +178.5 (n=4) | +170.2 (n=4) |
| Implementation #3 | +1,009.7 (n=3) | +289.5 (n=4) |
| Implementation #4 | +879.0 (n=2) | +640.5 (n=2) |
| Verification #2 | +92.8 (n=4) | +49.8 (n=4) |
| Verification #3 | +359.0 (n=1) | — |

These deltas combine whatever changed in the full request between responses, including tool results and assistant messages. They are not estimates of any individual component.

Tool and sideband evidence is asymmetric. The pk export contains 14 distinct `call_id` values and 12 assistant records. The Unreal export contains 28 `tool_call_status` records, 24 input markers, and 22 turn markers, but its sanitized tool status rows omit IDs, names, and payloads. These are event counts, not equivalent counts of model-visible messages or tool payload tokens. The pk output excerpts are bounded previews, not complete tool results. Neither trace format preserves the exact full serialized model request by component, so repeated messages, schemas, tool output, and orchestration metadata cannot be separated from the aggregate provider input count.

The next experiment is an opt-in instrumented rerun of the same task/settings cohort; it does not require the model to produce exactly 22 responses again. `pkbench` can now build a tagged pk binary that records content-free component item and JSON-value byte counts for each adapter request, correlated with response ID and provider-reported usage. It records system prompt (also included in `message_roles.system`), tool schemas, message roles, tool calls, tool results, and other input. Byte counts cover encoded item values, not transport envelope bytes or tokens. Unreal v0.1.1 has no equivalent adapter instrumentation seam, so these component metrics are explicitly unavailable for that engine. No request text or provider error text is written. The metric records are appended to each pk phase trace.

Run the same task/settings cohort with instrumentation enabled:

```sh
go run ./cmd/pkbench -context-metrics -tasks clamp,intervals -repetitions 2
```

The benchmark still makes provider calls and requires the normal pk credentials. The instrumentation is opt-in and does not change model behavior. Use the resulting metrics to choose a single component for a later paired ablation; do not infer a token reduction directly from byte counts.

The existing sanitized traces can be summarized without network access or provider calls:

```sh
python3 scripts/context-cost-attribution.py \
  benchmarks/results/pk-unreal-clamp-intervals-low-2rep-20260923
```

The script prints aggregate usage, response-ordinal means by phase, and the sideband/tool evidence available in each export. It does not inspect archived model source, expose prompt contents, or estimate tokens from text.
