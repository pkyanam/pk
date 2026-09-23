# Context management

The context manager is available in v0.1.5. In the TUI, open `/usage` to see the selected model's context budget,
the latest request composition, and recorded provider usage. Press `E` to edit
limits or `C` to compact saved history. Compaction runs only while the
foreground session is idle. It does not add a synthetic conversation turn.

## What the numbers mean

`Context limit` is a model capacity only when pk has a supported source for it.
`Operational input` is the working ceiling used by the estimator and automatic
compaction. When no limit is known, the default operational fallback is
128,000 tokens; that value is a safety policy, not a claim about model
capacity. With a known context or input limit, pk derives an operational input
budget after applying the output reserve and safety margin. Defaults are
25,000 output-reserve tokens and a 4,096-token safety margin.

Catalog sources have different freshness rules. For direct API providers, pk
uses supported built-in metadata or a saved provider model catalog no older
than seven days. For the native Codex provider, pk reads limit data from the
local Codex `models_cache.json`; pk does not refresh that catalog, and rejects
entries older than seven days when a `fetched_at` timestamp is present. A
provider/model-specific user override takes precedence over catalog values.
`/usage` shows the source for each limit; `unknown` means unavailable, not
zero.

The estimated request size is a heuristic, not provider tokenization. Text and
tool schemas use UTF-8 bytes divided by three plus a per-item allowance;
opaque reasoning and images use separate approximations. Image estimates are
especially uncertain. The estimate is not a hard context-window guarantee.
Request composition in `/usage` is JSON-value bytes, not estimated tokens or
the complete provider wire payload.

## Automatic and manual compaction

History compaction is enabled by default. It triggers when the estimated
projected request reaches 80% of the usable budget and aims for 65%. Defaults
reserve 5,000 tokens for summary work, use a 12,000-token summary-input
setting, and allow at most six summary calls. For larger operational windows,
summary input may scale up to 40,000 estimated tokens; each source chunk is
also capped at 96 KiB. The default summary-output target is 2,500 tokens.
Visible summary text is bounded to four bytes per target token (10,000 bytes
by default); this is a size guard, not exact tokenization. Provider output usage
can include reasoning, so it is recorded separately and is not compared with
the summary-text target. Adapters may not support a hard provider-side output
limit. These are configurable operational limits, not provider capacities.

Compaction summarizes only a safe prefix at completed exchange boundaries.
System instructions, tool schemas, the current prompt and attachments, and
unresolved tool calls/results remain in the model request. The complete
session transcript is retained. A private checkpoint sidecar stores the
summary, cut position, and fingerprint of the source transcript prefix. On
resume, pk applies it only if that prefix still matches; otherwise it refuses
to apply the checkpoint rather than silently using a stale summary. The
summary is treated as untrusted history, not as new instructions.

Use `/compact` to request a checkpoint immediately while idle. It can report a
no-op when there is no safe completed history to remove or the projected
request is already within budget. It refuses to run during an active turn,
tool operation, task attachment, or another guarded foreground operation. A
forked session is not supported by manual compaction yet. If the current
mandatory context alone is too large, or no safe completed-turn boundary can
bring the estimate under budget, pk refuses to checkpoint it. Other refusal
cases include an oversized summary request/result, invalid summary output, a
prefix-fingerprint mismatch, or exceeding the configured summary-call limit.
On failure, the saved transcript remains unchanged; summary-provider calls
already made may still have usage.

The summary-call ledger is stored in a private per-session sidecar, separately
from ordinary response usage. In `/usage`, expand `History compaction usage`
with `H` to see attempts, completed and failed calls, unknown-usage attempts,
and input/output/cache counters with per-counter call coverage. Failed
provider attempts remain in the ledger; missing counters remain unknown.
There is no cost estimate.

## Inspect and configure

The TUI `/usage` panel can edit the active provider/model override, operational
fallback, output reserve, and safety margin. Blank model-limit fields clear
that field's override; zero is accepted for reserve and margin. Changes are
saved only while the foreground session is idle.

The equivalent CLI commands are:

```sh
pk config budget show
pk config budget set --unknown-input 128000 --reserve 25000 --margin 4096
pk config budget set --provider native --model gpt-6-luna --context 1000000 --input 900000 --output 100000
pk config budget set --compaction on --trigger 0.80 --target 0.65 --summary-reserve 5000 --summary-input 12000 --max-summary 2500 --max-calls 6
```

Overrides match the exact provider ID and model ID. Use `0` for a model limit
to clear that override field. Configuration accepts at most 128 model
overrides; numeric limits are bounded to 10,000,000 tokens (the TUI editor
currently caps a single field at 2,000,000). For all options, run
`pk config budget set --help` or inspect `pk config budget show` after editing.

History compaction differs from `context_policy=compact`, which only changes
how eligible large completed Bash results are presented to the model while
retaining the original local capture. See [token usage](usage.md) and the
[compaction design notes](research/context-compaction.md).
