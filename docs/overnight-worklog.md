# Overnight pk improvement log

This log is the handoff for the scheduled improvement passes through 08:00 EDT on 23 September
2026. Each pass should append a dated checkpoint with completed work, evidence, blockers, and the
next small step. Keep changes reviewable; do not publish marketing material or claim benchmark gains
without evidence. Do not post promotional material externally without explicit authorization.

## Scope and order

1. Continue reproducing and diagnosing the urgent OpenTUI freeze report, then rerun terminal
   stability checks. `eba1d60` fixes reproducible input/focus defects, but the reported freeze has
   not been independently diagnosed as resolved. AskUser and scrolling passed earlier live checks.
2. Revalidate the latest TUI interactions: clickable selectors/question choices, scroll behavior,
   grouped compact tool rows, expansion, and Markdown rendering. Keep unattended task workers free of
   model-controlled blocking questions.
3. Continue the Pi-like extension design and implementation in `docs/extensions-design.md`.
4. Continue the model-invokable image-generation bridge through the existing Codex CLI login only
   where the provider driver supports it. A live pk smoke succeeded with Luna orchestrating an
   explicit Astra image driver; do not call this native Luna generation or shipped functionality.
5. Expand the matched success, latency, token, and cache benchmark. Do not claim optimization gains
   from the current single-repetition pilot.
6. Keep the six-harness comparison in the research owner's document. After technical behavior is
   validated, use the reviewer's Cap/Cmux video plan to capture and edit a concise local demo; draft
   optional marketing assets for review; do not post them externally.

Each change should preserve workspace and process permissions as currently documented. A new
interaction must not imply that Bash is sandboxed. Avoid broad rewrites when a small tested change
will do.

## Checkpoints

### 22 September 2026 — initial overnight handoff

- `fd9633c` added the concise README and a design preview. The preview is conceptual, not a product
  screenshot.
- `4f0a57e` shipped the OpenTUI agent, durable tasks, cache telemetry, and rich activity. The parent
  reports GitHub Actions green for this checkpoint.
- The parent reports the rich-progress implementation passed the full Go race suite and the OpenTUI
  suite; an installed 120×36 PTY showed Bash command/output/exit information, a Luna progress message,
  a second tool result, and the final answer in order.
- The foreground question broker and question UI are in the working tree. The UI supports listed
  choices or freeform composer input, waits for an answer acknowledgment, and lets Escape cancel the
  current question/turn. Live validation was still pending at this initial checkpoint; it is
  superseded by the later AskUser and transcript checkpoint below.
- Next: finish live question and scrolling checks, then update the getting-started, architecture,
  research, and validation docs to distinguish foreground questions from detached tasks. Continue
  with UX polish, supported file/PDF/image research, then reproducible benchmark work.

### 22 September 2026 — AskUser and transcript checkpoint

- `1636be2` added interactive model questions and fixed transcript scrolling.
- Root reports live AskUser validation passed: the model offered MINT/BLUE, the chosen response was
  used in a Bash action, and the run ended with a final response. Questions remain a foreground-only
  flow; detached workers do not have a durable model-question/reply lifecycle.
- Root reports the full Go race/vet checks, ten UI tests, and hosted CI are green at this checkpoint.
- The durable inventory is `docs/project-checklist.md`. Remaining visible work includes final mouse
  checks, attachment/file-input support validation, grouped/expandable tool cards with Markdown,
  matched harness benchmarks, review-only marketing assets, and the final local Cap/Cmux video.
- Benchmark scaffolding is not benchmark evidence: no comparative success, latency, token, or cache
  result has been established. Do not claim a gain.
- Cap preflight is complete and capture permission is available, but the desktop has not been
  captured. `docs/launch-video-plan.md` contains the verified local command paths and recording plan.
- Next: keep changes small and evidence-backed; update this worklog and the checklist after each
  meaningful checkpoint. Keep the recording and launch assets local for review unless publication is
  explicitly requested.

### 22 September 2026 — explicit attachments research checkpoint

- The attachments owner added `internal/attachments` as a bounded loader for explicitly selected
  paths. It reads text files, routes image paths through `ViewImage`, and extracts PDF text only with
  a pinned parser and strict byte/page/parser limits. Package and race tests pass locally.
- The one-shot CLI `--file`, RPC `files`, and foreground TUI `/file` queue routes are integrated.
  Root reports live Luna smokes for PDF text extraction and an explicitly selected external PNG
  through `ViewImage`. The OpenTUI uses explicit path entry and has no file browser. Native PDF
  upload remains unsupported by pk's pinned adapter; the fallback omits OCR and page images.
