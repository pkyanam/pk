# Overnight pk improvement log

This log is the handoff for the scheduled improvement passes through 08:00 EDT on 23 September
2026. Each pass should append a dated checkpoint with completed work, evidence, blockers, and the
next small step. Keep changes reviewable; do not publish marketing material or claim benchmark gains
without evidence. Do not post promotional material externally without explicit authorization.

## Scope and order

1. Finish the guided skill-browser flow and repository-based plugin installation in the TUI; source
   already includes CLI discovery/install. Verify explicit review/selection and that browsing does
   not execute candidate code.
2. Complete guided MCP setup/auth for local and remote servers, OAuth/PKCE and static credentials,
   with fixtures first. Cloudflare instructions do not authorize changing the user's account.
3. Integrate searchable saved sessions with batch archive-to-trash and restore. Keep the active
   session protected and make archive recoverable.
4. Reproduce the reported OpenTUI freeze only when the user's workspace is available and not being
   actively used. Finish stress checks of history/scrolling, questions, paste, tools, and the persistent
   activity indicator without disturbing a live session.
5. Audit privacy claims against actual network and storage code. Preserve the installed Bun
   `DO_NOT_TRACK=1` opt-out; distinguish pk from providers, skills sources, MCP servers, and plugins.
6. Continue matched quality, latency, total-token, cache, and resource measurements. Keep the
   superiority goal aspirational until repeated evidence supports it. If time remains, produce the
   local Cap/Cmux demo for review; do not publish or post.

Each change should preserve workspace and process permissions as currently documented. A new
interaction must not imply that Bash is sandboxed. Avoid broad rewrites when a small tested change
will do.

The requested delivery target is 08:00 EDT on 23 September. Treat it as a handoff deadline: finish
the highest-value reviewable slices, report anything still open accurately, and do not skip release
gates or imply that every backlog item is complete.

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

### 2026-09-23 02:09 EDT — installed Cmux interaction checks

- Created a separate Cmux workspace in `/tmp/pk-ui-validation`, leaving the user's existing AgentMail session running. A live short Luna response visibly returned to Ready.
- Selected the generated response with a mouse drag; native clipboard readback matched the expected test text (32 characters including trailing whitespace). This verifies copy-on-release in this Cmux setup.
- Invoked `/reload`; a new UI/backend process restored the same session ID and full transcript. Typed two lines with Shift+Enter afterward; both remained in the composer without submitting, confirming input remained responsive.
- Finder file-paste validation was deferred when Computer Use reported external user interaction. No private files were uploaded or recorded. Native Finder drag/drop remains open.
- Published the matched replay-compaction pilot in `a0e47cf`; it shows lower input tokens in this small sample with all holdouts passing, plus output/response-count increases and mixed paired latency. Default production behavior is unchanged.

### 2026-09-23 02:31 EDT — provider and subagent release installed

- Installed clean public GitHub revision `6ab5426` as release `20260923T062615.688896000Z-8c293a8c790f-8726563c`. Adds provider configuration/discovery and selection, foreground and durable provider routing, bounded parent-managed subagents, opt-in TinyFish tools, and refreshed self-knowledge. Existing processes remain untouched.
- Full isolated Go race suite, vet, build, and actual GitHub updater validation passed. An installed local HTTP fixture verified provider setup, `/v1/models` discovery, and a streamed Chat Completions response without a paid call.
- One bounded installed Luna/low run launched two children and verified both assigned files; one final assistant event, no parse errors. Isolated artifacts are in `/tmp/pk-subagent-smoke.yKIrmq/`; normal user config and sessions were not modified.
- Output-compaction support remains default-off. The published pilot is exploratory, not evidence of general superiority. TinyFish requires an API key; authenticated search is not yet tested.
- The existing 30-minute heartbeat now explicitly reserves the final stretch for validation, the edited Cmux/Cap video, and delivery by 08:00 EDT.
- New user finding: `/tools` misleadingly appears empty before the first prompt, then resolves. Backend and UI owners are fixing initialization and loading-state handling. Plugin command UI and ACP saved-session loading are ready for the next validation checkpoint.

### 2026-09-23 02:41 EDT — tool preview and plugin command checkpoint installed

- Installed clean GitHub revision `769d1e2`, release `20260923T064043.054390000Z-78a5114c66a9-8501b7b4`. Includes pre-prompt core tool preview with explicit integration deferral, `/commands` discovery/invocation/cancellation, quieter child plugin startup, compact child activity, and ACP saved-session loading.
- Isolated full Go race suite and vet passed; all 65 UI tests (298 assertions), typecheck/build and actual updater gates passed. An installed RPC smoke confirmed core tool preview before any prompt/model call. Existing user sessions were not interrupted.
- Audited retained Unreal runtime behavior in `docs/unreal-foundation-audit.md`. Existing real-runner independent-tool rendezvous test passed three repeats; async overlap is verified, comparative token savings are not.
- User feedback added task-only/no-file subagents, explicit dispatch constraints, bounded queueing, and concise structured results. Manager changes remain under review; event ordering and file-alias ownership limits require attention before shipping.

### 2026-09-23 02:52 EDT — task-only subagents installed; broader benchmark running

- Installed clean public revision `e5aba4a` as `20260923T064827.479283000Z-49b41587e672-dc23d223`. Full isolated Go race suite/vet and updater build gates passed. Adds explicit task-only scope, bounded FIFO queue (two active/eight waiting), queued cancellation/claim release, alias-aware advisory file ownership, ordered event delivery, and a Close event-drain barrier.
- One installed Luna/low run reproduced the user's three-child counting request without literal tool-flag hints. All three starts selected task_only and omitted files; all child session logs contain completed 1–25 enumeration, parent finished, and workspace stayed empty. Private artifacts: `/tmp/pk-subagent-count-smoke-20260923T025028-3860`. No model retries.
- Broader replay-compaction benchmark launched once from clean commit `226b07c`, with clamp/webhook, two repetitions, 90-second phase limits. Owner cache_runtime holds session 37111 and output `/tmp/pkbench-webhook-replay-rep2`; do not restart on observation timeout. Initial clamp holdouts pass; results are not final.
- User added guided skills.sh browsing/install, repository-based plugin installation, model self-configuration, Streamable HTTP MCP, OAuth, bearer/API-key auth, and Cloudflare setup compatibility. Owners are implementing vertical slices; do not claim these installed. Cloudflare's prompt was read as setup requirements, not executed against the user's account.
- Computer Use stopped after external user interaction closed the newly opened test workspace. Leave the user's Cmux alone while they are active. A reusable ffmpeg edit script is committed; actual Cap capture/final edit remains owed.

### 2026-09-23 03:18 EDT — guided installs, privacy opt-out, and next handoff

- Installed revision `184c6a0de9a2be6b35cb914983838ed5bfe1289f` as release
  `20260923T071743.415072000Z-06e2ed312728-3c8d66c3`. Root reports isolated Go race/vet, updater
  tests/build/activation, and installed pre-prompt catalog smoke passed. This release adds source-based
  `pk skills` and `pk plugin` management, Bun `DO_NOT_TRACK=1`, and initial MCP OAuth backend work.
  The new terminal manager UI flows and rich session operations are still in development.
- The installed privacy change opts Bun out of its documented anonymous crash reports. Keep claims
  limited to pk-controlled behavior; providers, MCP servers, plugins, and remote skill/search services
  have their own data handling. The parent is reviewing precise network, rollback, workspace access,
  and data-removal wording before the privacy audit is closed.
- Current checkout contains session list/archive/restore manager and RPC glue plus TUI history work.
  Batch operations must keep the active session safe, move data to recoverable trash, and have their
  concurrency/rollback tests reviewed before an install claim.
- The user’s larger goal remains an outstanding challenge: build a notably capable and efficient
  harness. Existing benchmark evidence is mixed/small and the output-cap probe was a no-op; no
  general speed/token advantage is established. Broaden matched runs and include quality and failure
  rates rather than optimizing a single token metric.
- Next in order: complete and validate the TUI skills flow; validate source plugin installation;
  finish MCP OAuth/auth fixtures and guided setup; integrate session history/bulk archive/restore;
  revise the privacy page after parent review; then use remaining time for freeze-safe GUI checks,
  benchmark follow-up, and local demo editing. Deadline is 08:00 EDT; report remaining work instead of
  claiming full completion.

