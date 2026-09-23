# Response latency and streaming

## Observed behavior, 2026-09-23

A user reported a long “Waiting for model” state followed by an abrupt response during a
Three.js coding task. The local session journal confirms the long model turn began at
04:42:17.571474 UTC and completed at 04:43:36.832745 UTC. The provider reported 4,322
output tokens; a Bash call contained 13,340 characters of arguments, including generated
application code. The journal records completed responses, not first-byte or first-token
timestamps, so it cannot distinguish provider queueing from generation time or retries.

Two separate presentation problems made this harder to understand:

- The installed UI's elapsed timer measured the entire user task, including question and
  tool time, while appearing beside a label describing only the current phase.
- Unreal Agent v0.1.1 receives Responses API SSE events, but its adapter returns the
  response only after a terminal event. pk's runner emits assistant messages after that
  response is persisted. Incoming text and tool-argument generation were invisible.

## Implementation boundary

Keep the existing Responses API parser and coordinator authoritative. A pk-owned client
can construct the exported Responses adapter with an HTTP transport that observes a copy
of the SSE bytes. A request-context callback keeps observations scoped to one request and
survives the primitive client's derived HTTP contexts. Do not replace the global HTTP
transport or edit the Go module cache.

Display provisional assistant text, tool name and argument byte counts, attempt number,
and request timing. Do not display reasoning or raw tool-argument deltas. Partial tool calls
must never execute. On completion, reconcile the provisional display with the authoritative
response; reset it on retries, cancellation, and failure. Observation must be bounded and
must not change the bytes consumed by the existing parser.

The phase timer and streaming observer are in development. They are not part of the
installed UI checkpoint `c589e26`; this document is diagnostic evidence, not a release claim.
