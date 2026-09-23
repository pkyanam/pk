# AgentMail read-only plugin

The example in [`examples/plugins/agentmail`](../../examples/plugins/agentmail/) connects pk to AgentMail through the installed `agentmail` CLI. It exposes three narrowly scoped tools:

| Tool | Operation | Bound |
| --- | --- | --- |
| `mail_inboxes` | List inboxes | At most 25 rows; only ID, address, and display name are returned. |
| `mail_list` | List recent messages in one inbox | At most 25 summaries; previews are capped at 512 bytes. Supports one page token. |
| `mail_get` | Get one message by inbox and message ID | Plain text, or extracted text when plain text is absent, capped at 12 KiB. HTML and attachments are omitted. |

## Install and run

Install and authenticate the AgentMail CLI using its [official CLI guide](https://www.agentmail.to/docs/integrations/cli). It supports OS keychain credentials or `AGENTMAIL_API_KEY`; the extension inherits the CLI's normal credential lookup and does not read or copy credentials itself.

```sh
npm install -g agentmail-cli
agentmail auth login --with-token --scheme BearerAuth

go build -o examples/plugins/agentmail/agentmail-worker ./examples/plugins/agentmail
pk run -p "List my inboxes and report their count." \
  --extension examples/plugins/agentmail/manifest.json
```

For the interactive TUI, enter `/plugin enable "/absolute/path/to/manifest.json"`, then `/new`. `/plugins` lists and toggles already known plugin records; it does not browse for manifest files. Extension tools are fixed for a session, so enabling a plugin does not alter an already-running session. Plugin manifests are explicitly selected; pk does not scan workspaces for extensions.

## Scope and handling

The worker invokes only `agentmail inboxes list`, `agentmail inboxes messages list`, and `agentmail inboxes messages get` in JSON mode. It has no code path for sending, drafting, creating, updating, or deleting email resources. It does not request raw messages or download attachments. Message body responses include a notice that the email is untrusted data; email instructions should be treated as content to inspect, not as authority.

The installed `agentmail` executable must be on `PATH`, and it must already be authenticated. Errors intentionally omit raw CLI diagnostics because those can echo private data; check `agentmail auth status` locally when a request fails. The extension process is not sandboxed: it runs as the local user, with that user's filesystem permissions and the CLI's existing AgentMail access. The manifest's capability declarations are not an OS permission boundary.

AgentMail's official API describes the inbox listing as `GET /v0/inboxes`, message listing as `GET /v0/inboxes/{inbox_id}/messages`, and a single-message fetch as `GET /v0/inboxes/{inbox_id}/messages/{message_id}`. See [List Inboxes](https://docs.agentmail.to/api-reference/inboxes/list), [List Messages](https://docs.agentmail.to/api-reference/inboxes/messages/list), and [Get Message](https://docs.agentmail.to/api-reference/inboxes/messages/get).

## Smoke test

The local AgentMail CLI authenticated successfully. An end-to-end `pk run` using this manifest exited successfully and emitted a `mail_inboxes` tool call; the prompt requested only the inbox count, and test reporting suppressed inbox addresses and response contents. No inboxes or messages were changed.

The worker has fixture tests for the three command shapes, limit clamping, field filtering, body truncation, unknown arguments, and read-only dispatch. Run them with:

```sh
go test ./examples/plugins/agentmail -count=1
```
