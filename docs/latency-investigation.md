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

The phase timer shipped in `6813acb`; the streaming observer and UI shipped and were
installed in `eed3d6e`. The UI passed 40 tests (170 assertions), typechecking, and its build.
Go race tests cover the observer, RPC bridge, and runner integration. A local SSE server
test holds completion open and verifies that a visible draft arrives before completion,
while the session history contains only the eventual authoritative response.

A live Luna RPC smoke delivered its first text at 1.222 seconds and its first authoritative
assistant event at 2.772 seconds. Tool preparation events preceded a Bash call; the generated
fixture was verified, and the final confirmation streamed before the turn finished. These
are observations from one small task, not latency guarantees. Headless/task output remains
completion-based in this installment. Real Cmux launch was verified; the interactive GUI
streaming check was interrupted by user activity and is not claimed as passed.
