# Codex image generation and pk integration

Checked 2026-09-22 against local Codex CLI help/features, current official OpenAI API docs, and the official `openai/codex` source repository. One small low-effort image-generation smoke was run with the existing ChatGPT login; no credential file or token value was read. A separate, unintegrated Go prototype now lives in `internal/imagegen`.

## Finding

The installed Codex CLI is `/opt/homebrew/bin/codex`, version `codex-cli 0.154.0`. `codex features list` reports `image_generation` as `stable true`. `codex exec --help` documents noninteractive runs and an `--image` option for supplying images to the initial prompt; it does not expose a dedicated `codex image generate` command. The feature listing plus source establish a model-invokable image-generation tool inside Codex runs; the `--image` flag alone is input support, not generation.

The OpenAI Codex repository implements this as an image-generation extension. Its extension registers a model-callable tool only when the configured model provider qualifies as OpenAI/auth-compatible. It constructs the backend with Codex's `AuthManager`, and the backend obtains provider auth and image provider from the active configured model provider before making generation/edit requests. Thus the Codex built-in path is designed to use the current Codex provider/login and does not require `OPENAI_API_KEY`; the bundled official imagegen skill explicitly says so. This source-backed Codex feature is distinct from the public OpenAI API integration below.

`codex login status` reported “Logged in using ChatGPT” (no account or token values were read). An initial smoke with `gpt-6-luna` and low effort failed before any model request: the CLI reported that Luna is unsupported with this ChatGPT account. After the parent task authorized a verified supported fallback, one low-effort smoke with locally cataloged `gpt-6-astra` succeeded and invoked built-in image generation. This fallback is a separate image-worker driver setting; it does not change pk's Luna model or silently retry its normal model. The result confirms image-generation entitlement for this login and driver combination, but it does not prove Luna compatibility.

The smoke exposed an important artifact detail. With `codex exec --json --ephemeral`, JSONL contained only thread/turn lifecycle and ordinary agent-message items; it did not contain an image-generation item, base64 result, or saved path. The tool said it had saved the file at its default location, and a valid 1254x1254 PNG appeared under `$CODEX_HOME/generated_images/<thread.started thread_id>/exec-<id>.png`. Matching the emitted thread ID isolated the exact image from the run. The worker must copy only from that exact per-thread directory, validate the PNG and destination, and fail on absent or ambiguous artifacts; searching all of `generated_images` is unsafe. Codex tool-side output may land outside the temporary CLI workspace even when the worker uses `--sandbox workspace-write`, so that behavior should remain explicit in the integration contract.

## What is and is not a supported integration path

| Path | Auth / contract | pk implications |
|---|---|---|
| Codex CLI built-in `image_gen` tool | Codex's own image extension uses its active provider and `AuthManager`. The official bundled skill says built-in mode needs no API key. The local binary exposes stable `image_generation`; the smoke confirms ChatGPT login access through `gpt-6-astra`, but not `gpt-6-luna`. Its JSONL omitted the image item and the generated PNG was saved under the exact thread-ID directory in `CODEX_HOME/generated_images`. | A model-invokable pk tool can delegate a tightly scoped image request to `codex exec` and use the user's existing Codex login. This adds another model run, latency, and image usage. Copy only from that run's generated-images directory and validate the artifact and destination. Keep the worker model explicit, separate from pk's model, and configurable. |
| Reimplement Codex's internal image backend directly | The open Codex source routes through internal provider/auth crates and an internal Codex Images endpoint. The source shows the mechanism, but this is not a documented public API or stable third-party auth contract. | Do not hard-code `/api/codex/images/...`, copy private auth assumptions, or send pk's OAuth access token to a guessed endpoint. Direct integration would couple pk to Codex internals and could break without a public compatibility guarantee. |
| Public Responses API `image_generation` tool or Images API | Official API docs describe model-invokable Responses API generation/editing and standalone Images API generation/editing. Public API authentication uses an API key; GPT Image access may also require API organization verification. | This is a clean direct API integration if the user opts into API credentials and API billing. It does not satisfy “reuse the ChatGPT/Codex login” by itself. Keep the API-key path explicit and separate from pk's subscription/Codex login. |

OpenAI's public docs establish that the Responses API supports an `image_generation` tool: a mainline Responses model can invoke it, the image model is selected in the tool configuration, and results include image-generation output data. The public Image API is also documented for standalone generation and editing. OpenAI's API guidance says standard API requests authenticate with API keys; the public docs reviewed do not establish that a ChatGPT subscription access token can authenticate to these public API endpoints.

## Official source anchors

