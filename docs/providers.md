# Model providers

ChatGPT/Codex login remains pk's native default. Other providers use credentials and endpoints that
you configure explicitly. Provider setup does not change the native ChatGPT login.

## Connect a preset in the TUI

Available starting with v0.1.3. Use `/provider setup` (or choose **Connect a provider** from `/provider`):

1. Search the built-in provider catalog and select a preset.
2. Paste its API key into the masked field and save.
3. pk queries that endpoint's model list. Choose a model. If the current conversation already has
   a prompt, pk starts a fresh session automatically and labels that transition in the UI; otherwise
   the choice applies to the new session already being created.

Selecting a provider/model in the TUI saves that pair for future sessions and relaunches.
Model and effort changes belong to the selected provider; external model IDs do not replace
the native ChatGPT defaults. Existing sessions retain their original provider binding.

The catalog currently contains ten presets:

| Preset | API | Protocol |
| --- | --- | --- |
| Anthropic | Native Anthropic Messages | `anthropic_messages` |
| Cerebras | OpenAI-compatible | `chat_completions` |
| Cloudflare Workers AI | OpenAI-compatible Chat Completions | `cloudflare_workers_ai` |
| Google Gemini | OpenAI-compatible | `chat_completions` |
| Groq | OpenAI-compatible | `chat_completions` |
| Mistral | OpenAI-compatible | `chat_completions` |
| OpenAI API | OpenAI Responses | `responses` |
| OpenRouter | OpenAI-compatible | `chat_completions` |
| Together AI | OpenAI-compatible | `chat_completions` |
| xAI | OpenAI Responses | `responses` |

Presets are connection templates, not account checks. Model discovery shows IDs returned by the
configured endpoint; it does not guarantee that your account is entitled to use a model, that the
model is available in your region, or that it supports every feature pk can send.

## CLI configuration

List configured providers:

```sh
pk provider list
```

`pk provider presets` prints public connection metadata, not credentials.

Configure a local endpoint without a key:

```sh
pk provider add --id local --protocol chat_completions \
  --base-url http://127.0.0.1:8080 --model qwen3
```

Configure a remote endpoint with an environment-backed key:

```sh
export MY_MODEL_API_KEY=...
pk provider add --id team --protocol responses \
  --base-url https://models.example/v1 --api-key-env MY_MODEL_API_KEY \
  --model my-model --effort medium
```

Or read a key from standard input so it does not appear in shell history or process arguments:

```sh
printf '%s' "$MY_MODEL_API_KEY" | pk provider add --id team \
  --protocol chat_completions --base-url https://models.example/v1 \
  --api-key-stdin --model my-model
```

Use `pk provider models ID` to query the configured endpoint's `/models` route, `pk provider use ID`
to select the default for new runs, `pk provider use native` to return to ChatGPT/Codex, and
`pk provider remove ID` to delete a provider. Supported protocols are `responses`,
`chat_completions`, `anthropic_messages`, and `cloudflare_workers_ai`. Set
`--reasoning-effort` only when a compatible endpoint accepts that request field.

### Cloudflare Workers AI

Setup references: [Cloudflare REST API setup](https://developers.cloudflare.com/workers-ai/get-started/rest-api/) and [OpenAI compatibility](https://developers.cloudflare.com/workers-ai/configuration/open-ai-compatibility/).

Workers AI is configured as a direct Cloudflare account endpoint; this setup does not use AI
Gateway. It needs a Cloudflare account ID and an API token with Workers AI access. The provider
uses Cloudflare's OpenAI-compatible Chat Completions endpoint for streamed text and tool calls.
Model discovery uses Cloudflare's account-scoped model catalog and returns text-generation model
names such as `@cf/...`; catalog availability does not guarantee a model is enabled for the account.
Tool support varies by model, and pk shows the function-calling badge only when the catalog reports
it. If a selected model rejects tool calls, choose one marked for function calling.

In the TUI, choose **Cloudflare Workers AI**, enter the 32-character account ID, paste the API token
into the masked field, and choose a discovered model. The token is stored in pk's private provider
config. CLI setup is also available without putting the token in shell history or process arguments:

```sh
printf '%s' "$CLOUDFLARE_API_TOKEN" | pk provider add \
  --preset cloudflare-workers-ai \
  --account-id 0123456789abcdef0123456789abcdef \
  --api-key-stdin
pk provider models cloudflare-workers-ai
```

Workers AI streams visible answer text and tool-preparation progress into the TUI.
Reasoning-only chunks show **Model is thinking**; their contents are not displayed or saved
by the progress observer. A longer prompt can still take longer to process at the provider.
Progress is evidence of arriving data, not a guarantee of completion time.

The integration is verified with local HTTP fixtures for setup, catalog parsing, streaming, and a
tool-call/result round trip. No live Cloudflare account request is part of these tests.

Provider records live in `~/.pk/providers.json` (or `$PK_HOME/providers.json`). The file is protected
with mode `0600` inside pk's private directory; API keys stored there are **not encrypted**. You can
instead store only an environment-variable name with `--api-key-env`. Provider list output reports
whether a key is configured and never prints its value.

Remote endpoints must use HTTPS. Plain HTTP is allowed only for loopback addresses. Bare API origins
normalize to `/v1`; explicit API-root paths are preserved. The selected provider's configuration is
bound to saved sessions; attach requires the same provider identity and configuration fingerprint.

## Protocol notes

Responses and Chat Completions adapters stream responses and preserve supported tool calls and usage
fields. Chat Completions accepts HTTP(S) image references and bounded base64 PNG/JPEG/GIF/WebP image
data in image-bearing tool results; this is not a promise that every endpoint or model supports
images. It does not provide a portable mapping for all Responses features. Provider-specific
capabilities and billing rules remain controlled by that provider.

The native Anthropic Messages adapter uses Anthropic's Messages API.
It sends image blocks only for supported base64 images returned by tools; it does not currently map
user-message image inputs. Generic reasoning-effort settings are not mapped to Anthropic-specific
thinking options. For the official `api.anthropic.com` HTTPS endpoint, pk requests ephemeral prompt
caching; this depends on Anthropic's cache rules and prompt size, and cache writes may cost more than
uncached input. Custom gateways do not receive that cache setting. No cache hit or savings are
guaranteed.

OpenAI API-key access uses the OpenAI Responses API and is separate from native ChatGPT/Codex login.
See the [getting-started guide](getting-started.md) for the interactive setup flow and
[provider presets source](../internal/providers/presets.go) for current catalog metadata.

### Streaming activity and reasoning details

The activity line distinguishes recent stream progress from waiting for more data. A long wait
alone cannot establish whether a provider is reasoning, queued, or stalled. Latest-response TPS
includes provider wait and excludes tool execution; it is not a live decoder-speed measurement.

In the TUI, Ctrl+O also expands reasoning text explicitly returned by compatible external chat
providers when available. These optional details are bounded, transient, and excluded from saved
conversation history and subsequent model prompts. They are provider output, not verified facts.
Native Codex private reasoning is not extracted. Providers that expose no such text show only
activity information. Workers AI effort is provider-controlled unless its configured adapter
explicitly supports an effort parameter.
