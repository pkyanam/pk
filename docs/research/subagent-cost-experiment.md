# Subagent tool-schema experiment

Status: experimental; production keeps the five existing controls.

At source ef77132, serializing the actual five helper tool declarations yielded
2,124 JSON bytes: Start 1,124, Status 226, Send 300, Wait 258, Cancel 216. A
minimal flat dispatcher prototype was 812 bytes. The implemented experiment,
including complete validation guidance, measures 1,317 bytes: 807 fewer bytes
than the five current declarations. These are schema bytes, not provider
tokens or cost. A single action enum avoids changing the available tools
mid-session. It must retain all five operations and the same manager behavior.

The experimental registry decorator and deterministic tests live in
`cmd/pkbench/experiment/subagent_dispatch.go`. Tests cover all five actions,
invalid arguments and result decoding after recreating the decorator. Tagged benchmark builds can select `PK_BENCH_POLICY=compact-subagent-schema`
or the unchanged `subagent-schema-current` baseline. Normal builds ignore these
policies. Use isolated benchmark homes; do not mix experimental session schemas
with ordinary installed sessions. The adapter rejects preparation or schema
mismatches before contacting a provider.

Before live evaluation:

1. Reject unknown actions/fields and enforce each action's required arguments.
   Keep task-only semantics, file ownership guidance, limits, and no recursive
   child spawning. No capabilities should disappear to improve the measurement.
2. Verify translation/result handling and actual schema-byte reduction locally.
3. Account for parent **and child** provider usage. Headless CLI previously
   omitted child usage events; parent totals alone are insufficient for a
   delegation cost comparison. Missing provider usage must remain unavailable,
   not become zero.
4. Run bounded, counterbalanced matched-model tasks that exercise delegation,
   steering, waiting and cancellation, retaining correctness, argument errors,
   response counts, input/output/cache coverage and elapsed time.

Do not promote the experimental schema based only on its byte size. A smaller
schema can cost more if it causes extra turns, invalid arguments or failed work.
The existing compact-tool-description pilot did not establish a general gain.

The real tagged-CLI loopback capture passes both policies: baseline exposes nine
tools / 4,004 schema bytes; dispatcher exposes five tools / 3,197 bytes. The four
non-subagent tools are unchanged. Both requests used synthetic local responses,
so this establishes request composition, not live cost or quality. Reproduce
with `python3 scripts/benchmark-capture-smoke.py`.

## First live pilot

At revision `53f2478`, one baseline-first paired run used Luna / low with two
independent coding children per arm. Both arms completed both children, had
complete parent/child input and output accounting, and passed pristine clamp
and interval-merge holdouts. Generated implementations and sanitized traces are
retained in [the result directory](../../benchmarks/results/subagent-schema-pilot-20260923a/summary.md).

| Combined parent + children | Five tools | Dispatcher |
|---|---:|---:|
| Input tokens | 26,171 | 20,239 |
| Cached input | 9,216 | 3,072 |
| Uncached input | 16,955 | 17,167 |
| Output tokens | 1,573 | 1,385 |
| Responses | 13 | 12 |
| Wall time | 38.587 s | 29.649 s |

The dispatcher used fewer total input tokens, but slightly more uncached input.
No dollar savings are established. This single ordered pilot is stochastic and
does not isolate cache effects or cover steering/cancellation. The trace records
the parent model and effort, but did not capture each child's actual selection;
defaults alone do not prove that neither parent requested an override. Production keeps
the existing controls pending broader, counterbalanced evidence.

## Counterbalanced follow-up

Revision `79c468b` added explicit child model/effort requests and observed child
selection metadata, plus combined uncached-input reporting that rejects invalid
per-response cache counters. The two-repetition follow-up ran baseline then
dispatcher, then dispatcher then baseline. All eight children actually used
`gpt-6-luna` / `low`; every arm completed both children and passed both holdouts.

| Rep | Interface | Combined input | Uncached input | Output | Responses | Wall seconds |
|---|---|---:|---:|---:|---:|---:|
| 1 | Five tools | 26,752 | 17,024 | 1,620 | 14 | 36.651 |
| 1 | Dispatcher | 26,688 | 19,520 | 1,367 | 15 | 23.992 |
| 2 | Five tools | 28,983 | 23,351 | 1,694 | 15 | 39.766 |
| 2 | Dispatcher | 24,780 | 20,172 | 1,419 | 15 | 33.483 |

The dispatcher was faster in both samples and used less total input/output,
but uncached input regressed in repetition one and improved in repetition two.
This is mixed evidence, not a reliable cost advantage. Children also made
different numbers of model calls despite the same model settings. The fixtures
are small and only exercise start/wait coding delegation; larger tasks and
steering/cancellation remain necessary before promoting the policy. Production
continues to use five tools.

[Raw results, source manifest, traces, and generated code](../../benchmarks/results/subagent-schema-counterbalanced-20260923/summary.md).
