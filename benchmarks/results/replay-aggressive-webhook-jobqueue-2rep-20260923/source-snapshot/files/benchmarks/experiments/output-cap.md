# Bash output-default ablation

## Why run it

The first matched-product pilot used three small Go tasks, one repetition, and
the same Luna medium prompts on `pk` and pinned Unreal Agent v0.1.1. All six
holdout checks passed. Provider totals, aggregated from its `summary.json` with
agentcalc, were:

| Runner | Input tokens | Output tokens | Cached input tokens | Observed responses |
|---|---:|---:|---:|---:|
| pk | 44,547 | 2,139 | 15,360 | 18 |
| Unreal Agent v0.1.1 | 20,309 | 1,413 | 5,120 | 15 |

In that one run, pk used about 2.19× the input tokens and 1.51× the output
tokens, with three more observed responses. These figures compare complete
runner configurations; they do not isolate the system prompt, tool schemas,
selected commands, or presentation. The event stream reports aggregate input
tokens only, so it cannot attribute a measured number to the system prefix.
No quality or product ranking follows from three simple tasks and one
stochastic repetition.

The traces give two testable clues. Pk searched `..` for `AGENTS.md` in each
implementation task even though the workspace was a self-contained fixture;
each search took about 70–90 ms. In the csvcount task it also ran `git status`
in a directory without a Git repository and received exit code 128. The next
cohort initializes an identical Git baseline commit in both arms and tells
both arms to inspect only a root `AGENTS.md`, so these avoidable setup actions
do not confound the output-policy comparison. An intervals source-write
operation reported exit code 129, but the retained command preview is
truncated and its full session output was deleted with the temporary workspace.
We cannot identify its cause from the surviving sanitized data. The new
cohort retains exact commands only for failed operations in private local
state and preserves bounded sanitized operation excerpts in its JSONL.

The old logs’ argument previews are truncated. A previous rough count of
omitted output limits based on those previews is invalid. The ablation hook
counts whether `max_output_length` was explicitly present before any
benchmark default is applied.

## Output-cap pilot: treatment not activated

The one-repetition, eight-phase pilot ran from an isolated checkout at
`9a16944`; all implementation and verification phases passed their holdout
checks. The manipulation check found 8 explicit Bash limits in the current
arm and 9 in the advertised-4K arm, with zero omitted fields and zero injected
defaults in either arm. There were also no output truncations. Since the
4,096-character enforcement path never ran, this is a no-op/inconclusive
experiment and its token, byte, and wall-time differences are not evidence
for or against the cap. The sanitized JSONL, source manifest, and archived
implementations are in
[`benchmarks/results/output-cap-rep1-20260923/`](../results/output-cap-rep1-20260923/).
An exact UTF-8 source snapshot matching that manifest is preserved in
[`source-snapshot/`](../results/output-cap-rep1-20260923/source-snapshot/).
It captures the pilot source before later cancellation and documentation
updates; use it with base commit `9a16944` to reproduce the recorded harness.

## Hypothesis and treatment

The experiment asks whether advertising and enforcing a 4,096-character
default for omitted Bash `max_output_length` fields reduces the output the
model receives and provider-reported uncached input tokens, while preserving
holdout success. This may have no effect if the model supplies explicit
limits, if commands return little output, or if truncated output forces extra
inspection and follow-up calls.

Both arms run the current `pk` CLI, its production system prompt and adapter,
the same model, effort, task prompts, empty skill directory, and task fixture
with a clean initial Git commit. The only treatment is the Bash output-limit
policy: the control uses the current schema and behavior; treatment advertises
4096 in the schema and injects 4096 only into valid Bash argument objects
that omit the field. An explicit value passes through unchanged. The control
and treatment use the same build-tagged instrumentation to count exact
explicit/omitted/defaulted calls. Normal builds have a no-op hook.

The pair consists of the existing tiny clamp task as a simple control and a
Go route-selection feature in a moderate fixture containing 48 unrelated
operations notes. Each arm gets a fresh copy, identical prompt, and the same
holdout tests. The implementation phase creates a session; verification
resumes it and runs tests. Test correctness comes from a pristine holdout
copy, never model-editable tests.

## Measurements and limits

The harness records holdout results, model responses, wall time, provider
input/output/cached/cache-write counts and availability, uncached input
(`input - cached` only when both fields are available), exact Bash limit
choice counts, returned versus raw output/error bytes, and truncated
operation counts. Cached tokens remain separate from uncached input. These
are observed model responses, not hidden HTTP retry attempts. No price
estimate is produced.

Tool event argument/output excerpts are bounded and sanitized. Model-produced
production Go files are retained as `.go.txt` artifacts. Exact Bash commands
are written only for failed/nonzero operations under
`~/.local/state/pk/benchmark-debug/<run-id>/`, in mode-0600 files inside a
mode-0700 directory; these files stay outside the repository. The result
summary includes a source-file manifest and hashes for the working source
tree and tracked diff.

The completed pilot used one repetition: two tasks × two arms × two phases =
eight bounded model phases, each capped at 90 seconds and the run capped at
15 minutes. Any rerun should first change the experiment so it exercises the
intended policy without forcing synthetic output; preserve this run as the
no-op result. A second repetition of the same no-op setup would not be useful.
The earlier pk-versus-Unreal pilot is a separate historical run. It predates
the source-manifest/snapshot procedure, and the working tree has since changed;
its summary and logs do not identify a fully reproducible source snapshot.
For reference, the pilot command was:

```sh
go run ./cmd/pkbench -output-cap-ablation -repetitions 1 -timeout 90s
```

The change is an experiment only. A product default change needs repeated
paired evidence that default application is material, holdout success is not
weakened, and uncached input or wall time improves without a compensating
increase in responses or repair loops. Publish neutral and negative results
alongside positive ones.
