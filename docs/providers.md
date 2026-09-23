# OpenAI-compatible providers

`pk` keeps ChatGPT/Codex as the default provider. You can add an explicit OpenAI-compatible provider for Responses API or Chat Completions deployments; the protocol is selected by you and is never guessed from the server.

Configure a local endpoint without an API key:

```sh
pk provider add --id local --protocol chat_completions --base-url http://127.0.0.1:8080 --model qwen3
```

Configure a remote endpoint using an environment variable for its key:

```sh
export MY_MODEL_API_KEY=...
pk provider add --id team --protocol responses --base-url https://models.example/v1 \
  --api-key-env MY_MODEL_API_KEY --model my-model --effort medium --reasoning-effort
```

For a key you do not want in the process environment, pipe it to stdin so it is saved in the private provider file instead of command history or process arguments:

```sh
printf '%s' "$MY_MODEL_API_KEY" | pk provider add --id team --protocol chat_completions \
  --base-url https://models.example/v1 --api-key-stdin --model my-model
```

Provider records are stored in `$PK_HOME/providers.json` (normally `~/.pk/providers.json`) with mode `0600` inside a mode `0700` directory. `pk provider list` shows only whether a key is configured and, for environment-backed keys, the variable name. It never displays literal key values.

Use `pk provider models ID` to query the configured endpoint’s `/models` route. `pk provider remove ID` deletes a provider. Bare origins normalize to `/v1`; explicit paths such as `/v1` are preserved. Plain HTTP is allowed only for loopback addresses; remote endpoints must use HTTPS.

The Responses adapter uses `/responses` and the Chat Completions adapter uses `/chat/completions`; both stream model responses and preserve tool calls and usage data. `--reasoning-effort` is opt-in because many compatible models do not accept that request field. The Chat Completions bridge currently represents image tool results as an omitted-image notice; the portable mapping is text-only.

Use `pk provider use ID` to select the default for new sessions, or `pk provider use native` to return to the built-in Codex provider. Interactive sessions can select a provider before their first prompt; once a session has started, its provider is fixed. Saved sessions retain their provider identity and fingerprint, so attach requires that same provider configuration and rejects changed endpoints or credentials.
