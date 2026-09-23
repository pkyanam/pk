# ACP image-input implementation boundary

Current ACP prompts accept text and opaque resource links. `Turn` carries a
string to the runner. The pinned Unreal v0.1.1 external-input/context path also
builds text messages; accepting an ACP image block alone would not deliver a
native multimodal user message to the provider.

A compatible first implementation could validate inline base64 image blocks,
store private session-owned image artifacts, and reuse pk's attachment notes
and ViewImage tool. This is tool-mediated image inspection, not direct vision
input. It should not advertise native image delivery or silently accept data
that the model never receives.

Required implementation and checks:

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

This is a source audit and implementation proposal. ACP image blocks remain
unsupported in the current release.
