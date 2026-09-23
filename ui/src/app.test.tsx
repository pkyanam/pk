import { afterEach, beforeEach, describe, expect, test } from "bun:test"
import { destroyTreeSitterClient, getTreeSitterClient } from "@opentui/core"
import { testRender } from "@opentui/react/test-utils"
import { act } from "react"
import type { ServerEvent } from "./protocol"
import { PkApp } from "./app"
import { leadingPathFromPrompt, parsePastedPaths } from "./app"
import type { PkTransport } from "./transport"

const openRenderers: Array<Awaited<ReturnType<typeof testRender>>> = []

afterEach(() => {
  for (const item of openRenderers.splice(0)) {
    act(() => item.renderer.destroy())
  }
  return destroyTreeSitterClient()
})

beforeEach(async () => {
  const client = getTreeSitterClient()
  await client.initialize()
  await client.preloadParser("markdown")
  await client.preloadParser("markdown_inline")
})

function fakeTransport() {
  let handler = (_event: ServerEvent) => {}
  const sent: Array<{ id: string; type: string; payload?: Record<string, unknown> }> = []
  let sequence = 0
  const transport = {
    setEventHandler(next: typeof handler) { handler = next },
    async start() {},
    send(type: string, payload?: Record<string, unknown>) { const id = `fake-${++sequence}`; sent.push({ id, type, payload }); return id },
    emit(event: ServerEvent) { handler(event) },
    async close() {},
  }
  return { transport: transport as unknown as PkTransport, sent, emit: transport.emit }
}

