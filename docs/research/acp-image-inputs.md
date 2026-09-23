# ACP image-input implementation boundary

The original ACP prompts accepted text and opaque resource links. The pinned
Unreal v0.1.1 external-input/context path
builds text messages; accepting an ACP image block alone would not deliver a
native multimodal user message to the provider.

The implementation validates inline base64 image blocks, stores private
session-owned artifacts, and uses prompt notes plus ViewImage. This is
tool-mediated image inspection, not direct image parts in the user request.

Implemented boundaries:

- Preserve mixed text/image ordering; bound decoded bytes and aggregate input.
- Validate strict base64, MIME/signature agreement and supported formats using
  the attachment loader, including dimension checks before accepting input.
- Use input persistence hooks for artifact creation and failure cleanup. Retain
  artifacts after durable input and include them in archive/restore/purge.
- Keep resource links opaque; image support must not fetch arbitrary URLs.
- Reconcile the 4 MiB ACP JSON-line limit with base64 expansion. A 5 MiB decoded
  attachment does not fit. Choose an explicit bounded ACP limit rather than
  removing transport limits.
- Exercise rejection, cleanup, resume and lifecycle behavior, plus an actual
  ACP-to-runner fixture that returns an image through ViewImage. Never log image
  base64 in diagnostics.

Direct image parts in user messages require a structured external-input and
message representation, context-builder support, and provider serialization
for both Responses and Chat Completions. That is a separate runtime change;
verify the provider request contains an image part before claiming support.

Inline images use
`inline-images/<session hash>/<input hash>/` with private files and directories.
The session manager's archive/restore/purge lifecycle test and race suite pass.
The official ACP SDK smoke drives the actual CLI: an inline PNG becomes a
ViewImage result in the loopback Chat Completions provider request, and a second
process restart replays the original image block. Model responses are synthetic;
no paid model calls or specific editor interoperability claims are involved.

Limits are eight images, 2 MiB aggregate decoded bytes, and 32 million aggregate
source pixels per prompt; eight image-bearing turns may run concurrently.
Ordered replay metadata is bound to the durable prompt's hash. Replay rejects
invalid metadata, symlinks in managed artifact paths, or oversized artifacts.
Legacy inputs without image metadata keep their existing text replay.