## 03:40 EDT — history, integrations, streaming, and installation checkpoint

- `1964201` fixes `/history` in a fresh session and adds the interactive skills browser. `/history` loads the latest saved page; `/history older` follows the cursor. UI checks passed with 72 tests/341 assertions.
- `91b14c3` preserves image tool results in compatible Chat Completions requests, grouping parallel calls and keeping image parts out of tool-role messages. Local strict HTTP fixtures pass; remote-provider capability is not implied.
- `d9d7407` flushes visible streamed text during provider pauses within the 75 ms coalescing window, with serialized terminal/retry delivery and a barrier-controlled ordering test.
- `fd31672` exposes opt-in full/compact Bash-result context policy across foreground, RPC, detached tasks, and children. Default remains full; the existing mixed pilot does not establish general savings. It also adds asynchronous integration/session-management RPC routes.
- A full race run caught detached-task terminal writes regressing the durable event cursor. Terminal transitions now serialize with event appends; a deterministic regression and repeated race test pass. The subsequent isolated full race suite and `go vet ./...` pass. Updater installation of this checkpoint is in progress.
- User clarified with a screenshot that long-answer layout looks correct; the footer/composer contamination occurs in copied text. Selection handling remains under active investigation.
- User requested a root `install.sh` and README curl one-liner. A clean-directory installer test is in progress using separate install directories; do not claim it published until its commit and public URL are verified.
- WebSearch/WebFetch ship using TinyFish's documented free APIs, which require a key. Easy secure setup and authenticated live validation remain outstanding; no paid proxy or undisclosed fallback is used.
- Rich session panel, guided MCP/plugin setup, native attachment checks, further benchmark evidence, and real Cmux/Cap video remain required before the 08:00 report.

## 03:53 EDT — public installer verified; skill selection regressions

- `828b0d4` publishes the README curl installer. A clean-directory public-URL install into isolated paths succeeded, matched the committed script, and reported the exact clean source revision/hash. Existing user installations were untouched.
- `32be5bf` commits scoped transcript selection copying, keyboard/mouse session management with recoverable bulk archive/restore and explicit purge, plus cancellable asynchronous history loading. UI checkpoint: 80 tests/366 assertions, typecheck and build passed; full isolated Go race/vet verification is running. Not yet installed.
- User screenshots identify two skills browser regressions: Vercel candidate source `@main` was parsed as a skill selector, and Google Workspace Gmail search results opened the whole repository, hitting the 64-candidate limit. Backend and UI owners are fixing exact selected-skill round trips with real public-source checks.
- `e697977` adds the jobqueue fixture and immutable holdout checks. One bounded 8-phase Luna/low full-versus-compact cohort is running from that clean source in isolation; no additional runs authorized beyond the recorded cap.
- Official Monid CLI/skill setup succeeded. A hidden-input local credential helper is ready and the user has been asked to enter their key privately. No authenticated Monid free-search request has run yet.
- Guided MCP UI, plugin source picker, native attachments, launch capture/video, and morning report remain active work. Current installed revision remains `fd31672`.

## 04:02 EDT — skill fixes installed; Monid free route verified

- Installed clean public commit `fb6bdbb` as release `20260923T075956.819388000Z-7e9d5c1a5465-996834da`. Updater gates and installed PTY/RPC pre-prompt catalog smokes passed. UI suite: 86 tests/392 assertions. Existing user processes were left running.
- Selected skills.sh URLs now survive the UI→discovery→installation round trip; candidate URLs preserve the exact folder and encoded slash-containing refs. Real isolated installs passed Vercel React, Google Workspace Gmail, Anthropic academy-guide, and Superpowers brainstorming. Other Gmail variants encountered GitHub API rate limiting rather than a parser failure; errors now identify that condition.
- New build includes scoped transcript copying, rich saved-session management, guided MCP stdio/HTTP/auth setup, safe modal focus, masked/nonselectable secret input, and cancellable history. Native mouse/clipboard validation across terminals remains distinct from the passing headless regressions.
- User privately configured Monid. Official CLI metadata verified TinyFish search/fetch as zero-priced; one search and four pricing-page fetches both reported zero billing. A bounded Monid-backed pk adapter is now in development; the successful CLI test is not yet a pk integration claim.
- Matched jobqueue pilot completed all eight phases and four holdouts. Compact replay increased total input by 33.8%, with nearly unchanged uncached input and variable wall time. Default full replay remains unchanged. Results and exact source are published in `benchmarks/results/jobqueue-replay-e697977-20260923/`.
- Next: plugin source UI/ASCII welcome, Monid adapter, MCP echoed-secret/refresh-timeout hardening, ACP interoperability audit, native attachment checks, real Cap recording/edit, and morning delivery.

## 04:16 EDT — free search, protocol checks, and native UI findings

- `f27dba2` integrates Monid-backed TinyFish Search/Fetch, using the existing active CLI key without copying credentials. Fixed endpoint selection, explicit zero-price/billing fields, bounded output/waits, cancellation, and direct-key precedence have fixture coverage. A bounded live Go-adapter Search+Fetch returned results with zero billing.
- `ec3e984` and `cef44a1` redact echoed MCP credentials, including rotated OAuth tokens, before model/history output and make OAuth refresh respect cancellation. Local Streamable HTTP echo and stalled-refresh fixtures pass.
- `3dbc0f5` hardens ACP protocol negotiation, active-session replay leases, analysis filtering, and split JSONL delivery. `bae0a0d` preserves an official SDK 1.5.0 interoperability smoke with a fake runner; no real editor/model interaction is claimed.
- An isolated full Go race suite and vet passed at `cef44a1`.
- Native Cmux validation opened the installed MCP manager successfully in a separate workspace. Rapid `/mcp` typing plus immediate Enter initially selected the model menu; a live-text command fix is committed with the welcome/plugin checkpoint. Desktop actions stopped when external user interaction closed the test workspace and returned to the user workspace; no capture started.
- User requested the ASCII welcome and removal of top-header command links; `4b64e7e` restores a compact wordmark and plain pk header. Their MCP screenshot exposed contradictory Enter hints. Form keyboard/mouse improvements are being validated before the next install. Fresh `/new` welcome restoration and list arrow-key behavior also need explicit regressions.

## 04:22 EDT — usability update installed

- Installed clean public revision `0e6947c` as `20260923T082044.912526000Z-2e562af91f3a-66a2076d`; updater gates passed. `pk web status` confirms the configured `monid_cli` route with credentials hidden.
- Header is plain pk; ASCII welcome appears on initial launch and after `/new` completes. MCP choices use arrows, Tab follows visual field order, and Enter saves only from focused Save; mouse actions ignore right-click. Both saved-session and MCP lists recognize real terminal arrow names. `/plugins` now has an Add plugin source-entry form followed by review and explicit install. Full UI suite passed 94 tests/429 assertions.
- Installed PTY startup/catalog smoke passed after its ready-frame predicate was updated to the current composer/footer instead of the removed welcome sentence. The initial old predicate timed out despite the new ready screen being rendered; this was a test harness mismatch. Single startup measurement is diagnostic only.
- The active development pass now covers longer RPC interaction reliability, remaining selector UX, honest launch copy, and a reproducible real demo fixture. No Cap capture has started; native desktop work remains deferred while the user is actively using Cmux.

### 2026-09-23 08:30 UTC — native coding run and capture validation
- Installed 0e6947c ran a real Luna/low coding task in disposable `/tmp/pk-demo-fixture.AsxZqh` through Cmux. It fixed inclusive interval counting, completed its response, and returned Ready. Independent `go test ./...` passed; CLI returned `{"covered":5}`.
- Committed cancellation/second-prompt regression and reusable fixture in 011d765; focused race checks passed. Launch/privacy/checklist updates in 51a8e31 pushed.
- Cap accepted Cmux window ID 31835, but contact-sheet inspection showed Codex instead. Rejected and removed that entire disposable capture/export/edit; no video delivered or uploaded. Investigating target selection before any recapture. This is a capture failure, not a coding-task failure.

