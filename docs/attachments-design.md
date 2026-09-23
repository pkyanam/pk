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
copies image bytes into the prompt or session history, and it does not search for neighboring files.
For now, PDFs use local text extraction only, not native file upload, page images, or OCR.

## Limits and path behavior

`attachments.Load(ctx, workspace, paths, limits)` accepts at most 8 selected paths by default.
Relative paths resolve against the workspace; absolute paths and `..` paths are allowed when named
explicitly. Symlinks are followed to their resolved file. Only regular files are accepted. There is
no glob expansion, directory recursion, or implicit file discovery. Each source is limited to 8 MiB,
the aggregate to 16 MiB, each image to 5 MiB, each text file to 128 KiB, and all extracted text to
128 KiB per prompt. The loader checks file type before opening it so a selected FIFO cannot block
the process.

PDF input is capped at 8 MiB, 20 pages, 64 KiB extracted text, 4 MiB decompressed stream, 50,000
content operators per page, and 20,000 glyphs per page. Extraction is synchronous and receives the
caller context for cancellation. A page/text cap is reported as truncated. The note explicitly says
that extraction is text-only; pages containing images without selectable text are identified as
scanned, and no OCR is implied.

The PDF fallback pins [`github.com/giraffesyo/pdf` v0.6.0](https://github.com/giraffesyo/pdf/tree/v0.6.0),
a zero-external-dependency MIT package with context-aware page extraction and parser resource
limits. It is a young project; keep the version pinned, preserve the malformed/oversize/cancellation
tests, and review maturity/security before upgrading. The fallback is a bounded text extractor, not
a promise of faithful layout, embedded-image understanding, or scan recognition.

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
Ctrl+Y copies selected transcript text using OSC 52; some terminals intercept Cmd+C. Shift+Enter and
Ctrl+J insert a newline without submitting. Native drag-and-drop parsing has tests, but native GUI
drop behavior has not been verified and is not promised.

A live 80×24 PTY smoke attached `marker.pdf` with `/file`, submitted the prompt, received the loaded
notice, and got the exact expected PDF text marker from Luna; the composer was empty afterward.
Earlier CLI smokes also confirmed one-page PDF text extraction and an explicitly selected external
PNG inspected through `ViewImage`. These checks validate the exercised routes, not broad format
coverage. The TUI path is explicit text entry, not a native picker.