describe("OpenTUI application", () => {
  test.each([{ width: 80, height: 24 }, { width: 120, height: 36 }])("renders a usable shell at $width × $height", async ({ width, height }) => {
    const fake = fakeTransport()
    const setup = await testRender(<PkApp transport={fake.transport} workspace="/tmp/pk" />, { width, height })
    openRenderers.push(setup)
    await setup.flush()
    const frame = await setup.waitForFrame((value) => value.includes("Message"))
    expect(frame).toContain("pk")
    expect(frame).toContain("/tmp/pk")
    expect(frame).toContain("Message")
    expect(frame).toContain("Ask pk to inspect")
    expect(frame).toContain("^P menu")
    expect(frame).not.toContain("\\n")
    expect(frame.split("\n")).toHaveLength(height + 1)
  })

  test.each([{ width: 80, height: 24, count: 28 }, { width: 140, height: 55, count: 60 }])("keeps a busy transcript viewport inside the composer at $width × $height", async ({ width, height, count }) => {
    const fake = fakeTransport()
    const setup = await testRender(<PkApp transport={fake.transport} workspace="/tmp/pk" />, { width, height })
    openRenderers.push(setup)
    await setup.waitForFrame((frame) => frame.includes("Message"))
    act(() => {
      for (let index = 0; index < count; index++) {
        fake.emit({ version: 1, id: `loop-${index}`, type: "task_output", payload: { role: "user", text: `Loop event ${index}` } })
      }
    })
    await setup.flush()
    const frame = setup.captureCharFrame()
    expect(frame).toContain(`Loop event ${count - 1}`)
    expect(frame).toContain("Message")
    expect(frame.split("\n")).toHaveLength(height + 1)
    expect(frame.indexOf(`Loop event ${count - 1}`)).toBeLessThan(frame.indexOf("Message"))
  })

  test("mouse wheel preserves a manual scroll position while new rows arrive", async () => {
    const fake = fakeTransport()
    const setup = await testRender(<PkApp transport={fake.transport} workspace="/tmp/pk" />, { width: 100, height: 30 })
    openRenderers.push(setup)
    await setup.waitForFrame((frame) => frame.includes("Message"))
    act(() => {
      for (let index = 0; index < 50; index++) fake.emit({ version: 1, id: `scroll-${index}`, type: "task_output", payload: { role: "user", text: `Scroll item ${index}` } })
    })
    await setup.flush()
    const transcript = (setup.renderer.root as any).findDescendantById("transcript")
    const bottom = transcript.scrollTop
    expect(bottom).toBeGreaterThan(0)
    await act(async () => setup.mockMouse.scroll(10, 8, "up"))
    await setup.flush()
    const manualOffset = transcript.scrollTop
    expect(manualOffset).toBeLessThan(bottom)
    act(() => fake.emit({ version: 1, id: "new-row", type: "task_output", payload: { role: "user", text: "New while reading" } }))
    await setup.flush()
    expect(transcript.scrollTop).toBe(manualOffset)
    await act(async () => setup.mockMouse.scroll(10, 8, "down"))
    await setup.flush()
    expect(transcript.scrollTop).toBeGreaterThan(manualOffset)
  })

  test("executes a selected slash command from the multiline composer", async () => {
    const fake = fakeTransport()
    const setup = await testRender(<PkApp transport={fake.transport} workspace="/tmp/pk" />, { width: 100, height: 30 })
    openRenderers.push(setup)
    await setup.waitForFrame((frame) => frame.includes("Message"))
    await act(async () => { await setup.mockInput.typeText("/status") })
    await setup.flush()
    act(() => setup.mockInput.pressEnter())
    await setup.flush()
    expect(fake.sent.some((item) => item.type === "status")).toBe(true)
  })

  test("Enter activates the highlighted no-argument slash command", async () => {
    const fake = fakeTransport()
    const setup = await testRender(<PkApp transport={fake.transport} workspace="/tmp/pk" />, { width: 100, height: 30 })
    openRenderers.push(setup)
    await setup.waitForFrame((frame) => frame.includes("Message"))
    await act(async () => { await setup.mockInput.typeText("/") })
    await setup.flush()
    act(() => setup.mockInput.pressEnter())
    await setup.flush()
    const frame = await setup.waitForFrame((value) => value.includes("Select model"))
    expect(frame).toContain("Select model")
    expect(frame).toContain("Luna")
  })

  test("mouse opens the slash model selector and picks a model", async () => {
    const fake = fakeTransport()
    const setup = await testRender(<PkApp transport={fake.transport} workspace="/tmp/pk" />, { width: 100, height: 30 })
    openRenderers.push(setup)
    await setup.waitForFrame((frame) => frame.includes("Message"))
    await act(async () => { await setup.mockInput.typeText("/") })
    await setup.flush()
    let frame = setup.captureCharFrame()
    let row = frame.split("\n").findIndex((line) => line.includes("/model"))
    await act(async () => setup.mockMouse.click(frame.split("\n")[row]!.indexOf("/model"), row))
    frame = await setup.waitForFrame((value) => value.includes("Select model"))
    row = frame.split("\n").findIndex((line) => line.includes("Sol · balanced"))
    await act(async () => setup.mockMouse.click(frame.split("\n")[row]!.indexOf("Sol · balanced"), row))
    await setup.flush()
    expect(fake.sent.some((item) => item.type === "set_model" && item.payload?.model === "gpt-6-sol")).toBe(true)
  })

  test("an unrelated RPC error does not clear a submitted turn or disable cancel", async () => {
    const fake = fakeTransport()
    const setup = await testRender(<PkApp transport={fake.transport} workspace="/tmp/pk" />, { width: 100, height: 30 })
    openRenderers.push(setup)
    await setup.waitForFrame((frame) => frame.includes("Message"))
    act(() => fake.emit({ version: 1, type: "ready", payload: { model: "gpt-6-luna", effort: "medium" } }))
    await setup.flush()
    await act(async () => { await setup.mockInput.typeText("please inspect this") })
    act(() => setup.mockInput.pressEnter())
    await setup.flush()
    expect(fake.sent.some((item) => item.type === "prompt")).toBe(true)

    act(() => fake.emit({ version: 1, id: "unrelated-setting", type: "error", payload: { message: "could not save model preference" } }))
    await setup.flush()
    expect(setup.captureCharFrame()).toContain("Waiting for model")

    let escapeName = ""
    const keyObserver = (key: { name: string }) => { escapeName = key.name }
    setup.renderer.keyInput.on("keypress", keyObserver)
    await act(async () => {
      setup.mockInput.pressEscape()
      await new Promise((resolve) => setTimeout(resolve, 40))
    })
    await setup.flush()
    setup.renderer.keyInput.off("keypress", keyObserver)
    expect(escapeName).toBe("escape")
    expect(fake.sent.some((item) => item.type === "cancel")).toBe(true)
  })

  test("clears the submitted composer and accepts a second prompt after transcript interaction", async () => {
    const fake = fakeTransport()
    const setup = await testRender(<PkApp transport={fake.transport} workspace="/tmp/pk" />, { width: 100, height: 30 })
    openRenderers.push(setup)
    await setup.waitForFrame((frame) => frame.includes("Message"))
    act(() => fake.emit({ version: 1, type: "ready", payload: { model: "gpt-6-luna", effort: "medium" } }))
    await setup.flush()

    await act(async () => { await setup.mockInput.typeText("first request") })
    await setup.flush()
    act(() => setup.mockInput.pressEnter())
    await setup.flush()
    expect(fake.sent.filter((item) => item.type === "prompt")).toHaveLength(1)
    const composer = (setup.renderer.root as any).findDescendantById("composer")
    expect(composer.plainText).toBe("")

    act(() => fake.emit({ version: 1, id: "turn-start", type: "turn_started", payload: {} }))
    act(() => fake.emit({ version: 1, id: "assistant-reply", type: "assistant", payload: { text: "First answer." } }))
    act(() => fake.emit({ version: 1, id: "turn-end", type: "turn_finished", payload: {} }))
    act(() => fake.emit({ version: 1, id: "tool-card", type: "tool_call", payload: { call_id: "card-1", name: "Bash", state: "completed", command_preview: "pwd" } }))
    const frame = await setup.waitForFrame((value) => value.includes("First answer.") && value.includes("Bash · pwd"))
    const toolRow = frame.split("\n").findIndex((line) => line.includes("Bash · pwd"))
    await act(async () => setup.mockMouse.click(8, toolRow))
    await setup.flush()

    await act(async () => { await setup.mockInput.typeText("second follow-up") })
    await setup.flush()
    expect(composer.plainText).toBe("second follow-up")
    act(() => setup.mockInput.pressEnter())
    await setup.flush()
    expect(fake.sent.filter((item) => item.type === "prompt")).toHaveLength(2)
    expect(fake.sent.filter((item) => item.type === "prompt")[1]?.payload?.text).toBe("second follow-up")
    expect(composer.plainText).toBe("")
  })

  test("queues quoted file paths, shows removable chips, and sends only explicit files", async () => {
    const fake = fakeTransport()
    const setup = await testRender(<PkApp transport={fake.transport} workspace="/tmp/pk" />, { width: 80, height: 24 })
    openRenderers.push(setup)
    await setup.waitForFrame((frame) => frame.includes("Message"))
    act(() => fake.emit({ version: 1, type: "ready", payload: { model: "gpt-6-luna", effort: "medium" } }))
    await setup.flush()

    await act(async () => { await setup.mockInput.typeText('/file "docs/meeting notes.pdf"') })
    await setup.flush()
    act(() => setup.mockInput.pressEnter())
    const queued = await setup.waitForFrame((frame) => frame.includes("Files 1/8") && frame.includes("meeting note"))
    expect(queued).toContain("Queued file 1/8 · docs/meeting notes.pdf")

    await act(async () => { await setup.mockInput.typeText("/files") })
    await setup.flush()
    act(() => setup.mockInput.pressEnter())
    expect(await setup.waitForFrame((frame) => frame.includes("Queued files:") && frame.includes("1. docs/meeting notes.pdf"))).toContain("/files remove N")
    await act(async () => { await setup.mockInput.typeText("/files remove 1") })
    await setup.flush()
    act(() => setup.mockInput.pressEnter())
    await setup.flush()
    expect(setup.captureCharFrame()).not.toContain("Files 1/8")

    await act(async () => { await setup.mockInput.typeText('/file "docs/meeting notes.pdf"') })
    await setup.flush()
    act(() => setup.mockInput.pressEnter())
    const chipFrame = await setup.waitForFrame((frame) => frame.includes("Files 1/8") && frame.includes("meeting note"))

    const filesRow = chipFrame.split("\n").findIndex((line) => line.includes("Files 1/8"))
    await act(async () => setup.mockMouse.click(chipFrame.split("\n")[filesRow]!.indexOf("meeting note"), filesRow))
    await setup.flush()
    expect(setup.captureCharFrame()).not.toContain("Files 1/8")

    await act(async () => { await setup.mockInput.typeText('/file "docs/meeting notes.pdf"') })
    await setup.flush()
    act(() => setup.mockInput.pressEnter())
    await setup.flush()
    await act(async () => { await setup.mockInput.typeText("/files clear") })
    await setup.flush()
    act(() => setup.mockInput.pressEnter())
    await setup.flush()
    expect(setup.captureCharFrame()).not.toContain("Files 1/8")
    await act(async () => { await setup.mockInput.typeText('/file "docs/meeting notes.pdf"') })
    await setup.flush()
    act(() => setup.mockInput.pressEnter())
    await setup.flush()
    await act(async () => { await setup.mockInput.typeText("Summarize this document") })
    await setup.flush()
    act(() => setup.mockInput.pressEnter())
    await setup.flush()
    const prompt = fake.sent.find((item) => item.type === "prompt")!
    expect(prompt.payload?.text).toBe("Summarize this document")
    expect(prompt.payload?.files).toEqual(["docs/meeting notes.pdf"])

    act(() => fake.emit({ version: 1, id: prompt.id, type: "turn_started", payload: {} }))
    await setup.flush()
    expect(setup.captureCharFrame()).toContain("Files 1/8")
    act(() => fake.emit({ version: 1, id: prompt.id, type: "attachments_loaded", payload: { files: [{ path: "/tmp/pk/docs/meeting notes.pdf", kind: "pdf", pages_extracted: 2, truncated: false }] } }))
    const loaded = await setup.waitForFrame((frame) => frame.includes("Loaded meeting notes.pdf (pdf, 2 pages)"))
    expect(loaded).not.toContain("Files 1/8")
  })

  test("recognizes explicit absolute and relative file paths without treating unknown slash commands as files", async () => {
    const fake = fakeTransport()
    const setup = await testRender(<PkApp transport={fake.transport} workspace="/tmp/pk" />, { width: 100, height: 30 })
    openRenderers.push(setup)
    await setup.waitForFrame((frame) => frame.includes("Message"))
    act(() => fake.emit({ version: 1, type: "ready", payload: { model: "gpt-6-luna", effort: "medium" } }))
    await setup.flush()

    await act(async () => { await setup.mockInput.typeText("/Users/preetham/Pictures/portrait photo.jpeg describe this") })
    await setup.flush()
    act(() => setup.mockInput.pressEnter())
    await setup.flush()
    let prompt = fake.sent.find((item) => item.type === "prompt")
    expect(prompt?.payload?.text).toBe("describe this")
    expect(prompt?.payload?.files).toEqual(["/Users/preetham/Pictures/portrait photo.jpeg"])
    act(() => fake.emit({ version: 1, id: prompt?.id, type: "attachments_loaded", payload: { files: [{ path: "/Users/preetham/Pictures/portrait photo.jpeg", kind: "image" }] } }))
    act(() => fake.emit({ version: 1, type: "turn_finished", payload: {} }))
    await setup.flush()

    await act(async () => { await setup.mockInput.typeText("../docs/design/brief.pdf") })
    await setup.flush()
    act(() => setup.mockInput.pressEnter())
    await setup.flush()
    prompt = fake.sent.filter((item) => item.type === "prompt").at(-1)
    expect(prompt?.payload?.text).toBe("Inspect the attached file.")
    expect(prompt?.payload?.files).toEqual(["../docs/design/brief.pdf"])
    act(() => fake.emit({ version: 1, type: "turn_finished", payload: {} }))

    await act(async () => { await setup.mockInput.typeText("/notacommand") })
    await setup.flush()
    act(() => setup.mockInput.pressEnter())
    expect(await setup.waitForFrame((frame) => frame.includes("Unknown command: /notacommand"))).toContain("Ready")
  })

  test("routes a leading file path and keeps following prose as the prompt", async () => {
    expect(leadingPathFromPrompt("/tmp/portrait.jpeg please describe this image")).toEqual({ path: "/tmp/portrait.jpeg", prompt: "please describe this image" })
    expect(leadingPathFromPrompt('"/tmp/portrait photo.jpeg" describe this')).toEqual({ path: "/tmp/portrait photo.jpeg", prompt: "describe this" })
    expect(leadingPathFromPrompt("/tmp/portrait photo.jpeg")).toEqual({ path: "/tmp/portrait photo.jpeg", prompt: "" })
    expect(leadingPathFromPrompt("/tmp/portrait photo describe this")).toEqual({ ambiguous: true })
    expect(leadingPathFromPrompt("/notacommand")).toBeUndefined()

    const fake = fakeTransport()
    const setup = await testRender(<PkApp transport={fake.transport} workspace="/tmp/pk" />, { width: 100, height: 30 })
    openRenderers.push(setup)
    await setup.waitForFrame((frame) => frame.includes("Message"))
    act(() => fake.emit({ version: 1, type: "ready", payload: { model: "gpt-6-luna", effort: "medium" } }))
    await setup.flush()
    await act(async () => { await setup.mockInput.typeText("/tmp/portrait.jpeg please describe this image") })
    act(() => setup.mockInput.pressEnter())
    await setup.flush()
    const prompt = fake.sent.find((item) => item.type === "prompt")
    expect(prompt?.payload?.text).toBe("please describe this image")
    expect(prompt?.payload?.files).toEqual(["/tmp/portrait.jpeg"])
  })

  test("queues bracketed pasted paths and preserves ordinary pasted prose", async () => {
    expect(parsePastedPaths("/tmp/one.png\n/tmp/two.pdf")).toEqual({ paths: ["/tmp/one.png", "/tmp/two.pdf"], prompt: "" })
    expect(parsePastedPaths("file:///tmp/meeting%20notes.pdf", "text/uri-list")).toEqual({ paths: ["/tmp/meeting notes.pdf"], prompt: "" })
    expect(parsePastedPaths('"/tmp/one file.png" "/tmp/two file.pdf"')).toEqual({ paths: ["/tmp/one file.png", "/tmp/two file.pdf"], prompt: "" })
    expect(parsePastedPaths("/tmp/one.png /tmp/two\\ file.pdf")).toEqual({ paths: ["/tmp/one.png", "/tmp/two file.pdf"], prompt: "" })
    expect(parsePastedPaths("a regular paragraph about files")).toBeUndefined()

    const fake = fakeTransport()
    const setup = await testRender(<PkApp transport={fake.transport} workspace="/tmp/pk" />, { width: 100, height: 30 })
    openRenderers.push(setup)
    await setup.waitForFrame((frame) => frame.includes("Message"))
    await act(async () => { setup.renderer.keyInput.processPaste(new TextEncoder().encode("/tmp/one.png\n/tmp/two.pdf")) })
    await setup.flush()
    expect(setup.captureCharFrame()).toContain("Files 2/8")
    await act(async () => { setup.renderer.keyInput.processPaste(new TextEncoder().encode("describe these files")) })
    await setup.flush()
    expect(setup.captureCharFrame()).toContain("describe these files")
  })

  test("requests clipboard files only on explicit paste shortcut and queues returned paths", async () => {
    const fake = fakeTransport()
    const setup = await testRender(<PkApp transport={fake.transport} workspace="/tmp/pk" />, { width: 100, height: 30, kittyKeyboard: true })
    openRenderers.push(setup)
    await setup.waitForFrame((frame) => frame.includes("Message"))
    expect(fake.sent.some((item) => item.type === "clipboard_paste")).toBe(false)
    await act(async () => setup.mockInput.pressKey("v", { ctrl: true }))
    const request = fake.sent.find((item) => item.type === "clipboard_paste")
    expect(request).toBeDefined()
    act(() => fake.emit({ version: 1, id: request?.id, type: "clipboard_files", payload: { files: [{ path: "/tmp/pasted.png", kind: "image" }], text: "describe this" } }))
    await setup.flush()
    expect(setup.captureCharFrame()).toContain("pasted.png")
    expect(setup.captureCharFrame()).toContain("describe this")
    await act(async () => setup.mockInput.pressKey("v", { super: true }))
    expect(fake.sent.filter((item) => item.type === "clipboard_paste")).toHaveLength(2)
  })

  test("Shift-Enter inserts a newline without submitting", async () => {
    const fake = fakeTransport()
    const setup = await testRender(<PkApp transport={fake.transport} workspace="/tmp/pk" />, { width: 100, height: 30, kittyKeyboard: true })
    openRenderers.push(setup)
    await setup.waitForFrame((frame) => frame.includes("Message"))
    await act(async () => { await setup.mockInput.typeText("first line") })
    await act(async () => setup.mockInput.pressEnter({ shift: true }))
    await act(async () => { await setup.mockInput.typeText("second line") })
    await setup.flush()
    expect(fake.sent.some((item) => item.type === "prompt")).toBe(false)
    expect(setup.captureCharFrame()).toContain("first line")
    expect(setup.captureCharFrame()).toContain("second line")
  })

  test("Ctrl-Y copy shortcut is explicit and leaves Ctrl-C behavior untouched", async () => {
    const fake = fakeTransport()
    const setup = await testRender(<PkApp transport={fake.transport} workspace="/tmp/pk" />, { width: 100, height: 30, kittyKeyboard: true })
    openRenderers.push(setup)
    await setup.waitForFrame((frame) => frame.includes("Message"))
    await act(async () => setup.mockInput.pressKey("y", { ctrl: true }))
    await setup.flush()
    expect(setup.captureCharFrame()).toContain("Select transcript text first")
    expect(fake.sent.some((item) => item.type === "shutdown")).toBe(false)
  })

  test("rejects a ninth queued file to match the attachment service limit", async () => {
    const fake = fakeTransport()
    const setup = await testRender(<PkApp transport={fake.transport} workspace="/tmp/pk" />, { width: 100, height: 30 })
    openRenderers.push(setup)
    await setup.waitForFrame((frame) => frame.includes("Message"))
    act(() => fake.emit({ version: 1, type: "ready", payload: { model: "gpt-6-luna", effort: "medium" } }))
    await setup.flush()

    for (let index = 1; index <= 9; index++) {
      await act(async () => { await setup.mockInput.typeText(`/file file-${index}.txt`) })
      await setup.flush()
      act(() => setup.mockInput.pressEnter())
      await setup.flush()
    }
    const frame = await setup.waitForFrame((value) => value.includes("attachment queue is full (8 files)"))
    expect(frame).toContain("Files 8/8")
    expect(frame).not.toContain("Files 9/8")
  })

  test("preserves queued files when the backend rejects a prompt", async () => {
    const fake = fakeTransport()
    const setup = await testRender(<PkApp transport={fake.transport} workspace="/tmp/pk" />, { width: 100, height: 30 })
    openRenderers.push(setup)
    await setup.waitForFrame((frame) => frame.includes("Message"))
    act(() => fake.emit({ version: 1, type: "ready", payload: { model: "gpt-6-luna", effort: "medium" } }))
    await setup.flush()
    await act(async () => { await setup.mockInput.typeText("/file report.pdf") })
    await setup.flush()
    act(() => setup.mockInput.pressEnter())
    await setup.flush()
    await act(async () => { await setup.mockInput.typeText("Review the report") })
    await setup.flush()
    act(() => setup.mockInput.pressEnter())
    await setup.flush()
    const prompt = fake.sent.find((item) => item.type === "prompt")!
    act(() => fake.emit({ version: 1, id: prompt.id, type: "error", payload: { request_type: "prompt", message: "file not found" } }))
    const frame = await setup.waitForFrame((value) => value.includes("file not found"))
    expect(frame).toContain("Files 1/8")
    expect(frame).toContain("report.pdf")
  })

  test("does not imply queued files were attached to a durable task", async () => {
    const fake = fakeTransport()
    const setup = await testRender(<PkApp transport={fake.transport} workspace="/tmp/pk" />, { width: 100, height: 30 })
    openRenderers.push(setup)
    await setup.waitForFrame((frame) => frame.includes("Message"))
    act(() => fake.emit({ version: 1, type: "ready", payload: { model: "gpt-6-luna", effort: "medium" } }))
    await setup.flush()
    await act(async () => { await setup.mockInput.typeText("/file report.pdf") })
    await setup.flush()
    act(() => setup.mockInput.pressEnter())
    await setup.flush()
    await act(async () => { await setup.mockInput.typeText('/task new "Review report.pdf"') })
    await setup.flush()
    act(() => setup.mockInput.pressEnter())
    const frame = await setup.waitForFrame((value) => value.includes("Queued files are not sent to background tasks"))
    const composer = (setup.renderer.root as any).findDescendantById("composer")
    expect(composer.plainText).toBe('/task new "Review report.pdf"')
    expect(frame).toContain("Files 1/8")
    expect(fake.sent.some((item) => item.type === "task_create")).toBe(false)
  })

  test("keeps prompt draft and queued files when the attached task cannot accept files", async () => {
    const fake = fakeTransport()
    const setup = await testRender(<PkApp transport={fake.transport} workspace="/tmp/pk" />, { width: 100, height: 30 })
    openRenderers.push(setup)
    await setup.waitForFrame((frame) => frame.includes("Message"))
    act(() => fake.emit({ version: 1, type: "ready", payload: { model: "gpt-6-luna", effort: "medium" } }))
    await setup.flush()
    await act(async () => { await setup.mockInput.typeText("/file report.pdf") })
    await setup.flush()
    act(() => setup.mockInput.pressEnter())
    await setup.flush()
    act(() => fake.emit({ version: 1, id: "attach-task", type: "task_attached", payload: { task_id: "task-1", status: "completed" } }))
    await setup.flush()
    await act(async () => { await setup.mockInput.typeText("Use the attached report") })
    await setup.flush()
    act(() => setup.mockInput.pressEnter())
    const frame = await setup.waitForFrame((value) => value.includes("File attachments are not supported for this background task"))
    const composer = (setup.renderer.root as any).findDescendantById("composer")
    expect(composer.plainText).toBe("Use the attached report")
    expect(frame).toContain("Files 1/8")
    expect(fake.sent.some((item) => item.type === "send_input")).toBe(false)
  })

  test("rpc_closed ends thinking state and marks live tools interrupted", async () => {
    const fake = fakeTransport()
    const setup = await testRender(<PkApp transport={fake.transport} workspace="/tmp/pk" />, { width: 100, height: 30 })
    openRenderers.push(setup)
    await setup.waitForFrame((frame) => frame.includes("Message"))
    act(() => fake.emit({ version: 1, id: "turn-start", type: "turn_started", payload: {} }))
    act(() => fake.emit({ version: 1, id: "tool-running", type: "tool_call", payload: { call_id: "live-1", name: "Bash", state: "running", command_preview: "go test ./..." } }))
    await setup.flush()
    act(() => fake.emit({ version: 1, type: "rpc_closed" }))
    const frame = await setup.waitForFrame((value) => value.includes("Agent connection closed. Relaunch pk"))
    expect(frame).not.toContain("Thinking…")
    expect(frame).toContain("Bash · go test ./...")
    expect(frame).toContain("↯")
  })

  test("restores bounded session history and marks omitted earlier entries", async () => {
    const fake = fakeTransport()
    const setup = await testRender(<PkApp transport={fake.transport} workspace="/tmp/pk" />, { width: 100, height: 30 })
    openRenderers.push(setup)
    await setup.waitForFrame((frame) => frame.includes("Message"))
    act(() => fake.emit({
      version: 1,
      id: "attach-1",
      type: "history",
      payload: {
        session_id: "session-123",
        truncated: true,
        entries: [
          { role: "user", text: "Earlier request", sequence: 1 },
          { role: "assistant", text: "Earlier answer", sequence: 2 },
        ],
      },
    }))
    const frame = await setup.waitForFrame((value) => value.includes("Earlier request") && value.includes("earlier messages were omitted"))
    expect(frame).toContain("Earlier request")
    expect(frame).toContain("pk")
    expect(frame).toContain("earlier messages were omitted")
  })

  test("keeps tool activity chronological after completion without object coercion", async () => {
    const fake = fakeTransport()
    const setup = await testRender(<PkApp transport={fake.transport} workspace="/tmp/pk" />, { width: 100, height: 30 })
    openRenderers.push(setup)
    await setup.waitForFrame((frame) => frame.includes("Message"))
    act(() => fake.emit({ version: 1, id: "turn", type: "turn_started", payload: {} }))
    act(() => fake.emit({
      version: 1, id: "tool-running", type: "tool_call",
      payload: { call_id: "call-1", name: "Bash", state: "running", command_preview: "bun test", arguments_preview: { cmd: "bun test" }, elapsed_ms: 41000 },
    }))
    act(() => fake.emit({ version: 1, id: "assistant", type: "assistant", payload: { text: "Tests are complete." } }))
    await setup.flush()
    act(() => fake.emit({
      version: 1, id: "tool-finished", type: "tool_call",
      payload: { call_id: "call-1", name: "Bash", state: "completed", command_preview: "bun test", arguments_preview: { cmd: "bun test" }, elapsed_ms: 42000, operations: [{ type: "shell", state: "completed", output_excerpt: "6 pass", exit_code: 0 }] },
    }))
    act(() => fake.emit({ version: 1, id: "turn-end", type: "turn_finished", payload: {} }))
    const frame = await setup.waitForFrame((value) => value.includes("Bash · bun test"))
    expect(frame).toContain("Bash · bun test")
    expect(frame).toContain("Tests are complete.")
    expect(frame).not.toContain("6 pass")
    expect(frame).toContain("bun test")
    expect(frame).not.toContain("[object Object]")
    const toolRow = frame.split("\n").findIndex((line) => line.includes("Bash · bun test"))
    await act(async () => setup.mockMouse.click(8, toolRow))
    const expanded = await setup.waitForFrame((value) => value.includes("6 pass"))
    expect(expanded).toContain("$ bun test")
    act(() => setup.mockInput.pressKey("o", { ctrl: true }))
    await setup.flush()
    expect(setup.captureCharFrame()).not.toContain("6 pass")
  })

  test("groups adjacent tools but keeps assistant updates as timeline boundaries", async () => {
    const fake = fakeTransport()
    const setup = await testRender(<PkApp transport={fake.transport} workspace="/tmp/pk" />, { width: 120, height: 36 })
    openRenderers.push(setup)
    await setup.waitForFrame((frame) => frame.includes("Message"))
    const tool = (id: string, command: string) => fake.emit({ version: 1, id, type: "tool_call", payload: { call_id: id, name: "Bash", state: "completed", command_preview: command, elapsed_ms: 1000, operations: [{ type: "shell", state: "completed", output_excerpt: `${command} output`, exit_code: 0 }] } })
    act(() => tool("cmd-1", "go test ./a"))
    act(() => tool("cmd-2", "go test ./b"))
    act(() => fake.emit({ version: 1, id: "commentary", type: "assistant", payload: { text: "Between the test groups." } }))
    act(() => tool("cmd-3", "go test ./c"))
    const frame = await setup.waitForFrame((value) => value.includes("Between the test groups."))
    expect(frame).toContain("Ran 2 commands")
    expect(frame).toContain("go test ./c")
    expect(frame).not.toContain("go test ./a output")
    const groupLine = frame.split("\n").findIndex((line) => line.includes("Ran 2 commands"))
    await act(async () => setup.mockMouse.click(8, groupLine))
    const expanded = await setup.waitForFrame((value) => value.includes("go test ./a output"))
    expect(expanded).toContain("go test ./b output")
    expect(expanded.indexOf("Ran 2 commands")).toBeLessThan(expanded.indexOf("Between the test groups."))
    expect(expanded.indexOf("Between the test groups.")).toBeLessThan(expanded.indexOf("go test ./c"))
  })

  test("renders block and inline markdown after Tree-sitter highlighting completes", async () => {
    const fake = fakeTransport()
    const setup = await testRender(<PkApp transport={fake.transport} workspace="/tmp/pk" />, { width: 120, height: 36 })
    openRenderers.push(setup)
    await setup.waitForFrame((frame) => frame.includes("Message"))
    const markdown = "# Heading\n\n- List item\n\n*italic word* and _underscored emphasis_.\n\nSetext heading\n===============\n\n---\n\n```sh\nprintf hello\n```\n\n    printf indented\n\n[Docs][reference]\n\n[reference]: https://example.com"
    const parser = getTreeSitterClient()
    const highlightResult = await parser.highlightOnce(markdown, "markdown")
    expect(highlightResult.error).toBeUndefined()
    act(() => fake.emit({ version: 1, id: "markdown", type: "assistant", payload: { text: markdown } }))
    const renderedText = ["Heading", "List item", "italic word", "underscored emphasis", "Setext heading", "printf hello", "printf indented", "Docs"]
    const frame = await setup.waitForFrame((value) => renderedText.every((text) => value.includes(text)))
    expect(frame).toContain("List item")
    expect(frame).toContain("italic word")
    expect(frame).toContain("underscored emphasis")
    expect(frame).toContain("Setext heading")
    expect(frame).toContain("printf hello")
    expect(frame).toContain("printf indented")
    expect(frame).toContain("Docs")
    expect(frame).not.toContain("# Heading")
    expect(frame).not.toContain("*italic word*")
    expect(frame).not.toContain("[Docs][reference]")
  })

  test("keeps live activity visible through tool and model gaps until turn_finished", async () => {
    const fake = fakeTransport()
    const setup = await testRender(<PkApp transport={fake.transport} workspace="/tmp/pk" />, { width: 100, height: 30 })
    openRenderers.push(setup)
    await setup.waitForFrame((frame) => frame.includes("Message"))
    act(() => fake.emit({ version: 1, type: "ready", payload: { model: "gpt-6-luna", effort: "medium" } }))
    act(() => fake.emit({ version: 1, id: "turn", type: "turn_started", payload: {} }))
    await setup.flush()
    expect(setup.captureCharFrame()).toContain("Waiting for model")
    await act(async () => { await new Promise((resolve) => setTimeout(resolve, 2200)) })
    await setup.flush()
    expect(setup.captureCharFrame()).toMatch(/Waiting for model · [1-9]s/)
    act(() => fake.emit({ version: 1, id: "tool", type: "tool_call", payload: { call_id: "tool", name: "Bash", state: "running", command_preview: "go test ./..." } }))
    await setup.flush()
    expect(setup.captureCharFrame()).toContain("Running 1 tool")
    act(() => fake.emit({ version: 1, id: "tool-done", type: "tool_call", payload: { call_id: "tool", name: "Bash", state: "completed", command_preview: "go test ./..." } }))
    act(() => fake.emit({ version: 1, id: "final", type: "assistant", payload: { text: "All tests pass." } }))
    await setup.flush()
    expect(setup.captureCharFrame()).toContain("Waiting for model")
    expect(setup.captureCharFrame()).toContain("All tests pass.")
    act(() => fake.emit({ version: 1, id: "turn-done", type: "turn_finished", payload: {} }))
    await setup.flush()
    expect(setup.captureCharFrame()).toContain("Ready")
  })

  test.each([
    { name: "single-star and underscore emphasis", markdown: "*only italic* and _underscored italic_", expected: "only italic" },
    { name: "a setext heading", markdown: "Setext headline\n==============", expected: "Setext headline" },
    { name: "a horizontal rule", markdown: "Before\n\n---\n\nAfter", expected: "After" },
    { name: "indented code", markdown: "    printf indented", expected: "printf indented" },
    { name: "a reference link", markdown: "[Docs][reference]\n\n[reference]: https://example.com", expected: "Docs" },
  ])("renders $name through OpenTUI Markdown", async ({ markdown, expected }) => {
    const fake = fakeTransport()
    const setup = await testRender(<PkApp transport={fake.transport} workspace="/tmp/pk" />, { width: 120, height: 36 })
    openRenderers.push(setup)
    await setup.waitForFrame((frame) => frame.includes("Message"))
    const result = await getTreeSitterClient().highlightOnce(markdown, "markdown")
    expect(result.error).toBeUndefined()
    act(() => fake.emit({ version: 1, id: "markdown-variant", type: "assistant", payload: { text: markdown } }))
    const frame = await setup.waitForFrame((value) => value.includes(expected))
    expect(frame).toContain(expected)
  })

  test("answers a foreground model question through its stable RPC id", async () => {
    const fake = fakeTransport()
    const setup = await testRender(<PkApp transport={fake.transport} workspace="/tmp/pk" />, { width: 100, height: 30 })
    openRenderers.push(setup)
    await setup.waitForFrame((frame) => frame.includes("Message"))
    act(() => fake.emit({ version: 1, id: "ask-call", type: "tool_call", payload: { call_id: "ask-call", name: "AskUser", state: "awaiting", arguments_preview: { question: "Which package manager should I use?", choices: ["Bun", "npm"] } } }))
    act(() => fake.emit({ version: 1, id: "turn-request", type: "question", payload: { id: "ask-17", kind: "question", text: "Which package manager should I use?", choices: ["Bun", "npm"] } }))
    const questionFrame = await setup.waitForFrame((frame) => frame.includes("Which package manager"))
    expect(questionFrame).toContain("Bun")
    await act(async () => { await setup.mockInput.typeText("npm") })
    await setup.flush()
    act(() => setup.mockInput.pressEnter())
    await setup.flush()
    expect(fake.sent.some((item) => item.type === "answer_question" && item.payload?.id === "ask-17" && item.payload?.answer === "npm")).toBe(true)
    expect(fake.sent.some((item) => item.type === "prompt")).toBe(false)
    const pending = await setup.waitForFrame((frame) => frame.includes("Sending answer…"))
    expect(pending).toContain("Which package manager")
    act(() => fake.emit({ version: 1, id: "answer-ack", type: "question_answered", payload: { id: "ask-17" } }))
    const accepted = await setup.waitForFrame((frame) => frame.includes("Answer · npm"))
    expect(accepted).not.toContain("Sending answer…")
    expect(accepted).toContain("Ask pk to inspect")
    expect(accepted).toContain("AskUser · Which package manager should I use?")
    expect(accepted.indexOf("AskUser · Which package manager")).toBeLessThan(accepted.indexOf("Answer · npm"))
    expect(accepted).not.toContain("arguments_preview")
    expect(accepted).not.toContain("[object Object]")
    act(() => fake.emit({ version: 1, id: "turn-request-2", type: "question", payload: { id: "confirm-18", kind: "confirmation", text: "Apply the change?" } }))
    await setup.waitForFrame((frame) => frame.includes("Confirmation needed"))
    act(() => setup.mockInput.pressKey("ARROW_DOWN"))
    await setup.flush()
    act(() => setup.mockInput.pressEnter())
    await setup.flush()
    expect(fake.sent.some((item) => item.type === "answer_question" && item.payload?.id === "confirm-18" && item.payload?.answer === "No")).toBe(true)
  })

  test("mouse click answers the selected model question option", async () => {
    const fake = fakeTransport()
    const setup = await testRender(<PkApp transport={fake.transport} workspace="/tmp/pk" />, { width: 100, height: 30 })
    openRenderers.push(setup)
    await setup.waitForFrame((frame) => frame.includes("Message"))
    act(() => fake.emit({ version: 1, id: "question", type: "question", payload: { id: "mouse-choice", text: "Pick one", choices: ["Mint", "Blue"] } }))
    const frame = await setup.waitForFrame((value) => value.includes("Pick one"))
    const row = frame.split("\n").findIndex((line) => line.includes("Blue"))
    await act(async () => setup.mockMouse.click(frame.split("\n")[row]!.indexOf("Blue"), row))
    await setup.flush()
    expect(fake.sent.some((item) => item.type === "answer_question" && item.payload?.id === "mouse-choice" && item.payload?.answer === "Blue")).toBe(true)
    expect(setup.captureCharFrame()).toContain("Sending answer…")
  })

})