### 2026-09-23 08:31 UTC — native Finder paste and image interpretation
- Copied generated `mint sample.png` in Finder with Cmd+C, then Cmd+V into the dedicated Cmux demo session. pk queued the single spaced-path file and displayed its attachment chip.
- Sent a brief description request. Saved session 03872f2a records ViewImage on the exact file and a final answer describing the mint vertical rectangle against a dark background. This verifies Finder-file Cmd+V through the installed Cmux/pk route and real image interpretation; raw pixel clipboard and native drag/drop remain separate checks.
- Provider model lookup race fix e9821c4 pushed; 96 UI tests/434 assertions, typecheck/build pass. GitHub updater running against this checkpoint, existing sessions left intact.
- Installed e9821c4 verified with `pk version`: clean GitHub source, release `20260923T083151.202063000Z-9180ffa6da58-0d042a65`. Existing demo session displayed Update ready without interruption.
- Cap 0.6.0 source investigation found window targeting crops the display region, so foreground occlusion caused the rejected capture. Raising the dedicated Cmux window through Computer Use then taking a Cap still on window 31835 produced the correct pk view (inspected). Next recording must keep this window frontmost and validate initial footage.

### 2026-09-23 08:36 UTC — allocation fix and capture retry
- Session preview normalization now stops at the required prefix; Unicode/invalid UTF-8 differential checks and race suite pass. Synthetic 32×128KiB session listing measured 27.3% fewer allocated bytes in one five-iteration run. Reproduction and limitations in docs/performance.md; committed 7436784/d85b6c7.
- Full UI suite now 97 tests/441 assertions, including repeated-turn/overflow/resize keyboard+mouse recovery. Original reported native freeze remains unreproduced.
- Second real coding run in /tmp/pk-demo-fixture.4do6j3 passed independent tests and CLI output5. Capture showed generated Finder test window occluding Cmux despite clean Cap still. Rejected/removed second take and edit. Closed only the generated Finder window; next capture must validate an actual short recording, not rely on still behavior.

### 2026-09-23 08:40 UTC — first reviewed local demo delivered
- Cap short recording confirmed no occlusion, then real Luna/low session in /tmp/pk-demo-fixture.BFypqp fixed the Go inclusive-range bug. Independent tests and README CLI output5 passed.
- Reviewed original and edited contact sheets plus title/outro frames. Final local video: /Users/preetham/Movies/pk-launch-demo/pk-coding-demo.mp4 (44s, 1080p30, 552306 bytes). Cap project, source export, and reproduction notes retained alongside it; no upload. This is the initial focused coding demo; broader feature montage/polish remains possible.
- Installed f6d5783 clean GitHub release 20260923T083731.814932000Z-7fb8926ae1f2-039f1af3 includes session preview allocation improvement.

### 2026-09-23 08:42 UTC — raw clipboard image validation
- In Preview, selected all pixels of the generated mint PNG and copied with Cmd+C. Cmux Cmd+V produced a `clipboard-2026-09-23-044040-7E440986.png` attachment rather than the Finder source path.
- Session 7c1a2e81 records ViewImage on that clipboard PNG and a final answer accurately describing the glowing mint rectangle. This verifies the Preview→Cmux→pk raw-image path for this terminal, not every terminal's clipboard protocol. Native drag/drop remains pending.

### 2026-09-23 08:45 UTC — scope audit and ImageGen gap
- Auditing actual startup exposed ImageGen as one-shot only, contrary to intended TUI tool availability. Luna backend/UI agents are implementing explicit persisted opt-in plus a keyboard/mouse setting; separate image driver will remain distinct from chat model and disabled by default.
- Native Finder drag attempt produced no queued file; coordinate interaction did not establish a successful cross-window drop. Kept native drag/drop unverified rather than inferring behavior from parser tests.

### 2026-09-23 08:52 UTC — real screenshot and async runtime proof
- README now uses the reviewed real coding-demo frame (e2f6cc7), pushed to main. Concept assets remain labeled separately.
- Added a barrier-controlled runtime regression proving two Bash operations overlap, running events are emitted before release, and the model receives the first result with a still-running placeholder for the second. Final request contains both outputs; model calls remain serialized. Luna reviewer ran ten race iterations; root independently reran successfully. This is behavioral evidence, not a speed or token-savings benchmark.
- MCP form's six focused UI tests pass; installed f6d5783 already has context-specific hints and Enter saves only on the focused Save action.

### 2026-09-23 — ImageGen interactive configuration checkpoint
- Pushed 616102f: persisted opt-in ImageGen settings, `/image` keyboard/mouse UI, fresh tool preview, and saved-driver snapshot protection. Chat model is unchanged. Updated bundled pk skill and guides.
- Root full race run passed all packages except two old fixtures; corrected fixture version and renamed the generic missing-tool fixture away from the reserved ImageGen builtin. Final affected-package race run passed cmd/pk, config, runner, integration, and skills. Full UI suite passed at 98 tests; three additional interaction regressions (late response after Escape, retry after error, mouse) also pass. Escape regression emits a React act warning; no failing assertions.
- GitHub-source install completed: release `20260923T085707.844387000Z-2757d0f03172-f6baad42`, clean revision `616102f`. An isolated installed-RPC smoke verified disabled default → enable + new exposes ImageGen → disable + new removes ImageGen. No model requests, paid image generation, or user configuration changes were made.
- Luna benchmark agent is preparing a bounded counterbalanced low-vs-medium effort pilot with full context; production effort default remains unchanged.

### 2026-09-23 09:00 UTC — native reload and settings review
- Computer Use reloaded the existing demo session `7c1a2e81` in Cmux workspace 2 onto the installed release; its messages and composer returned. User workspace 1 was untouched. `/image` opened correctly and Escape closed it without changing settings.
- Native review found two polish gaps: selector label/status text visually runs together, and resumed history omits tool rows while exposing the attachment's model-input wrapper. UI agent is correcting selector spacing; reviewer is tracing history fidelity. These remain open until verified.

### 2026-09-23 09:08 UTC — interaction fixes and effort pilot result
- Committed/pushed selector label separation (`097212c`) and mouse session reopening (`af07f05`). Root reran six ImageGen and eleven session-manager interaction tests successfully. These UI polish changes await the next coherent install with history restoration.
- Effort pilot process completed once, exit 0, all 16 phases and eight verification holdouts passed. Low effort showed 4.3% fewer total input+output tokens but 9.0% more uncached input; task-level direction differed. This does not meet the preset 10% token reduction threshold, so defaults remain unchanged. Benchmark owner is fixing stale formatter prose and preserving exact run-source metadata without rerunning.

### 2026-09-23 09:20 UTC — installed history and mouse checkpoint
- GitHub-source installation activated clean `347a609`, release `20260923T091845.240980000Z-72ecea148b19-6e5f9c7d`, after 110 UI tests / 485 assertions. The first staging attempt failed a repaint-sensitive MCP Cancel assertion and left the previous release intact; waiting for the actual rendered state fixed the test, with ten focused repetitions passing.
- Computer Use reloaded only the owned Cmux demo session. Grouped Bash history and ViewImage history returned; Ctrl+O expanded saved ViewImage arguments. User workspace 1 was not touched.
- Committed `30400c7` bounded assistant preview assembly. Synthetic helper-only benchmark reduced allocated bytes from ~2.76 MB to 16 KB; upstream full-log decoding remains and this is not an end-to-end or token savings claim.
- Latest user MCP screenshot has obsolete contradictory Enter hints. Installed code already uses focus-specific hints; a follow-up keyboard/mouse audit is underway. Attachment provenance remains in progress; legacy history is intentionally unchanged without verifiable metadata.
- Native MCP follow-up: on installed `347a609`, opened Add, changed to remote HTTP with Right, tabbed to Save, and verified the Save-only Enter hint plus green focus. Canceled without saving. The user's contradictory screenshot predates this installed hint fix. A bounded follow-up improves Enter navigation and field mouse focus.

### 2026-09-23 — coherent MCP focus and saved attachments
- `c680a0b` gives MCP Enter one focused action: selectors change, text fields advance, Save submits. Mouse clicks synchronize field focus, including masked credentials; contextual hints and modal visibility match behavior. Full UI suite: 113 tests, 508 assertions; TypeScript passes.
- `1a5a614` adds bounded attachment presentation records keyed to durable input IDs, verified against typed-prefix and effective-payload hashes. New foreground attachment prompts restore typed text and filename/type/PDF chips; legacy or mismatched records keep full original input. Archive/restore/purge include these records. Model inputs and cache prefixes are unchanged. Steering attachments are not supported by this pass.
- Affected Go race suites pass after correcting the new large-prompt test to account for the existing ellipsis marker. History metadata is included in entry/page display budgets.