- Local CLI evidence: `/opt/homebrew/bin/codex --version`, `codex --help`, `codex exec --help`, `codex features list`, and `codex login status`. The local result was version `0.154.0`; `image_generation` was `stable true`; login status disclosed only “Logged in using ChatGPT.” The `gpt-6-astra` low-effort smoke succeeded; the `gpt-6-luna` attempt failed before generation.
- Official Codex [image-generation extension](https://github.com/openai/codex/blob/main/codex-rs/ext/image-generation/src/extension.rs#L38-L123): provider-eligibility check, `AuthManager` wiring, and tool registration.
- Official Codex [image backend](https://github.com/openai/codex/blob/main/codex-rs/ext/image-generation/src/backend.rs#L55-L95): resolves active provider and auth, then sends generation/edit through the Images client.
- Official Codex [Images endpoint implementation](https://github.com/openai/codex/blob/main/codex-rs/codex-api/src/endpoint/images.rs#L1108-L1169): appends `images/generations` or `images/edits` to the configured provider URL. This is source evidence for Codex's internal route, not a public endpoint contract.
- Official Codex [image tool](https://github.com/openai/codex/blob/main/codex-rs/ext/image-generation/src/tool.rs#L59-L125): the current source's `gpt-image-2` default, model-facing prompt/reference fields, and directly exposed tool. The implementation also saves artifacts and emits image-generation items/events.
- Official Codex [bundled imagegen skill](https://github.com/openai/codex/blob/main/codex-rs/skills/src/assets/samples/imagegen/SKILL.md#L207-L237): built-in mode requires no `OPENAI_API_KEY`; CLI/API fallback requires it; built-in output defaults under `$CODEX_HOME` and should be copied into a project when project-bound.
- OpenAI [Responses API image generation tool](https://developers.openai.com/api/docs/guides/tools-image-generation) and [image-generation guide](https://developers.openai.com/api/docs/guides/image-generation): supported public tool/API surfaces, supported image model selection, edits, input/output data, and possible API-organization verification.
- OpenAI [API production best practices](https://developers.openai.com/api/docs/guides/production-best-practices#api-keys): API-key authentication. The [API quickstart](https://developers.openai.com/api/docs/quickstart) also starts by creating a Platform API key.

One source-version detail: Codex's current image tool source sets its default image model constant to `gpt-image-2`, whereas current public API docs list GPT Image 2.5 models. Do not copy the internal default without checking the installed Codex version and current supported models.

## Bounded pk design proposal

Preferred first implementation for subscription-login users: add an explicit `ImageGen` tool to pk and spawn the installed `codex exec` as a separate worker. The pk model chooses whether to call it. Run Codex with `--ignore-user-config` (verified local CLI help says this skips `$CODEX_HOME/config.toml` while auth still uses CODEX_HOME), workspace-write sandboxing, shell execution disabled, and an allowlisted environment that does not forward API-key variables. The tested CLI's JSONL did not expose image items; identify the run by `thread.started`, inspect only that thread's generated-images directory, and copy one validated PNG to a workspace-relative caller-selected destination. Treat failures, absent/ambiguous artifacts, usage limits, cancellation, and missing CLI/login as normal tool errors.

The `internal/imagegen` prototype now provides a model-visible `ImageGen` translator and an asynchronous `operation.RemoteJobHandler`, plus the CLI process bridge. The tool is registered only when the caller explicitly configures an image-driver model; this setting does not change pk's Luna/default model, and there is no automatic fallback. The worker uses low effort by default, `--ignore-user-config`, shell-disabled workspace-write sandboxing, ephemeral execution, a bounded event reader, exact thread-ID artifact lookup, PNG signature/full-decode/dimension/size validation, workspace-bounded exclusive output writes, and process-group cancellation. Run-scoped handler teardown is waitable; an end-to-end runner test confirms a canceled operation is durable and its worker process is gone. Adversarial tests cover decoy files under other thread IDs, missing and multiple images, malformed/oversized JSONL, invalid/truncated PNGs, symlinks, destination preflight, and credential-env filtering. The image extension writes its generated source PNG under CODEX_HOME outside the temporary CLI working directory; integration should keep that provenance visible and return only the copied workspace artifact. The CLI flag and overall UX are still being integrated; do not present this as a shipped feature yet.

Suggested first-pass tool contract:

```json
{
  "name": "ImageGen",
  "arguments": {
    "prompt": "...",
    "reference_paths": ["selected/path.png"],
    "output_path": "optional-new-path.png"
  }
}
```

Implementation checks should enforce: nonblank prompt length limit; zero to five explicitly named reference images with image MIME/signature checks and a combined byte cap; output confined to the task workspace unless the user explicitly selects an external destination; refusal to overwrite with root-bounded writes; subprocess timeout/cancel/reap; and no image bytes in durable text history or routine logs. Return the saved artifact path, dimensions, MIME type, and a short status. Let a later model turn call `ViewImage` when it needs to inspect the result.

This delegation adds a second model call and should be measured for latency and usage; don't present it as the same request or cost as a native pk-provider tool. A future direct backend can be considered only with a supported provider/auth interface or explicit OpenAI API-key mode. Do not ship private endpoint assumptions as an undocumented integration.