- Owners: CLI/RPC integration — CLI owner; OpenTUI mouse, compact tool rows/expansion, Markdown — TUI
  owner; benchmark — cache-runtime owner; six-harness research and demo plan — reviewer; root handles
  install, integration validation, and commits.
- Package and race tests pass for attachment path, source/text budgets, malformed/scanned PDF,
  cancellation, and FIFO rejection. Next: continue attached-file validation and matched benchmark
  work. At this checkpoint, research into model-invoked image generation through the existing Codex
  CLI login had not yet confirmed capability or implementation.

### 23 September 2026 — pilot and next priorities

- The benchmark pilot at `benchmarks/results/pilot-20260923T033523Z/` used one repetition per task
  and passed all six correctness checks across pk and Unreal Agent v0.1.1. In this pilot pk generally
  consumed more tokens. The sample is too small for a comparative performance conclusion; it shows
  no pk token-efficiency gain.
- A Codex CLI image-generation tool was verified on the supported Astra driver. The current driver
  does not support Luna, and the pk bridge is not integrated yet. Keep this as an active integration
  question, not a shipped feature.
- Pi-like extension points are under development; see `docs/extensions-design.md`.
- The TUI owner reports clickable selectors/question choices, transcript mouse scrolling, compact
  grouped tool rows with expansion, and core Markdown rendering are implemented. An urgent UI freeze
  investigation remains incomplete, so prioritize reproducing it and validating stability before
  further polish.
- Next: resolve the freeze, rerun focused TUI tests and real terminal checks, then continue image
  generation integration and repeated benchmark trials. Preserve the pilot artifacts and make no
  performance claims from one repetition.

### 23 September 2026 UTC — installed checkpoint `b544505`

- `b544505` with compact tool cards and bounded file inputs is pushed and installed; CI is green.
- The urgent UI freeze investigation remains unresolved and is still the next priority. Pi-like
  extensions and the Codex image-generation bridge are not integrated/shipped yet.
- Keep the one-repetition benchmark result unchanged: all six correctness checks passed, pk generally
  used more tokens, and no efficiency gain is established.

### 23 September 2026 UTC — installed checkpoint `eba1d60`

- `eba1d60` is pushed and installed, including the compact tool-card and bounded-file-input work from
  `b544505`; the reported full check/build passed. The OpenTUI suite passed 17 tests and 72
  assertions.
- An installed 54×183 PTY smoke completed two Luna turns; the composer cleared after each turn and
  accepted follow-up typed text. This addresses reproducible input/focus defects, but root has not
  independently diagnosed the originally reported freeze as resolved. Ask the user to relaunch for
  the installed build and keep the freeze investigation open.
- Benchmark review found the fixed 20-minute experiment timeout can cut short a valid two-repetition
  run at maximum 90-second phase timeouts (24 phases allow 36 minutes, plus setup). The historical
  one-repetition pilot completed and remains valid; repair the bound before relying on full-length
  two-repetition runs.

### 23 September 2026 — TUI attachment validation

- The OpenTUI supports `/file PATH`, `/files`, `/files remove N`, and `/files clear`, with an
  8-file queue, removable chips, and foreground-only sending. Queued paths remain in the draft after
  prompt errors and clear after a successful `attachments_loaded` acknowledgment; detached-task
  creation does not transfer or consume them. There is no native file browser.
- Root reports the UI suite passed 22 tests and 91 assertions, with check/build passing. An installed
  80×24 PTY using the `eba1d60` Go backend attached `/tmp/pk-attachment-check/marker.pdf`, submitted
  the prompt, received the loaded notice, and returned the exact expected marker `PK_PDF_MINT_731`
  from Luna; the composer was empty afterward.
- Native PDF upload, OCR, and page-image extraction remain unsupported; the existing local bounded
  PDF text extraction applies. Keep the foreground queue behavior distinct from detached tasks.

### 23 September 2026 — image generation and output-cap pilot (working-tree evidence)

- Root reports a live end-to-end pk image-generation smoke at `/tmp/pk-imagegen-live-check/`:
  Luna invoked the ImageGen tool once, with `gpt-6-astra` explicitly selected as the image driver
  and existing ChatGPT authentication. It produced `mint-cursor.png` in about 30 seconds. This
  demonstrates Luna orchestration of a supported Astra driver, not native Luna image generation.
- The generated result was visually reviewed and copied to
  [`output/launch/pk-mint-cursor.png`](../output/launch/pk-mint-cursor.png). It is local concept
  art, not a product screenshot and not posted on social media or used in an external campaign. The original event log remains in the temporary
  validation directory.