- GitHub-source install activated clean `2e365e8`, release `20260923T093118.840648000Z-7bd1d5efc551-b59e58a0`. Native Cmux reload returned the owned demo; Enter on Connection type switched to Remote Streamable HTTP without saving, with the matching focused hint. Canceled the form without configuration changes.

### 2026-09-23 09:37 UTC — stream failure ordering and reliability audit
- `3dbd31e`: reproduced a delayed assistant delta arriving after `request_failed` because the error path bypassed timer cleanup and serialized delivery. Changed it to the existing failure-event path; failing-before/passing-after regression plus the modelstream race suite pass. This is not evidence that the unrelated native freeze is fixed.
- Native `/new` on installed `2e365e8` shows the mint ASCII wordmark, plain pk header, welcome actions below the wordmark, and bottom activity row. No stale starting-session text remained.
- Luna lifecycle audit found no reproducible native-freeze cause; added a simulated silent-gap/steer/cancel/follow-up regression. It covers UI event state, not minute-long provider latency or native terminal input.
- Upstream session-store audit confirms no public one-pass API combines snapshot metadata and transcript items. `Inspect` and `Items` each decode/replay the log; avoiding that safely needs an upstream combined API or carefully invalidated metadata index. No custom replay decoder introduced.
- A bounded two-fixture, two-repetition current-pk versus pinned-Unreal comparison is being prepared, with matched Luna/low settings, 90-second phases and 15-minute overall cap; no repeated paid runs are authorized for this cohort.
- Installed-launcher attachment smoke also passed on clean `2e365e8`: one isolated local fake-provider request, unchanged provider payload, typed text and filename chip restored after RPC restart, 308-byte content-free sidecar. External model calls: zero.
- GitHub-source updater activated clean `0069ac3`, release `20260923T093828.901078000Z-4db46f2885f4-ee1e45e0`, including the failed-stream timer fix. Running user sessions were not interrupted.
- Current live comparative cohort is owned by Luna reviewer, exec `53227`, output `benchmarks/results/pk-unreal-clamp-intervals-low-2rep-20260923`. Do not restart it or create a duplicate. Parent flagged overlap with updater validation as a wall-time confound; fixed engine ordering also needs disclosure.
- Luna extension agent is implementing additive negotiated tool-progress notifications with bounded per-call output; RPC/UI wiring remains to be reviewed. Do not mark progress streaming delivered until integration tests pass.

### 2026-09-23 — completed matched baseline
- Luna-owned exec53227 completed once: 16 phases, eight passing holdouts, 2m15s overall. Result committed `3cac34d`; root independently verified all 331 preserved source-file hashes. pk input 43,889 (28,017 uncached), output 1,752; Unreal input 20,011, output 1,333; both 22 responses. No general savings or speed claim. Shared-machine checks and fixed ordering confound wall time.
- Prompt/tool overhead analysis is now assigned to Luna using local captures only. Extension progress package, RPC bridge, and TUI are under integration; do not mark released yet.

### 2026-09-23 09:50 UTC — negotiated plugin progress installed
- `90a04f9` adds negotiated bounded plugin progress, worker-process correlation, asynchronous queues, common-credential/control sanitization, RPC relay and compact active-tool updates. Late/wrong-turn updates cannot revive finished rows. Full UI suite 115 tests / 522 assertions; extension and cmd/pk race suites pass, including final-result delivery from an immediately exiting worker and cancellation under progress flood.
- Installed clean `71170b9`, release `20260923T095015.036503000Z-a250ac420c82-7bb96df1`. Local installed real-worker/fake-provider smoke is assigned to cli_v2. Existing workers remain compatible; progress is opt-in and never enters model context or saved history.
- `/usage` read-only session accounting is in development, with partial/unavailable reporting rather than inferred zeroes. No provider/model/default policy changes.
- Installed `71170b9` full pipeline smoke passed: real JSONL subprocess worker negotiated progress, emitted a correlated update, waited 300 ms, and returned `PLUGIN_RESULT_ONLY`. The installed RPC emitted progress before the terminal tool event; a localhost fake provider's next request contained only the tool result, with no progress text. Two localhost requests, zero external calls, isolated temporary home/workspace removed. This validates the installed protocol path, separate from native terminal rendering evidence.

### 06:00 EDT — session usage development review

- Developing `/usage` as a local read of durable provider counters, with per-metric coverage and no model request or estimated dollar savings. UI typecheck and initial backend usage tests pass; not installed yet.
- Review identified explicit JSON null being treated as zero, lost zero/missing distinctions in Chat Completions normalization, and repeated full-log decoding from paging the pinned store. Backend owner is addressing all three using the existing validated store API.
- Independent review found close-during-load should cancel the backend operation as well as the UI spinner; backend/UI owners are adding correlated cancellation. An isolated fake-provider smoke is prepared and waiting for the corrected normalization, with no external requests.
- Completed validation: 120 UI tests / 551 assertions, TypeScript check, cmd/pk race suite and provider race suite pass. Targeted cancellation/null/overflow/long-history tests pass. A fresh local fake-provider run persisted 137 input, 11 output, 21 cached, 116 uncached tokens; restart/attach returned the exact counters and coverage=1 without increasing the single provider request. No external inference requests were made.
- Installed clean GitHub revision `097bab8` as release `20260923T100519.306848000Z-1a99c9a8a1e5-3dec9a09`. The installed launcher passed the same isolated restart/attach usage smoke: exact 137/11/21/116 totals, coverage 1, one localhost provider call before and after the lookup, zero external calls. Native Cmux panel inspection remains separate from these protocol and headless rendering checks.

### 06:08 EDT — native usage review and next efficiency experiment

- In owned Cmux workspace 2, attached the saved demo session, reloaded revision `097bab8`, and opened `/usage`. The native panel showed six recorded responses: 24,434 input, 495 output, 14,848 cached input, 9,586 uncached input, all coverage 6/6. Escape closed the panel. No model call was made; user workspace 1 was untouched.
- Native review found fresh-session `/reload` refuses before any conversation exists despite showing Update ready. Backend/UI owners are implementing a fresh restart that preserves configuration without requiring a model request.
- Committed `04503a3`: opt-in aggressive replay-compaction benchmark profile (1,024-byte threshold, 256-rune excerpts) with source metadata and scoped environment overrides. Production defaults remain unchanged. Focused benchmark tests and tagged override-isolation regression pass. A single bounded matched cohort is assigned; results are pending and this is not yet evidence of savings.

### 06:20 EDT — native skill installation and compaction results

- Native Cmux `/skills search react` → source review → installation succeeded for `vercel-react-best-practices`, and `/new` → `/skills available` showed the managed skill. `pk skills list` independently confirmed its installed source/path. No model call was required. The skill remains installed as requested; existing session snapshots were not rewritten.
- The native review also exposed duplicate source/description text, stale installation notices after `/new`, and overlong skill list descriptions. UI owner has focused fixes/tests ready. Monid's folded YAML description appeared as `>`; a bounded metadata parser correction is in development.
- Aggressive replay cohort completed all 16 phases in 7m55s with all four holdouts per arm passing. Aggressive input 544,819 vs control 127,054; uncached 97,331 vs 55,374; output 10,448 vs 4,704; responses 90 vs 25. No reruns. Keep full context default; no broad efficiency claim. Full results and exact 335-file source snapshot retained.
- Installed clean revision `0436669` as release `20260923T102151.628967000Z-579be7d11287-2b400681`. Native Cmux fresh-session `/reload` restarted successfully without a prompt; the new process retained `/private/tmp/pk-demo-fixture.BFypqp`, `gpt-6-luna`, `low`, and explicit `native` provider. The welcome screen returned with no refusal. User workspace 1 was untouched.

### 06:34 EDT — PDF previews and skill metadata validation

