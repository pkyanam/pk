import { afterEach, beforeEach, describe, expect, test } from "bun:test"
import { destroyTreeSitterClient, getTreeSitterClient } from "@opentui/core"
import { testRender } from "@opentui/react/test-utils"
import { act } from "react"
import type { ServerEvent } from "./protocol"
import { PkApp } from "./app"
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
  const sent: Array<{ type: string; payload?: Record<string, unknown> }> = []
  let sequence = 0
  const transport = {
    setEventHandler(next: typeof handler) { handler = next },
    async start() {},
    send(type: string, payload?: Record<string, unknown>) { sent.push({ type, payload }); return `fake-${++sequence}` },
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
    expect(setup.captureCharFrame()).toContain("Thinking…")

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

  test("renders headings, lists, code fences, and links in markdown replies", async () => {
    const fake = fakeTransport()
    const setup = await testRender(<PkApp transport={fake.transport} workspace="/tmp/pk" />, { width: 120, height: 36 })
    openRenderers.push(setup)
    await setup.waitForFrame((frame) => frame.includes("Message"))
    act(() => fake.emit({ version: 1, id: "markdown", type: "assistant", payload: { text: "# Heading\n\n- List item\n\n```sh\nprintf hello\n```\n\n[Docs](https://example.com)" } }))
    const frame = await setup.waitForFrame((value) => value.includes("Heading"))
    expect(frame).toContain("List item")
    expect(frame).toContain("printf hello")
    expect(frame).toContain("Docs")
    expect(frame).not.toContain("# Heading")
  })

  test("answers a foreground model question through its stable RPC id", async () => {
    const fake = fakeTransport()
    const setup = await testRender(<PkApp transport={fake.transport} workspace="/tmp/pk" />, { width: 100, height: 30 })
    openRenderers.push(setup)
    await setup.waitForFrame((frame) => frame.includes("Message"))
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