- The output-cap ablation at `benchmarks/results/output-cap-rep1-20260923/` completed all eight
  phases, but every phase reported zero omitted output limits. The default-cap treatment was never
  activated; classify the run as a no-op/inconclusive and make no token or quality claim from it.
- Extension and image-generation integration code has since been committed/pushed as `1ebe672`; it
  remains uninstalled while the current UI work is under review. The
  extension prototype loads only explicitly supplied manifests; CLI tool calls are async durable
  operations, while CLI commands are not wired, protocol-v1 hooks are rejected, and progress is not
  streamed. Root reports full Go race/vet and a live CLI smoke: Luna invoked `workspace_stats` once,
  and the subprocess returned ready/completed before Luna accurately reported 0 files, 0 directories,
  and 0 bytes. This is working-tree evidence, not a release claim.
- The TUI owner reports shortcut WIP as Enter submit, Shift+Enter/Ctrl+J newline, Ctrl+P command
  palette, Ctrl+O tool detail toggle, Escape cancel, and Ctrl+D detach/exit. New intake work includes
  drag/drop, Cmd/Ctrl+V files and images, path-only and path-leading prompts, and selection/copy
  behavior. The owner has not confirmed final handling for all these flows; the root requests removal
  of the green connected indicator plus persistent progress across model/tool gaps and provider-load
  delays. At this point these controls still awaited GUI checks; see the later Cmux validation below.
- Next: continue the urgent UI freeze investigation and checkpoint review; keep extension/ImageGen
  capabilities marked pending installation. Rework the output-cap experiment so
  the intended omission/default behavior is actually exercised before interpreting results.

### 23 September 2026 — backend extension/ImageGen checkpoint `1ebe672`

- Root reports `1ebe672` committed and being pushed with the extension, ImageGen, clipboard backend,
  and benchmark work. Full Go race tests and vet passed; live `workspace_stats` and ImageGen smokes
  passed. The installed backend binary is still `9a`; do not say the new backend is installed yet.
- Luna orchestrated an ImageGen call through the explicit Astra driver and ChatGPT authentication;
  the extension smoke invoked `workspace_stats` successfully. Neither establishes native Luna image
  generation, and neither justifies cache/token savings claims.
- The current UI checkpoint `a67` remains uninstalled pending review. GUI verification of drag/drop,
  Cmd/Ctrl+V, pasted paths, selection/copy, removal of the connected indicator, and persistent
  activity is still pending.

### 23 September 2026 — current handoff update

- Root reports backend binary checkpoint `9a` installed; backend commit `1ebe672` is pushed but not
  installed, and UI checkpoint `a67` is still awaiting a new release. Do not treat current clipboard, drag/drop, connected-status, or persistent-progress UI
  changes as released until the parent confirms the checkpoint and GUI checks.
- Benchmark runner follow-up fixes the CLI outer bound for repeated phases. On Unix, canceled phase
  commands now run in a process group; cancellation sends TERM, waits 750 ms, then KILLs the group.
  Cancellation is distinguished from a per-phase deadline, and a regression test covers a child
  process holding pipes open. Root reports full Go race tests, vet, and tagged CLI checks passed.
- ImageGen result provenance: the event log is at `/tmp/pk-imagegen-live-check/events.jsonl`; the
  generated file was copied to [`output/launch/pk-mint-cursor.png`](../output/launch/pk-mint-cursor.png)
  after visual review. The model orchestrating the call was Luna; the explicitly selected generator
  driver was Astra. Do not say Luna generated the image natively.

### 23 September 2026 — Cmux input and status verification (UI WIP)

- Root reports the current UI suite passed 36 tests and 156 assertions; typecheck and build passed.
  The UI changes are not yet released; the final Markdown normalizer removal remains in root review.
- In a real Cmux session, Shift+Enter created a second composer line without submitting. A path-only
  prompt and `notes.txt describe this` both attached the file and returned the exact expected marker.
  The UI showed “Waiting for model” and later “Ready.” The green connected indicator is gone.
- Double-click selected transcript text; Ctrl+Y copied 22 characters, and Ctrl+V pasted the exact
  marker. Finder copy of a generated `mint-cursor.png` queued its correct absolute path including
  spaces. With image contents on the clipboard, Cmd+V caused Cmux to export a temporary image path
  that pk queued correctly. The subsequent own-file Cmd+V check was interrupted, so direct native
  Finder-file Cmd+V is not yet verified. Cmux intercepts Cmd+C; Ctrl+Y is the verified copy route.