- Added bounded optional Poppler previews for scanned/image-only PDF pages, including mixed documents. Images use private session/input-owned storage and survive resume/archive/restore; purge removes them. The real local renderer smoke decoded page 2 and verified that lifecycle without external calls.
- Replaced line-based skill metadata interpretation with bounded YAML decoding before default registry registration. Folded descriptions and quoted names now match in the catalog, SkillUse registry, and model context; malformed optional skills warn/skip and saved sessions retain their captured metadata.
- All 123 UI tests/562 assertions pass. Affected Go race packages pass. A full Go run caught the temporary real-PDF smoke using macOS’s /var alias; the caller now canonicalizes the root while retaining managed-directory symlink checks. Final clean run/install follows.

### 06:37 EDT — PDF and metadata installment installed

- Clean GitHub revision `b5aa90b` activated as `20260923T103610.264877000Z-e6a9b9b1a6d2-34126c4e`. Full Go suite and all 123 UI tests passed; affected Go race suites and opt-in real Poppler test passed.
- Native Cmux reload returned the owned fresh session with its ASCII welcome. `/skills available` now shows Monid’s actual folded description rather than `>` and compact skill descriptions. No model requests were needed. The user’s separate workspace was untouched.
- Installed one-shot smoke used an isolated home/workspace and one localhost fake-provider request. Its PDF prompt referenced the rendered page and the folded skill description reached both request and saved snapshot; root independently verified the persisted 8,089-byte PNG is mode 0600. The initial inspection script used the wrong snapshot filename, then verified the correct SHA-256-named snapshot. No external model call was made.

### 06:40 EDT — public installer and startup checkpoint

- Downloaded the public raw GitHub `install.sh` and ran it with isolated PK_BIN_DIR/PK_LIB_DIR destinations. It built clean `3fe1908`; the resulting launcher reported that revision. The normal installation and running sessions were untouched.
- Fixed the startup measurement PTY child to use its intended temporary workspace. Installed b5aa90b reached its ready frame in about 0.5 seconds in this small shared-machine run; RPC catalog warm median was 10.5 ms. Raw results and reproduction are in `docs/performance.md`. No model calls.

### 06:46 EDT — reliability fixes and live PDF vision

- `ba9cee7` fixes an unlocked subagent active-count read found by race testing; cancellation tests verify final child events drain before Close returns. `d1540d5` rotates MCP OAuth credential references on logout so stale token refreshes cannot repopulate them. Already-connected runs may retain in-memory access until they end, as documented.
- `32f3160` memoizes transcript grouping and skips static timeline reconciliation on activity-only ticks while preserving live timers. All 124 UI tests/568 assertions pass. No end-to-end speedup percentage is claimed.
- Installed b5aa90b passed a real Luna/low scanned-page inspection: one ViewImage call, correct red-square description, two provider responses, 6,974 input/169 output/1,536 cached input tokens. The real-renderer fixture was corrected to use exact PDF stream lengths and now asserts nonwhite expected pixels.
- The large-output replay profile is benchmark-only. Preflight cannot prove any eligible 16KiB output in existing tasks, so no paid cohort was run merely to exercise a new setting. Production full-context default remains unchanged.

### 06:49 EDT — reliability installment installed

- Clean GitHub revision `8d2217d` activated as `20260923T104825.645608000Z-f78554af8afa-93f7a64a`, including OAuth logout invalidation, the subagent race fix, and static-transcript clock optimization. Full Go suite, affected race tests, and all 124 UI tests passed.
- A further native Finder-to-Cmux automated drag attempt did not produce a queued file. This is not proof of a parser failure because cross-window drop delivery was not observed. Keep native drag/drop unverified and recommend the verified paste or `/file` routes.

### 07:02 EDT — MCP form and session-list checkpoint

- MCP form choices use arrows; Enter advances to the next control and only saves when Save is focused. Save and Cancel are keyboard-focusable and clickable, with contextual hints. OAuth result notices survive catalog refresh. Focused keyboard/mouse tests pass.
- Session-list summaries now have a bounded in-memory cache with log/context identity checks before and after reading; active status stays fresh. Root measured 9.83 ms/op including the first miss and 1.90 ms/op warmed over five iterations on Apple M3; the earlier uncached baseline was 28.64 ms/op. These are local fixture timings, not token savings.
- AgentMail opaque identifiers are preserved exactly within declared limits; oversize identifiers return explicit errors instead of unusable truncated IDs. Fixture tests and affected race suites pass; no private email bodies were read.
- Lifecycle observers remain uncommitted while a process-worker timeout isolation issue is fixed and tested. They are excluded from this installation.

### 07:05 EDT — MCP form installment installed

- Clean GitHub `cada2b5` activated as `20260923T110343.361921000Z-6ee8c5cff48d-5ca53084`. Native Cmux reload, remote-mode arrow selection, and Enter advancement to Server ID were verified without saving configuration or calling a model. Existing user sessions were untouched.
- The first full UI run had 124 passes and one obsolete wording assertion; that assertion was corrected. The managed updater then passed the complete Go and UI suites plus typecheck/build on its clean checkout before activation. Affected Go race checks also passed. The next install is reserved for separately reviewed lifecycle observer work.

### 07:13 EDT — extension observers installed

- Clean GitHub `3a1e36c` activated as `20260923T111231.073481000Z-38a3fdba468d-e2ae95da` after the managed updater passed full Go/UI suites, typecheck and build. Affected extension/runner/CLI race tests also passed.
- Opt-in `run_start`, `response_complete`, and `run_end` notifications expose metadata only. Process workers use bounded one-way delivery; slow observers no longer time out the tool response channel. Shutdown queue accounting and invalid feature negotiation have regression coverage. Delivery remains best effort, not an audit-log guarantee.
- The run-observer example passed a real subprocess handshake/event smoke without a provider request. The configured AgentMail checkout worker was rebuilt to include the exact-ID fixes, without reading private email or interrupting existing workers.
- Next active checks: composer focus after selecting/copying responses, and redundant validation reads on cold session attachment. Overall benchmark superiority remains unproven.

### Next checkpoint — copy focus and cold session lookup

- `Manager.Get` now reuses its first validated snapshot instead of decoding the log a third time. Cache eligibility is captured before Inspect and checked again after Items, so concurrent file changes cannot populate the cache using a newer baseline. Race tests pass. The 128 KiB cold fixture measured 1.53 ms and 1.42 MB per lookup in root verification versus 2.31 ms and 2.10 MB before; allocation count rose from 312 to 372.
- A renderer regression reproduces loss of composer focus after a delayed response and transcript copy; the proposed fix restores focus only when no setup modal owns input. Root's native pre-fix check on a restored Cmux demo session accepted typing after copy, so this does not establish the original full freeze's cause. No provider request was made for that native check.

### 07:24 EDT — copy focus and history lookup installed

- Clean GitHub `b06e1ab` activated as `20260923T112302.648900000Z-ed0d042e12b6-5fe9c087` after complete Go/UI tests, typecheck and build. Sessionmanager race tests passed.
- The delayed-response/copy regression failed twice without restoration and passed with it; a separate rendered-modal test preserves picker focus. Native Cmux reload restored the saved demo history, drag-selection reported a copy, and a subsequent physical `x` key appeared in the composer. The draft was cleared without sending a prompt. This verifies that path, not a general claim that all freezes are solved.

### 07:32 EDT — final reliability audit in progress

- The durable checklist now points to installed `b06e1ab` and preserves unresolved requirements rather than presenting older releases as current.
- `6eb9fe3` makes concurrent subagent shutdown callers wait for child/event drain. A held-callback regression and affected race suites pass; root added a cancellation barrier and failure cleanup to the test.
- `91361d1` rejects Chat Completions streams that end successfully with no assistant content or tools. Explicit refusal/output-limit outcomes retain their status and usage, and providers emitting useful content without a finish reason remain compatible. Local provider fixtures and affected race suites pass. This does not diagnose the user's earlier native Codex waiting symptom.
- A separate TUI regression is checking whether global attachment paste intercepts paths meant for setup fields or AskUser answers. These new code changes are not installed yet; the active release remains `b06e1ab`.

### 07:36 EDT — reliability installment installed

- Clean GitHub `264b3c7` activated as `20260923T113445.615501000Z-35dab041a348-3590899b` after full Go/UI suites, typecheck and build. Affected provider/subagent/helperregistry race tests passed.
- Native Cmux reload restored the saved demo transcript. In `/mcp` → Add → Executable path, pasting `/usr/local/bin/server` appeared in the focused field, with no queued attachment. The Computer Use paste call reported a clipboard-read timeout, but the subsequent screenshot verified the complete value. The form was canceled without saving configuration or calling a model.
- Renderer regressions also verify AskUser path answers remain literal text and normal chat file paste still queues attachments. Both new tests failed without the guard and passed with it.

