# Explicit file attachments

This note describes the bounded loader in `internal/attachments` and the model paths it can use. It
does not claim native file-upload support in pk's Codex transport.

## Transport boundary

The pinned Unreal Agent adapter accepts `llm.Message` with one text string. Its Responses serializer
turns a user message into `input_text`; it does not map a user attachment into `input_image` or
`input_file` ([pinned message type](https://github.com/unreallabsai/unreal-agent/blob/b7c9bf1c5c2fa4127255c07727a7c8413e23944a/harness/llm/model.go),
[request serialization](https://github.com/unreallabsai/unreal-agent/blob/b7c9bf1c5c2fa4127255c07727a7c8413e23944a/harness/llm/responsesapi/request.go)).
The OpenAI Responses API documents native image and file inputs, including PDF text and page-image
processing on vision-capable models, but that API documentation does not establish that the separate
ChatGPT Codex endpoint accepts an equivalent file-input payload through this adapter
([image inputs](https://developers.openai.com/api/docs/guides/images-vision),
[file inputs](https://developers.openai.com/api/docs/guides/file-inputs)).

pk therefore uses only paths the user explicitly selects. Text files and PDFs are read into the
current user input. Images are represented by their validated path and inspected through the
existing `ViewImage` operation; its result returns image content to the model. The loader never
copies source image bytes into the prompt or session history, and it does not search for neighboring
files. PDFs are not uploaded as native PDF inputs and no OCR is performed. In addition to text
extraction, pk can optionally render a small number of image-only/scanned PDF pages to local PNGs
for the existing `ViewImage` operation.

## Limits and path behavior

`attachments.Load(ctx, workspace, paths, limits)` accepts at most 8 selected paths by default.
Relative paths resolve against the workspace; `~/` expands to the current user's home directory.
Other shell expressions, including `~user` and environment variables, are not expanded.
Absolute paths and `..` paths are allowed when named
explicitly. Symlinks are followed to their resolved file. Only regular files are accepted. There is
no glob expansion, directory recursion, or implicit file discovery. Each source is limited to 8 MiB,
the aggregate to 16 MiB, each image to 5 MiB, each text file to 128 KiB, and all extracted text to
128 KiB per prompt. The loader checks file type before opening it so a selected FIFO cannot block
the process.

PDF input is capped at 8 MiB, 20 pages, 64 KiB extracted text, 4 MiB decompressed stream, 50,000
content operators per page, and 20,000 glyphs per page. Extraction is synchronous and receives the
caller context for cancellation. A page/text cap is reported as truncated. Pages containing images
without selectable text are identified as scanned, and no OCR is implied.

When `pdftoppm` (from Poppler) is available, scanned/image-only pages may also be previewed. At most
three candidate pages are rendered, each scaled to at most 1600 pixels, with a 4 MiB per-image and
12 MiB per-prompt total image limit and a shared 20-second render deadline. For mixed PDFs, only the
pages identified as scanned are rendered; text-bearing pages are not rasterized. If the renderer is
missing or fails a resource/safety check, text extraction remains available and the attachment note
reports that no preview was made. `pdftoppm` is an optional local fallback, not a bundled dependency.

The PDF text extractor pins [`github.com/giraffesyo/pdf` v0.6.0](https://github.com/giraffesyo/pdf/tree/v0.6.0),
a zero-external-dependency MIT package with context-aware page extraction and parser resource
limits. It is a young project; keep the version pinned, preserve the malformed/oversize/cancellation
tests, and review maturity/security before upgrading. The extractor is bounded and does not promise
faithful layout or scan recognition. Raster previews use the separately installed Poppler `pdftoppm`
tool only for page images; it does not add native PDF upload or OCR.

Rendered PNGs live under a private per-session, per-input directory in the session store. The paths
are stable for the saved input so `ViewImage` can inspect them after attach/reload. Session archive
and restore carry those artifacts with the session; permanent purge removes them. Failed prompt
setup cleans up previews that were not persisted. The session manager validates the managed paths
before archive or deletion.

## Host integration

The current integration seam is deliberately small:

```go
items, err := attachments.Load(ctx, workspace, selectedPaths, attachments.Limits{})
if err != nil {
    return err
}
prompt += attachments.FormatPromptNote(items)
```

The host appends that note to the new user input, not the saved system prefix. Text and extracted PDF
content are labeled as user-provided data, include the selected filename, and are bounded. Image
notes give the model only the selected path and direct it to `ViewImage`. The selected image path can
be absolute when it is outside the workspace because `ViewImage` accepts absolute paths; the tool
still applies its own image decoding, resizing, and size limits. The host must not describe an image
as analyzed until that tool call succeeds.

The one-shot route is `pk run -p PROMPT --file PATH` (repeat `--file` for multiple selections);
relative paths use the run workspace, while an explicitly named absolute path may be outside it.
The RPC prompt request also accepts a `files` array for a host that provides one. In the OpenTUI,
`/file PATH` queues an explicitly selected path for the next foreground prompt. Quote paths with
spaces. `/files` lists the queue, `/files remove N` removes one item by its 1-based list number, and
`/files clear` empties it. Click a visible chip to remove that selection. The effective per-prompt
limit is 8 files. There is no file browser or implicit selection. A queued file remains in the
foreground draft until the host acknowledges successful loading; a prompt error preserves it for
retry. Detached task creation does not transfer or consume that draft queue.

The OpenTUI also accepts a bare path as a prompt and sends it with the default request “Inspect the
attached file.” A path followed by prose, such as `notes.txt describe this`, sends that prose as the
request. Quote paths containing spaces; ambiguous unquoted paths stay in the draft. Bracketed terminal
paste routes recognized file URI lists and quoted/escaped paths to the queue. Ctrl+V asks the native
clipboard for content; `/paste` is the explicit fallback when a terminal intercepts the shortcut.
Releasing a transcript selection copies it through the native clipboard; Ctrl+Y also copies the
selection. OSC 52 is a fallback when native copying is unavailable, and some terminals intercept Cmd+C. Shift+Enter and
Ctrl+J insert a newline without submitting. Native drag-and-drop parsing has tests, but native GUI
drop behavior has not been verified and is not promised.

A live 80×24 PTY smoke attached `marker.pdf` with `/file`, submitted the prompt, received the loaded
notice, and got the exact expected PDF text marker from Luna; the composer was empty afterward.
Unit fixtures cover bounded rendering of scanned pages, selecting only image-only pages in a mixed
PDF, private output modes, renderer absence/failure, timeout, and session archive/restore/purge.
A real-Poppler local smoke also preserved selectable cover text, rendered scanned page 2 into a
decodable PNG, and verified resume, archive, restore, and purge without a model request. Installed in `b5aa90b`. Earlier CLI smokes confirmed selectable-text PDF extraction and an explicitly selected external PNG inspected
through `ViewImage`. These checks validate exercised routes, not broad format coverage. The TUI path
is explicit text entry, not a native picker.

## Native Cmux checks (2026-09-23)

Clipboard RPC operations run independently of the request reader, with a five-second response
deadline and one native operation in flight. A timeout leaves the native slot occupied until the
operation actually returns: macOS pasteboard calls cannot be cancelled once entered. Other RPC
requests can continue. The UI suppresses fallback copying when a native write might still finish,
and ignores stale copy replies so they cannot trigger a fallback for an older selection. This bounds
pk's clipboard work; it does not guarantee that the operating system clipboard service will recover.

Two separately exercised paths reached the model successfully:

- Finder: select a generated PNG whose filename contains a space, Cmd+C, then Cmd+V in pk. The file chip held the selected path; `ViewImage` inspected that path and Luna described the image correctly (session `03872f2a`).
- Preview: select all pixels of that generated PNG, Cmd+C, then Cmd+V in Cmux. Cmux created a clipboard PNG; pk queued it, and `ViewImage` inspected it successfully (session `7c1a2e81`). This checks raw image copying through Cmux, not just copying a filename.

These are terminal-specific observations, not a guarantee for every terminal or image encoding.
Native cross-window drag/drop remains unverified: automated attempts did not produce a queued
file. Use `/file`, Finder paste, or `/paste` while that interaction is still being validated.

A bounded live Luna/low run on installed `b5aa90b` attached a valid mixed PDF, invoked `ViewImage` on the rendered second page, and returned “A solid red square appears against a white background.” The opt-in Poppler integration test now checks visible red pixels, not only valid PNG decoding. This one fixture does not establish general PDF fidelity. [Usage record](../benchmarks/results/pdf-vision-smoke-20260923/result.json).

## Inline output previews

Assistant replies can display local PNG, JPEG, WebP, and GIF files inline, from either
`![description](path)` or an ordinary image link. Previews fit within the transcript and use
OpenTUI's native terminal graphics when supported, with a colored-cell fallback elsewhere.
Remote images are not fetched automatically. Rendering a preview is local UI work and adds no
model request or tokens.

Preview sources must resolve to regular files inside the active workspace; generated `sandbox:`
references may also resolve inside the temporary directory. Source files are limited to 32 MiB, 8192 pixels per dimension, and 16 million pixels,
and each reply displays at most two previews. Decoding is serialized; mounted previews
share a 32-million-pixel budget. When full, the original-file link remains available. The full-image link uses a local file URL rather
than passing the unsupported `sandbox:` scheme to the operating system.

Cmux uses native Kitty graphics, preserving image detail rather than drawing colored text
cells. Until OpenTUI publishes its next package, pk builds the matching 0.5.12 native
renderer from the pinned [upstream layering fix](https://github.com/anomalyco/opentui/pull/1525).
Release archives include that library; users running `pk update` do not compile it.
Source builds use `scripts/prepare-opentui-native`, which verifies the toolchain and caches
the built library. It leaves the pinned JavaScript packages unchanged.

Successful ImageGen tool results also create an output card, independently of the assistant's
final reply. Saved sessions reconstruct those cards from completed operation results after
validating the local artifact. The card stays in place if later prose links the same image.
Missing or deleted images cannot be reconstructed from a filename; the original file remains
necessary. These previews do not send image bytes back to the model.