- Native drag/drop remains unverified in the GUI, although its parsing tests pass. Ctrl+J is tested
  for a newline; Ctrl+P opens the slash menu, Ctrl+O toggles tool details, Escape cancels, and Ctrl+D
  detaches/exits. These are current UI behaviors, but not yet part of the installed checkpoint.
- Next: complete an uninterrupted Finder Cmd+V test and native drag/drop test, recheck long waits and
  provider-load progress, and obtain root review/commit/install before describing the UI as released.

### 23 September 2026 — installed UI/backend and prompt reference

- Root reports backend `1ebe672` and UI `c589e26` installed. The final UI suite passed 36 tests and
  150 assertions, plus typecheck and build. Cmux confirmed path-plus-prose attachment, Shift+Enter
  newline without submission, Ctrl+Y text copy, activity-state changes, and clipboard image-to-path
  handling. Native drag/drop and uninterrupted Finder-file Cmd+V remain unverified; Cmux intercepts
  Cmd+C, so Ctrl+Y is the verified copy route.
- The new [`docs/prompt-reference.md`](prompt-reference.md) records the complete assembled TUI
  prompt template, conditional workspace/skills sections, and the four exact built-in tool schemas.
  At this checkpoint the dynamic pk/model identity replacement was pending; that state was
  superseded by `c0c9952` below. Legacy snapshots remain unchanged, and `/new` adopts the new prompt.
- Extensions and the optional ImageGen driver are now in the installed backend. The live ImageGen
  smoke used Luna as orchestrator with `gpt-6-astra` as the explicit driver; this does not show native
  Luna image generation. Next: complete the remaining input GUI checks and confirm identity behavior
  in a fresh session before marking that prompt change released.

### 23 September 2026 — model-aware identity checkpoint `c0c9952`

- Root reports `c0c9952` committed and atomically installed. The full Go race suite and `go vet`
  passed. This backend also loads the TUI's default skill directories consistently.
- New session snapshots replace the inherited Unreal/Labs identity line with pk and the current
  selected model ID. The identity is filled from each request's model, so model switching updates it.
  Legacy snapshots retain their previous prompt; use `/new` to adopt the updated prompt and tools.
- Tests verify the actual request field when switching models. A live Luna smoke in session
  `ebe01e531f6708ba4e8f42b57803a2af` reported “I’m pk, running in the pk harness with the configured
  model ID gpt-6-luna.” The model's wording is one bounded smoke observation; the request-level
  tests are the direct verification.
- The first launch card is committed in the public repository; it has not been posted to X or used
  in an external campaign. Avoid calling repository inclusion “unpublished.”
- Next: continue remaining Cmux input checks and investigate the reported freeze. Keep marketing
  assets as review drafts and do not post them externally without explicit authorization.

## Checkpoint template

```text
### YYYY-MM-DD HH:MM EDT — short label

- Changed:
- Evidence: commands/tests/smokes and outcomes
- Open issues or limits:
- Next:
```

### 2026-09-23 01:18 EDT — skills/plugins checkpoint and protocol scope

- Installed paired backend/OpenTUI from committed `be8b59c`, built from a clean archive to exclude in-flight updater and steering work. `/skills` discovers the saved session catalog, including bundled pk; `/plugins` manages explicit manifests for new sessions. Activity/Ready/cache is now muted directly above the composer, with the top-right cleared.
- Evidence: isolated full `go test -race ./...`, `go vet ./...`, Go build; UI typecheck, 45 tests/200 assertions and bundle build all passed. Installed RPC smoke verified bundled pk catalog and empty plugin list with a temporary PK_HOME. Existing user processes were left running.
- User explicitly requests MCP and ACP and completion ideally by 08:00 EDT. Separate Luna agents own initial MCP client and ACP server adapters with official protocol research and fixture tests. Protocol support is not installed yet.
- AgentMail worker has read-only authenticated smoke evidence from its owner; root requested stricter output bounds and corrected TUI instructions before commit. No mail mutations.
- Updater staging correctly refused a release while the new steering-boundary regression failed. Both implementations remain in flight and are excluded from this installed checkpoint.
- Next: complete steering integration and updater/restart, integrate MCP/ACP, revalidate native input and long-session stability, then record/edit the truthful Cmux/Cap demo. Benchmark superiority remains unproven.

### 2026-09-23 01:27 EDT — paired release updater installed