### 07:47 EDT — native clipboard routing and catalog cleanup installed

- Clean GitHub `35d448a` activated as `20260923T114652.417884000Z-0faf9fd0486c-250f55df` after full Go/UI suites, typecheck and build. Focused runner race tests and eight paste/copy regressions also passed.
- Native clipboard replies are bound to their requested input, question and session. Stale replies do not queue attachments or insert text into a different question/session. AskUser path answers remain literal text. The earlier bracketed-paste form fix remains intact.
- New empty-catalog sessions omit an unavailable SkillUse declaration (244 serialized UTF-8 bytes in the capture). Existing saved tool lists are preserved. Normal installed sessions have the bundled pk skill and therefore retain SkillUse; no general token saving is claimed.
- A local startup check of `264b3c7` measured a 500 ms first TUI ready frame and 498–502 ms on two subsequent launches, without provider requests. The new trace attribution script reproduces observed pk/Unreal usage and request growth but cannot reconstruct omitted full request components. Evidence links were repaired and checked.

### 08:00 EDT — morning handoff

- Installed production release remains `35d448a`, fully tested. The final source-only benchmark addition provides opt-in count/byte instrumentation with response/usage correlation; it does not save request content and has not been used in a paid/live cohort. Unreal component measurements remain unavailable.
- Wrapper, benchmark-driver, and tagged CLI hook race tests pass. Root preserved the untagged build for ordinary benchmark runs and made zero-response metric sets unavailable rather than a successful capture.
- Morning deliverables: docs/overnight-report.md, the complete checklist, measured benchmark/performance records, launch artwork gallery and draft X posts, and the local reviewed Cmux/Cap coding video. No social posts or private emails were sent.
- Not all requested work is complete. Comparative superiority, general cost savings, original freeze diagnosis, native drag/drop, Cloudflare account OAuth, real-editor ACP and broader provider interoperability remain open. The overnight schedule is paused after this handoff; the overall improvement goal remains unachieved.

### Post-handoff — benchmark capture validation

- Hardened optional benchmark metrics ingestion: typed records reject extra content and negative counts; complete coverage requires matching usage response IDs and contiguous request ordinals. Failed requests remain unavailable. Ambient benchmark policy/replay settings are removed before child launch.
- Added real runner-hook and cancellation coverage plus a fake-child driver test; affected Go race tests and the tagged runner-hook test pass. No paid provider calls were made.
- Attribution summaries exclude sidecar files and report component JSON-value bytes with explicit overlap caveats. A local duplicate-sidecar fixture passes; historical pilot totals remain unchanged. Production install remains 35d448a because this installment affects benchmark tooling only.

### Post-handoff — full CLI capture smoke

- Added a reproducible local SSE provider smoke for the benchmark-tagged CLI, with temporary PK_HOME, workspace and provider config. One request passed through the actual run/provider/runner path; nine tool declarations matched the HTTP request.
- Verified explicit zero cached-input availability, response correlation, 0600 capture permissions, and absence of prompt/response text in metrics. All usage numbers are synthetic; no live model request or cost-improvement claim.

### 08:12 EDT — live request-component diagnostic

- Bounded Luna-low capture completed clamp and intervals with both holdout checks passing. All 11 responses correlated; totals 29,717 input / 13,824 cached / 774 output tokens. This was pk-only, one repetition, not a comparative gain.
- Large tool-result growth followed Git usage errors in non-repository fixtures and persisted on resume. Saved count-only captures, sanitized traces and analysis under benchmarks/results/context-capture-luna-low-20260923. Next experiment should target unnecessary Git-error context while preserving useful errors and real-repository behavior.

### Post-handoff — workspace Git awareness candidate

- Fresh sessions now receive a concise not-inside-work-tree fact only after a bounded Git probe confirms it. Missing Git, malformed metadata and timeout remain unknown; inherited Git discovery variables are excluded. Bare repositories are described as not inside a work tree.
- Runner race suite passes. Independent regression failed before the change and now verifies a resumed prefix remains byte-identical after git init while a fresh session updates. Five local probe iterations averaged 12.74ms on M3 (small shared-machine sample); timeout is 500ms plus bounded process drain.
- This candidate has not yet established lower token usage and is not installed in the production release.

### Post-handoff — Git-awareness live diagnostic

- Candidate b7a93a2 passed both holdouts and avoided failed Git commands. One-repetition sequential comparison: input 29,717→21,736; uncached 15,893→14,056; output 774→755; responses 11→12. Total wall time worsened; cache hits differed. These preliminary results do not prove general savings.
- Saved candidate captures and tradeoff analysis under benchmarks/results/git-awareness-luna-low-20260923.

### 08:21 EDT — Git-awareness installment installed

- Managed GitHub updater activated clean 2da4bc0 as 20260923T122106.062697000Z-9553ed49507a-fd890b40 after its validation gates. pk version confirms the source revision. Existing running sessions were not interrupted. Relaunch/reload and start a fresh session to receive the workspace advisory; rollback remains available.

### 08:23 EDT — native input recheck

- Opened a separate Cmux workspace, leaving the user workspace untouched, and launched installed 2da4bc0. Confirmed mint ASCII wordmark and compact header. In /mcp Add, Right selected remote HTTP and Enter focused Server ID with the matching next-field hint.
- Escape canceled the form and then the manager. Physical x, Shift+Enter, y appeared as two composer lines without submitting a prompt. No provider request or MCP configuration write occurred.
- Renderer audit exercised long-gap typing/copy and cancel/second-prompt paths without finding the original freeze. Additional focused-form/turn-completion regression is retained; original native freeze remains undiagnosed.
- The expanded keyboard-only form regression passes (six assertions): field sentinel stays out of chat, form and manager cancel, composer regains focus, and two prompt payloads remain correct across turn completion. Initial failures were stale-frame assertions and the renderer’s 20ms lone-Escape parsing delay; no application defect was established. One React act warning remains in the test output.

### 08:37 EDT — updater dispatch and progress installed

- Fixed real RPC /update dispatch passing the command name into a flags-only handler. New regression failed with unexpected arguments before the fix and now reaches source validation. CLI update emits readable stage progress to stderr and the current stage on failure; post-build UI validation is now labeled accurately.
- Affected updater and CLI/RPC race tests pass. Managed updater activated clean be4bf3a as 20260923T123657.052406000Z-5aad47beba25-33d3a5aa after its full validation gates. Installed binary smoke verified CLI progress and actual RPC dispatch against an isolated invalid source; no secondary installation or network access was needed for that smoke. Existing running user sessions were not interrupted.

### ACP configured-provider integration

- ACP now uses the configured provider or explicit `--provider ID|native`, with provider model/effort defaults and explicit flag overrides. Provider identity is persisted with the session.
- Official SDK 1.5.0 smoke now drives the real CLI against a local Chat Completions fixture for two turns, checking model, tool declarations and retained history. It passed without a remote model request; actual editor integration remains unverified.
- The neighboring RPC command dispatch audit found no further argument mismatches. MCP form checks pass (nine tests, 53 assertions).

### 08:50 EDT — ACP provider installment installed

- Managed updater activated clean c796f96 as 20260923T125009.244964000Z-90fefa436666-685f3407 after Go/UI validation and build. Existing running sessions were not interrupted.
- A bounded renderer diagnostic with four 32 KiB Markdown responses and four plain responses preserved composer visibility through resize and wheel scrolling. The initial sample was slower for Markdown, but ordering, warm-up and accumulated content differ; this is not a comparative benchmark or reproduction of the original freeze.

### ACP first-prompt and rendering follow-up

- Empty pre-created sessions now receive their initial context without a misleading resume warning. Existing history with a missing snapshot still warns and refuses captured-output compaction. Full runner and CLI race suites pass.
- Replaced the first large-transcript diagnostic with fresh renderers, prewarmed parsers and alternating format order; retained full content. Initial Markdown work remains more expensive, while unrelated updates were similar. No clipping or freeze reproduced; timing details and scope are in docs/performance.md.
- Official SDK smoke now passes restart/load of the actual CLI, replaying both user and assistant history and continuing with a third provider turn. This verifies the SDK path, not a particular editor.

