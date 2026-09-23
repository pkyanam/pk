# pk project checklist and durable handoff

Updated 2026-09-22. This checklist consolidates the user's project requests from project start through the current overnight work. `[x]` means evidence exists; `[~]` means active or partly delivered; `[ ]` means still owed. It preserves original goals and keeps ideas proposed by the team from being mistaken for user requirements.

## Product and implementation

- [x] Create the public GitHub repository at [github.com/pkyanam/pk](https://github.com/pkyanam/pk) and use it as the canonical public project.
- [x] Build pk as a low-level, resource-conscious Go coding harness on the Unreal Agent foundation; explore deeper harness behavior and preserve extensible seams. Initial foundation and upstream research are in the commit history and `docs/research/`.
- [~] Keep Go as the preferred implementation language and pursue low CPU/memory overhead as an explicit design goal. The current runtime is Go; measure resource use before claiming it is more efficient than alternatives.
- [~] Verify the motivating Unreal claim of roughly 40% fewer tokens and compare pk against Unreal, Pi, Codex, and other harnesses with matched tasks. This remains an evaluation question, not a pk result; no relative quality or savings claim is supported yet.
- [~] Pursue the user's ambition for pk to outperform Pi/Codex while treating superiority as an unproven goal. Validate with matched success, cost, latency, and reliability evidence before making any comparison public.
- [~] Use multiple GPT-6 Luna agents for cost-conscious implementation/research where work can be split independently; root coordinates and reviews integration while agents use the current harness/tools. Keep parallel work reviewable and record evidence.
- [x] Make the app installable and runnable early, with real project working-directory behavior and native ChatGPT sign-in. See `docs/getting-started.md`; the installer and auth flow are in the shipped code.
- [x] Use Luna as the default economical model and expose model/reasoning-effort selection. Defaults are `gpt-6-luna`/`medium`; CLI config and TUI pickers exist.
- [x] Deliver the native OpenTUI session, slash-command controls, session/new-session handling, status, and durable detached tasks in separate task folders. The README, getting-started guide, validation record, and `4f0a57e` describe this release.
- [x] Preserve cache-friendly session prefixes and expose provider-reported usage/cache telemetry. One live short smoke observed 2,560 cached-input tokens on a later response; this is an observation, not a general cache guarantee.
- [x] Add rich chronological progress and tool activity with command/output/result details, bounded display, and redaction of common credential-shaped values.
- [x] Add foreground AskUser questions with choices/freeform answers, acknowledgment, and Escape cancellation, distinct from permission approvals. A live smoke verified MINT/BLUE choices, Bash execution using the selected answer, and a final response; current implementation is at `1636be2`. Use confirmation when an interaction needs a user choice; do not imply that the user's full-access process permissions are sandboxed.
- [x] Fix terminal viewport/composer clipping, top separator, stale new-session behavior, and scrolling as covered by the current UI smoke.
- [x] Add a concise marketing README, badges, and an explicitly conceptual mockup placeholder (`fd9633c`).
- [~] Preserve the requested visual direction in the final product mockup: premium dark terminal UI, mint accent, and ASCII `pk` wordmark. The committed preview is conceptual; confirm it matches the requested treatment in the actual TUI instead of presenting the concept image as a screenshot.
- [~] Produce a few low-effort image-generation mockups, select the dark/mint direction as the first concept, and keep its ASCII `pk` wordmark. Current committed preview is conceptual; any missing mockup variants remain to be made and reviewed.
- [x] Make the TUI command surface easy to discover from the Codex-style slash-command menu, including session, task, status, login, cancellation, model, and effort controls.
- [x] Support installing/running `pk` from the current project folder with that folder as the working directory, and document PATH setup for the installed binary.
- [~] Push implementation in frequent reviewable increments and relaunch/notify the user at meaningful shipped checkpoints. Commits `fd9633c`, `4f0a57e`, and `1636be2` are recorded; continue this through remaining work rather than waiting for one large final batch.
- [~] Finish attachment support for image/PDF/file inputs against provider/model capability. Attachment code exists in the working tree; document exact support/limits and run provider-backed or fixture validation before claiming broad support.
- [~] Finish optional mouse interaction and polish the terminal experience. Keyboard paths are tested; mouse-specific behavior still needs a focused real-terminal check.
- [~] Make rich tool cards compact, grouped, and expandable/clickable with readable built-in Markdown rendering. Current tool previews/results are implemented; the requested interaction and visual polish need explicit validation against the latest layout.

## Research and evidence

- [x] Study Unreal, OpenCode V2, Grok Build, Codex, Hermes Agent, Claude Code, and the verified DeepSeek Harness. Source-backed scope and caveats are in `docs/research/overnight-harness-landscape.md` and related research documents.
- [x] Research structured ask-user and confirmation flows, mouse-aware terminal behavior, long-task controls, and context efficiency; distinguish public source evidence from product claims or nonpublic implementations.
- [~] Establish honest, comparable efficiency and task-success benchmarks against Unreal, Codex, and other harnesses. A benchmark scaffold is in the working tree, but no comparative results or gains have been established. Pin configuration, use repeated matched tasks and independent verification, report cost/cache/latency, and retain traces before making any optimization claim.
- [x] Run shipped-feature validation: detached-task smoke in a fresh workspace completed and passed its Go test; TUI detached task wrote the expected marker; cache telemetry showed one observed reuse; 80x24 layout and 120x36 rich-progress PTY smokes passed. Root reports the current full Go race/vet run, ten UI tests, and CI are green after the AskUser checkpoint.

## Marketing and delivery

- [x] Prepare a friendly, concise README with centered product framing and badge/mockup treatment; the mockup is labeled conceptual, not a real product screenshot.
- [ ] Create the requested image-generated marketing asset(s) after technical validation, using the chosen visual direction; deliver drafts for review.
- [ ] Draft the requested X launch posts for review after technical validation. Do not publish or post them without explicit authorization.
- [~] Make the final local launch video in Cmux using Cap: open/focus Cmux through Computer Use, drive a real pk flow, record a truthful 30–60 second product demonstration, then edit and inspect the MP4. Preflight and production plan are complete in `docs/launch-video-plan.md`; no desktop capture or recording has started. Cap screen permission is available. Do not upload or publish without explicit authorization.
- [x] Research video production tools and a resource-/storage-conscious approach. `docs/launch-video-plan.md` recommends Cap window capture and FFmpeg edits; Remotion and Motion Canvas were reviewed as higher-overhead choices for this one-off video.
- [x] Draft an initial README tease that the product is coming soon, then replace it with the concise public-facing README and conceptual design preview (`fd9633c`).

## Long-running work and overnight continuation

- [x] Keep background coding work durable, resumable, and isolated in a new workspace, with task status, follow/attach, cancel, and resume operations. Smoke evidence and recovery limits are in `docs/getting-started.md` and `docs/validation.md`.
- [x] Let users start a task from the foreground `/task` or `/new` workflow and see its separate workspace; do not describe `/detach` as a way to keep a direct foreground process alive after exit.
- [x] Preserve cache-friendly provider prefixes across turns and resumes where possible, and report provider usage rather than promise cache hits.
- [~] Continue small, reviewable improvements through the requested overnight window. The handoff schedule is recorded in `docs/overnight-worklog.md`; root chose 30-minute improvement passes through 08:00 EDT as an operational assumption for “through morning,” not a user-specified exact wake time. At each pass, update evidence, blockers, and the next step.
- [ ] Complete remaining validation for attachment formats, mouse behavior, expandable tool cards/Markdown, and matched benchmark work; then update user-facing docs with only verified behavior.
- [ ] Finish and inspect the final video and optional launch assets; leave them local for review unless the user separately requests publication.

## Current handoff

Latest completed commits noted by the project owner: `fd9633c` (README/design preview), `4f0a57e` (OpenTUI, durable tasks, cache telemetry, rich activity), and `1636be2` (AskUser and transcript scrolling). Root reports Go race/vet, ten UI tests, and hosted CI green at the latest checkpoint. The current working tree contains subsequent UI/RPC, benchmark, and attachment work; treat it as in progress until the owner records its tests and commit state. The benchmark has no comparative result yet. The video is at preflight/plan stage only.

Next: complete the narrowly scoped UI/file-input validation; capture matched benchmark evidence without overstating results; use Cmux and Cap for the real product demonstration; update this checklist and `docs/overnight-worklog.md` at each checkpoint so a compacted or resumed session can continue without reconstructing history.