- Source `f608b9e` is now installed through the stable launcher; active release `20260923T052617.096650000Z-9f99d1106e9f-048f9d19`. `pk update --source DIR`, `pk rollback`, and `pk version` are available. This is CLI update support; in-app restart remains in progress.
- The updater built an isolated committed source archive, ran all Go tests, UI typecheck/tests/build, staged dependencies, health-checked and activated the release. Separate isolated real rollback/restoration also passed. The full committed Go race suite/vet passed before staging. Launcher pins binary and UI to one resolved release and preserves custom install paths; update cancellation kills Unix build process groups.
- AgentMail read-only example and documentation are committed in `ec725ee`; fixture tests and agent-reported authenticated count-only smoke passed. No email mutations or social posts.
- Luna workers briefly stopped on usage limits; the user reset usage and requested continued token-conscious Luna delegation. Existing agents resumed their files; no duplicate implementation was started.
- Steering's exact tool-result boundary and error handling passed focused runner tests. RPC/UI, MCP, ACP, and reload remain uncommitted integration work. OMP was added to the research checklist.

### 2026-09-23 01:34 EDT — steering checkpoint; installation gate findings

- Committed boundary-safe steering as `ac26d01` and initial MCP stdio adapter as `b66a375`. MCP configuration/CLI/TUI bridge remains in progress; do not advertise the package alone as usable MCP support.
- Steering focused runner/RPC race checks and 49 UI tests/220 assertions passed. The next installed update was correctly refused: full isolated checks exposed other one-second imagegen fixture deadlines under concurrent load, plus inherited launcher's PK_UI_ENTRY leaking into build-time UI-path tests. Luna owners are fixing both; the installed `f608b9e` release remains intact.
- ACP and clean reload helpers are in shared uncommitted work. Preserve explicit file ownership during integration. Update/reload/rollback TUI controls are next; no hot-reload claim until real process handoff is verified.

### 2026-09-23 01:42 EDT — steering and ACP installed

- Installed `e64a9eb` as managed release `20260923T054029.274439000Z-d8f879af2234-ba3650fa`. It includes negotiated mid-turn steering and the initial `pk acp` stdio server, plus isolated update-build environment handling. Full committed Go race suite/vet passed; the updater's Go/UI validation/build gates passed. Installed ACP initialize/session-new wire smoke passed without provider calls.
- User reported a missing AgentMail worker and tool-description confusion while inside its example directory. Rebuilt the ignored worker and verified initialize returns its three tool names without mail API calls. Enable/runtime executable checks and concise model-tool identity guidance are in progress.
- User explicitly requests copy-on-selection-release and a GitHub-source updater through `pk --update`, `pk -update`, or `/update`. Luna owners are implementing native confirmed clipboard writes and canonical GitHub source staging. Neither is installed yet.
- Computer Use validation was deferred after Cmux reported external user interaction; no existing user session was interrupted or recorded.

### 2026-09-23 01:54 EDT — GitHub updater and native clipboard checkpoint

- Installed clean public GitHub revision `eef6c46` through an actual `pk --update` fetch/build/activation. Full isolated Go race suite/vet and updater Go/UI gates passed (52 UI tests). Stable launcher retains paired releases; existing user processes were not restarted.
- This release adds copy-on-selection-release with native clipboard acknowledgment, GitHub update aliases and TUI maintenance controls, MCP CLI configuration, and plugin executable availability checks. Native GUI copy and reload still need end-to-end verification.
- Rebuilt the ignored AgentMail worker with the committed redacted HTTP-error handling. No mail was sent or modified.
- MCP RPC and `/mcp`/`/tools` UI checkpoint committed as `320cb9c`, not installed yet.
- New user report: assistant final text arrives but foreground turn stays active, and some new-session paths lose steering capability. Backend and TUI Luna owners are prioritizing lifecycle/negotiation regressions before the next release.
- Replay-compaction benchmark first preflight failed because a git archive lacked repository metadata; no provider calls occurred. Retried from clean git checkout `8e2783d`, with two repetitions and bounded per-phase timeouts. Production compaction policy remains unchanged pending results.

### 2026-09-23 02:05 EDT — foreground completion hotfix installed

- Installed public GitHub revision `0d5476d` as release `20260923T060454.646785000Z-3b3cead7ab77-e9f7c088`. Foreground QueueInputs no longer suppresses normal idle completion; `/new` and `/attach` retain negotiated steering. MCP RPC/UI `/mcp` and `/tools` are included.
- Evidence: isolated full Go race suite/vet/build passed, all updater Go/UI validation gates passed. One live installed Luna RPC smoke returned `turn_finished` without Escape or input-channel closure; subsequent new/attach events both advertised steering. Existing user sessions were left untouched.
- Replay-compaction pilot completed all eight paired task runs with passing holdouts. Luna owner is analyzing and sanitizing results; no efficiency claim yet. Provider selection, managed subagent wiring, TinyFish tools, and the next skill refresh remain in progress.