### 08:58 EDT — ACP continuity checkpoint installed

- Managed updater activated clean 8075acd as 20260923T125814.937959000Z-33a2ccfc6892-73392029 after all validation gates. Fresh ACP sessions no longer report a misleading resume warning. Existing running sessions were not interrupted.
- Saved the ACP image-input boundary and required artifact lifecycle work in docs/research/acp-image-inputs.md. It is a proposal, not implemented image-block support.

### Delegation accounting and schema experiment

- Headless CLI now forwards allowlisted child lifecycle/usage JSONL through the same synchronized writer as parent output; child transcript content stays private. Unknown provider usage is emitted explicitly, including tool-only responses. Malformed child counts mark accounting unavailable.
- pkbench retains parent-only columns and adds deduplicated child/combined totals with coverage flags. Missing/partial usage and dispatcher calls without child accounting cannot appear as free delegation. Parent component-capture correlation is unchanged.
- Added a benchmark-only flat Subagent dispatcher with all five actions, conditional validation and stateless result decoding across resumed registries. Actual definitions measure 2,124 → 1,317 JSON bytes. It is not enabled by a CLI policy or production defaults; no live savings claim.
- Full runner/CLI race suites, pkbench/experiment race suites, tagged CLI tests and the additional dispatcher-accounting regression pass. No live provider calls were made for this checkpoint.

### 09:18 EDT — delegation accounting installed

- Managed updater activated clean f483eee as 20260923T131805.235416000Z-25ff380ac87a-cd00bf4b after full validation/build gates. Existing running sessions were not interrupted. The flat dispatcher remains experiment-only; production still exposes the five established subagent controls.

### 09:35 EDT — live delegation schema pilot

- Pushed benchmark driver and tagged policy wiring in 53f2478. Tagged CLI and benchmark package tests passed; Luna agent's benchmark race suite passed. Verified generated sources are archived before fixture cleanup.
- Ran one baseline-first paired Luna/low trial, bounded to 120 seconds per arm. Both arms completed two children with complete input/output accounting and passed both pristine holdouts.
- Combined input: baseline 26,171, dispatcher 20,239; cached input: 9,216 vs 3,072; output: 1,573 vs 1,385; elapsed: 38.587 vs 29.649 seconds. Uncached input was slightly higher for dispatcher (17,167 vs 16,955), so no cost-saving claim or production promotion.
- Results, source identity, sanitized traces and generated implementations: benchmarks/results/subagent-schema-pilot-20260923a/. Next evidence needed: reverse-order repetitions and lifecycle operations beyond start/wait.

### 09:45 EDT — compact MCP mouse controls installed

- Installed clean 1fbd1cd as 20260923T134349.857906000Z-cc4d08d2b46c-9a9c6998 after managed Go/UI validation and artifact build gates. Existing user sessions were left running.
- Connection and authentication selectors now have explicit previous/next mouse buttons using the same behavior as arrow keys. Eleven renderer tests cover navigation and an 80×24 longest-auth form with visible Save/Cancel and ignored right-click.
- Updated clipboard help to describe the native copy path, and removed stale current-release labels from the checklist.
- A bounded Luna audit found no additional clipboard/modal/turn-completion focus bug. The original native freeze remains unconfirmed; this installment does not claim to fix it.

### 09:59 EDT — ACP inline images installed

- Installed clean b263a48 as 20260923T135842.276791000Z-69cbb8dd8335-343d5080 after full managed Go/UI/build validation. Installed `pk acp` initialization confirms image capability. Existing running sessions were untouched.
- ACP inline images use private per-input artifacts and the existing ViewImage tool path; this is not native image parts in the initial provider request. Mixed block order survives process restart via bounded, prompt-hash-bound metadata. Archive/restore/purge includes the artifacts.
- Limits: eight images and 2 MiB decoded bytes / 32M pixels per prompt, eight image-bearing active turns, existing 4 MiB JSON-line cap. Validation/replay reject malformed or mismatched data and unsafe artifact paths; pre-persist failure cleans only the current input.
- Focused ACP/CLI race tests and attachment/session-manager race tests pass. Official ACP SDK loopback smoke verifies input, real CLI ViewImage execution, image content delivered to Chat Completions, and exact image replay after another restart. No paid model calls. Remote provider vision and named editor interoperability remain unverified.

### 10:08 EDT — counterbalanced delegation pilot

- Pushed 79c468b: content-free child model/effort observations and combined uncached-input accounting, including missing/invalid cache coverage. Focused forwarder tests/race and benchmark tests/race pass.
- Ran two repetitions, reversed arm order, Luna/low, 120-second phase bounds. All eight children have verified Luna/low model events; all four arms completed both children with complete usage and passed both holdouts.
- Dispatcher elapsed was lower in both samples (23.992 vs 36.651 seconds; 33.483 vs 39.766). Uncached input was worse in rep one (19,520 vs 17,024), better in rep two (20,172 vs 23,351). No general savings claim or production policy promotion.
- Raw evidence: benchmarks/results/subagent-schema-counterbalanced-20260923/. Existing installed release remains b263a48; accounting-only source changes will ride the next product installment.

### 10:22 EDT — attachment path reliability installed

- Installed clean af75c3a as 20260923T141955.975433000Z-8545ecbc9436-e8fcb63b after managed Go/UI/type/build gates; existing user sessions remain running.
- Shared attachment intake now expands exactly `~/` against the current user's home. Other shell expressions stay literal. Tests cover selection outside the workspace, missing home, and literal expressions.
- TUI multi-file paste preserves escaped backslashes, apostrophes, and Unicode filenames. Remote file URI authorities are rejected instead of being rewritten into unrelated local paths; localhost URI decoding remains supported.
- Focused parser tests and attachment tests pass; attachment race tests passed before the additional portable missing-home case, which passed normal validation. Clipboard documentation now describes native selection-release copying with OSC 52 fallback.
- Native Finder cross-window drag/drop remains unverified. This installment fixes concrete path handling defects, not the unresolved original freeze or all native terminal transports.

### 10:29 EDT — clipboard responsiveness and first updater speed fix

- Installed clean 05538d2 as 20260923T142909.128525000Z-abe2a9e02177-d42f20f4 using the new source updater. Go checks, build, one UI check/test pass, and production dependency setup completed; running user sessions were not interrupted.
- 46ae6ba moves native clipboard work off the RPC reader with a five-second deadline and one reserved native operation slot. Stalled reader/writer fixtures verify unrelated status requests remain serviceable and the slot recovers after native completion; focused race tests pass. The original reported freeze is not established as clipboard-related.
- UI ignores stale copy replies, bounds pending tracking, and distinguishes busy from timed-out operations that may finish late. Ten focused copy/paste tests pass (41 assertions); existing React act warnings remain in several fixture paths.
- 05538d2 removes redundant pre-build dependency setup/UI checks, retaining build and post-build validation. Update tests assert UI validation once. No timing comparison claimed.
- User requested prebuilt GitHub Releases for fast routine updates. GitHub release list was empty and installer is source-only; task_runtime is implementing paired platform artifacts/checksums/download update support. Checklist retains this as open until delivered and measured.

### 10:37 EDT — visible links and retrieval timestamps

- Installed clean 38c94d8 from the isolated `/tmp/pk-link-style-release` worktree while prebuilt-update implementation continues in the main checkout. Managed Go/UI/build gates passed; running user sessions remain untouched.
- Explicit `markup.link.label` and `markup.strong` styles match OpenTUI's actual token scopes: link labels are blue/underlined independently of hover. Opening links still follows terminal modifier behavior. The exact reported Bitcoin response is saved complete with valid Markdown and renders fully in a focused regression.
- A temporary drag-copy experiment reproduced partially copied hidden bold delimiters. That is a distinct selection serialization defect; tui is implementing a targeted follow-up, not a blanket Markdown strip.
- Successful WebSearch/WebFetch envelopes now include UTC RFC3339-second `retrieved_at`, explicitly not source freshness. Shared-handler fixture/race tests cover success, failure, and partial URL errors without external calls.
- GitHub Release download work remains uncommitted and under review/tests; no prebuilt release has been published yet.

### Release and session-manager follow-up

