# pk overnight checkpoint — September 23, 2026

Draft checkpoint; the morning report will update pending items below.

## Run it

Run `pk` from your project directory. The installed release is `cada2b5`; use `/reload` while idle or relaunch `pk`. Fresh-session reload works before sending a first prompt and preserves the workspace/model/effort/provider.

New installation:

```sh
curl -fsSL https://raw.githubusercontent.com/pkyanam/pk/main/install.sh | bash
```

## What is available

- Go runtime with an OpenTUI interface, mint welcome wordmark, Markdown, compact expandable tool activity, progress updates, and mid-turn steering.
- Durable background tasks, saved-session browsing and recoverable bulk archive, restored tool history, provider/model/effort selection, and bounded subagents with queued task-only work.
- Interactive skill and plugin setup, MCP stdio and Streamable HTTP configuration, supported credential and OAuth flows, and an ACP interface.
- Managed GitHub updates, rollback, and session-preserving reload. Installed processes use paired immutable Go/UI releases.
- Image file and clipboard intake, bounded PDF text extraction and scanned-page previews, opt-in ImageGen, and configured TinyFish search/fetch with a zero-price Monid route and no paid fallback.
- `/usage` reads durable provider token totals locally, distinguishes unavailable counts from zero, and reports per-metric coverage. Opening it does not call the model.

See the [project checklist](project-checklist.md) for the verification level and limits of each feature. Configured integrations and local fixture tests are not proof that every external service or editor has been exercised.

## Evidence and limits

The matched Luna/low pk-versus-Unreal pilot passed all eight holdout checks, but pk used more total and uncached input tokens. It does **not** establish cost or quality superiority. Compaction results are mixed, including regressions; full context remains the default. The aggressive-compaction experiment also regressed: 544,819 input tokens versus 127,054 for the control, despite passing the same holdouts.

Read [benchmark findings](benchmark-findings.md), [prompt overhead](research/prompt-overhead.md), and the [work log](overnight-worklog.md) for measurements and reproducibility details. Synthetic allocation improvements are separate from model-token savings.

`/usage` passed the installed localhost-provider restart/attach smoke and native Cmux rendering check. The earlier complete terminal freeze has not been reliably reproduced. Native drag/drop remains unverified; Finder file-path paste and Preview pixel paste were verified. Scanned PDF page rendering passed a real local Poppler smoke and a bounded Luna/low vision check. Cloudflare account OAuth and a real external ACP editor have not been exercised.

## Launch material

- [README](../README.md), with a real coding-session screenshot and concise setup.
- [Draft X posts](launch-copy.md), not published.
- [Demo editing source](../scripts/demo-edit/README.md).
- Local 44-second edited coding demo: `/Users/preetham/Movies/pk-launch-demo/pk-coding-demo.mp4`.

The video shows a real Luna coding task and verified tests. It makes no unsupported benchmark claim. The final MP4 and reusable source remain local; nothing was posted or uploaded.
