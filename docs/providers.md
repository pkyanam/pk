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

The catalog currently contains nine presets:

| Preset | API | Protocol |
| --- | --- | --- |
| Anthropic | Native Anthropic Messages | `anthropic_messages` |
| Cerebras | OpenAI-compatible | `chat_completions` |
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
`chat_completions`, and `anthropic_messages`. Set
`--reasoning-effort` only when a compatible endpoint accepts that request field.

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