- Reviewed binary-first updater and bootstrap installer. Focused updater/CLI race tests passed; native macOS archive import smoke passed using the archive's own executable in isolated install directories. The dirty smoke archive is not a release candidate.
- Packaging smoke caught bootstrap dispatch and macOS AppleDouble entries; both are corrected. Release workflow requires Go/UI gates and stages a draft for final download verification. Public binary publishing is still pending the UI gate.
- Session-manager overlap/select-all/purge feedback implementation has focused coverage; additional narrow-terminal/Unicode review is in progress. No user sessions have been archived or deleted.
- The latest pushed source CI, run 35875910744 at 4ba00a8, reports success after the portable Monid fixture correction.
- Added explicit community-plugin compatibility and bounded pk -p regression work to the durable checklist. Current pk plugins use pk.extensions/v1; SKILL.md and MCP interoperability are distinct from foreign executable plugin compatibility.

### 10:54 EDT — session-manager install

Clean revision `19f79b7` passed the managed updater's Go/UI build and test gates and activated release `20260923T145253.657615000Z-6383862dff89-df8d3ba1`. Source worktree `/tmp/pk-session-manager-release` is retained for provenance. Running user sessions were not interrupted. Session-manager tests: 15 pass/58 assertions. A native interactive retest has not been performed for this install.

Binary-first installer/updater and gated draft-release packaging are committed/pushed at `1d40806`; no GitHub binary release is published yet. CI run 35877358636 was queued at this checkpoint. The copy/Markdown correction remains under active implementation; this did not delay the session fix install.

### First binary release gate

Tag `v0.1.0` points to clean `da05621`; release run 35877602396 passed the Linux amd64 package job but failed on macOS 14 in Go updater tests. The failure is read-only staging-directory rename (`permission denied`), before publication; the publish job was skipped and no release assets are public. An updater portability fix is in progress. Keep the failed tag immutable and use a new tag for the corrected candidate.

### 11:08 EDT — GitHub binaries published and installed

Release run 35878570548 for clean tag `v0.1.1` / `28f1429` passed both native platform jobs after the macOS publish-order fix. Both downloaded SHA-256 checksums matched; an isolated macOS install and PTY UI startup passed. Published https://github.com/pkyanam/pk/releases/tag/v0.1.1 and installed its verified macOS archive without touching running user processes. Managed release `legacy-20260923T150731-9d680d28`, archive SHA `30567147dd64535a7d88f5bf1ade6f8642780014d8b102aad7f10f4d09a83ec0`. Measured local archive activation 1.969s and already-current network check 0.195s. A separate public install.sh run in fresh temp install dirs, including script/metadata/checksum/archive fetch, took 4.596s. Timings are single observations.

The new user report confirms unwanted per-turn extension process startup; trace distinguishes UI stderr notices from actual manifest schemas in model requests. Lazy worker startup and complete synthetic provider payload captures are being developed separately. No private email was read.

### 11:24 EDT — payload transparency and product review

Committed three complete native Responses request bodies and an opt-in reproducible capture harness (`5eb59d7`). They use clean installed baseline `28f1429`, a loopback fake provider, and a real synthetic workspace-stats worker. Requests are 7,403 / 7,640 / 7,978 bytes, each with nine tool declarations and a 2,969-character system message. These are not token measurements or live hosted-service captures. The bodies expose full-history resend, stable cache-key use, retained asynchronous tool instructions, and conditional tool registration.

The relevance reminder (`39b3b0f`) adds 108 bytes. One bounded Luna/low test still selected a synthetic mail tool for a Discord greeting (9.65s, 3,680 input / 72 output tokens, zero cached tokens). No real messaging API was connected. This negative result prevents claiming that prompt wording solved irrelevant integration selection.

Reviewed the user's optional Grok critique. Adopt grouped accurate help and safe TUI cancellation; track structured-file-tool evaluation, durable questions, and diagnostics separately. Preserve bottom activity placement requested by the user. Several suggestions are stale or inaccurate: current workspace is already the task default, `pk web` is search configuration, and binary releases now exist. Lazy initialization remains under review after identifying a timeout retry-loop edge case; it is not yet installed.

### 11:34 EDT — v0.1.2 installed; provider catalog underway

Published and installed v0.1.2 / `033bf40`, managed release `legacy-20260923T153425-6f651f0c`. Both native platform jobs passed Go/UI gates and archive import checks; downloaded SHA256 checks passed. The macOS archive additionally passed isolated import and an 80x24 PTY startup/idle Ctrl+C exit. Active user sessions were not interrupted. This release includes lazy tool-extension initialization (hook observers stay eager), bounded shared initialization failures, quiet RPC startup, mid-turn file queues, grouped help, and TUI Ctrl+C draft preservation. All Go packages and 151 UI tests passed locally; package CI repeated platform gates. Model-visible extension schemas remain present, so process laziness is not a token-saving claim.

The requested provider range and key-first GUI are separate work in progress: verified presets, private key entry, live model selection, nonblocking discovery, and native Anthropic Messages support. No external provider key or paid model call was used for this work.

### Midday — provider setup and context measurement checkpoint

Committed native Anthropic Messages support (`e5df0fc`), nine verified provider endpoint presets with cancellable discovery (`5747b03`), and credential-preserving updates limited to unchanged endpoints (`8190551`). A masked input component never passes the raw key to a text renderable (`d703edb`). Provider setup integration tests cover searching presets, entering a key, choosing a discovered model, and preserving the old conversation if starting a replacement session fails. The launch wordmark component targets 60 FPS for a 720 ms mint sweep, then restores the idle rendering rate (`2689ab8`). These changes are awaiting the next paired release.

The user's usage request is implemented in the backend (`71e5729`): latest matching request/response metadata, disjoint serialized-value-byte categories, and presence-aware provider token counters. Session totals remain separate. Numeric-only sidecars use private permissions, atomic replacement, and the existing session archive/restore/purge lifecycle; they contain no prompt text. A 100-iteration local M3 microbenchmark measured 6.89 ms/request with two file syncs versus 0.452 ms after removing those unnecessary syncs for recoverable metrics (8.98 KB and 63 allocations/op). This excludes model/provider work and is not an end-to-end agent speed claim. Focused metrics tests passed; integrated UI and full release gates remain pending.

### v0.1.3 release candidate — user will run the updater

Tagged `v0.1.3` at `57dd4ea` after all Go packages, UI typecheck/build, and 168 UI tests (886 assertions) passed. Provider discovery cancellation and failed-new-session rollback behavior are covered; a canceled old provider request cannot overwrite a newer usage snapshot (`2fb32aa`). The usage panel scrolls and refreshes, with session totals separate from byte composition and latest-response token counts. The release workflow is https://github.com/pkyanam/pk/actions/runs/35888195605.

The user explicitly asked to run `pk update` personally. Keep their installed v0.1.2 and active processes untouched; verify release artifacts in isolation and publish only after those checks. This entry records the candidate, not a published or installed release.

Published v0.1.3 after both platform jobs passed and both downloaded archives matched SHA256SUMS. The first Linux attempt hit a test assumption that two queued steers share one model request; the unchanged release retry passed. The fixture was corrected separately on main (`85b6f94`) and passed 30 normal and five race repetitions. Darwin's archive also passed isolated import and PTY startup/clean Ctrl-C exit. Darwin SHA256: `78e48edd07c5b9d55e8402cd5f2e546da19c97d90ad760360f5c47928547affd`; Linux: `0ebf9074f178b03b9b5a3ca74ed274bfb4fc522ba39c9e80af3c2db72e2a6dd1`. Release: https://github.com/pkyanam/pk/releases/tag/v0.1.3. User installation remains v0.1.2 for their own updater test.

### Post-v0.1.3 — durable task questions and self-knowledge

Background AskUser persistence, worker/RPC integration, and attached-task UI are under implementation. Review identified a crash window between question-state persistence and event publication; recovery tests and repair are required before this capability is marked delivered. Existing published v0.1.3 is unchanged, and the user retains control of installation.

Corrected the bundled pk skill to describe checksum-verified GitHub Release updates (source builds are explicit or compatibility fallback) and distinguish provider token counters from byte-composition measurements. Removed upstream branding from the model-facing architecture summary. The skill validator passed with an isolated PyYAML dependency.
