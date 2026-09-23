# ACP server support

`pk acp` exposes a stdio server for Agent Client Protocol (ACP) v1 clients. It
uses UTF-8, newline-delimited JSON-RPC 2.0 messages. ACP responses and
`session/update` notifications go to stdout; diagnostics stay on stderr.

Start it as a child process of an ACP client:

```sh
pk acp [--model MODEL] [--effort EFFORT]
```

The defaults come from `pk config` (`gpt-6-luna`, `medium`). Credentials are
read from pk's normal login state; log in with `pk login` before starting the
client. Each ACP session uses the client-supplied absolute `cwd` as its working
directory. The first prompt creates a durable pk runner session, and subsequent
prompts in that ACP session continue that same runner session.

The implemented v1 surface is deliberately limited:

- `initialize`, `session/new`, `session/prompt`, and the `session/cancel`
  notification.
- Text and resource-link prompt blocks. Committed assistant messages are
  reported at model-response boundaries, and tool-call state is reported while
  tools run, using `session/update` notifications. pk does not expose
  token-level partial drafts yet.
- Cancellation interrupts the active runner call and completes the prompt with
  `stopReason: "cancelled"`.

pk does not advertise or implement session load/resume, session listing,
authentication RPC, MCP server connections, image/audio prompt blocks, client
filesystem or terminal requests, permission requests, or plan/configuration
updates. Non-empty `mcpServers` declarations and unsupported content blocks are
rejected rather than silently ignored. The ACP process keeps its session map in
memory; persistent runner history remains in pk's local session store, but ACP
session IDs cannot yet be reattached after the ACP process exits.

The wire behavior follows ACP v1's [overview](https://agentclientprotocol.com/protocol/v1/overview),
[initialization](https://agentclientprotocol.com/protocol/v1/initialization),
[session setup](https://agentclientprotocol.com/protocol/v1/session-setup),
[prompt turn](https://agentclientprotocol.com/protocol/v1/prompt-turn), and
[stdio transport](https://agentclientprotocol.com/protocol/v1/transports)
requirements.
