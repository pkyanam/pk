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
  notification. `session/load` is available for durable sessions and is
  advertised with `agentCapabilities.loadSession: true`.
- Text and resource-link prompt blocks. Committed assistant messages are
  reported at model-response boundaries, and tool-call state is reported while
  tools run, using `session/update` notifications. pk does not expose
  token-level partial drafts yet.
- Cancellation interrupts the active runner call and completes the prompt with
  `stopReason: "cancelled"`.

pk does not advertise or implement session resume/listing, authentication RPC,
MCP server connections, image/audio prompt blocks, client filesystem or
terminal requests, permission requests, or plan/configuration updates.
Non-empty `mcpServers` declarations and unsupported content blocks are rejected
rather than silently ignored. ACP session IDs are the durable runner session
IDs. `session/load` replays saved user and assistant messages and tool-call
state in order; it requires the original workspace saved with the session to
match the client's `cwd`. A session must have completed at least one prompt so
the saved workspace context exists. Sessions created by older ACP builds used
a separate in-memory ID and cannot be loaded by this version.

The wire behavior follows ACP v1's [overview](https://agentclientprotocol.com/protocol/v1/overview),
[initialization](https://agentclientprotocol.com/protocol/v1/initialization),
[session setup](https://agentclientprotocol.com/protocol/v1/session-setup),
[prompt turn](https://agentclientprotocol.com/protocol/v1/prompt-turn), and
[stdio transport](https://agentclientprotocol.com/protocol/v1/transports)
requirements.
