# Static tool-description ablation

This is a benchmark-only experiment. It compares the existing static tool
schema with a compact-prose variant, using the same `pk` production system
prompt, `gpt-6-luna` at `medium`, task prompts, empty skills, fixtures, and
holdout evaluator. It changes no production defaults and does not alter the
runner preamble or workspace instructions.

## Why this treatment

The pinned Unreal Agent v0.1.1 preamble is 253 words by `wc -w`, before `pk`
replaces its opening identity sentence. The separate `pk` workspace
instructions and dynamic workspace/platform fields also remain untouched;
those instructions shape long-run behavior.

Instead, the treatment shortens five descriptions in the static tool schema:
the Bash tool, Bash `max_output_length`, SkillUse tool and name, and ViewImage
path. Bash's concurrency wording overlaps with the preamble; the child-process
shutdown behavior does not, and the compact description retains it. The long
output-truncation explanation is shortened while preserving its character
limit, default, head/tail, omitted-count, and full-output-path semantics.
Across the five changed strings, the current schema uses 495 UTF-8 bytes and
the treatment uses 374, a deterministic 121-byte reduction per request. Across
all static tool descriptions, measured totals are 573 bytes before and 452
after. This is a byte-level manipulation check, not a token estimate. Every
tool name, translator, parameter, type, bound, required field, default, and
operation behavior stays unchanged. Provider-reported input and cached-input
tokens show whether the request change reduces measured usage; holdout tests
check whether behavior remains correct.

The task pair is a small clamp control and a moderate route-selection feature
in a repository containing 48 unrelated notes. Both arms get fresh copies
initialized with the same Git baseline. Each task has a new-session
implementation phase followed by a resumed-session verification phase. The
completed pilot used two repetitions: 16 bounded model phases, with task and
arm order counterbalanced in the second repetition. Its full records and
source snapshot are in
[`benchmarks/results/tool-schema-rep2-20260923/`](../results/tool-schema-rep2-20260923/).
All holdout checks passed, but two repetitions do not establish a causal token
or quality improvement; the recorded provider usage varied across paired
tasks and repetitions.

## Run

```sh
go run ./cmd/pkbench -tool-schema-ablation -repetitions 1 -timeout 90s
```

The output records exact tool-description bytes before/after and changed field
count for both arms, plus provider-reported input, uncached input, output,
cached input, response counts, wall time, and immutable-holdout results. For
the pinned tool schema the manipulation check is 573 to 452 bytes per request.
Review actual token usage and quality together; string-byte reduction alone
does not demonstrate fewer provider tokens or an efficiency gain.
