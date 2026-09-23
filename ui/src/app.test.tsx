import { afterEach, describe, expect, test } from "bun:test"
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
    act(() => fake.emit({
      version: 1, id: "tool-finished", type: "tool_call",
      payload: { call_id: "call-1", name: "Bash", state: "completed", command_preview: "bun test", arguments_preview: { cmd: "bun test" }, elapsed_ms: 42000, operations: [{ type: "shell", state: "completed", output_excerpt: "6 pass", exit_code: 0 }] },
    }))
    act(() => fake.emit({ version: 1, id: "turn-end", type: "turn_finished", payload: {} }))
    const frame = await setup.waitForFrame((value) => value.includes("Bash · completed") && value.includes("6 pass"))
    expect(frame).toContain("Bash · completed")
    expect(frame).toContain("6 pass")
    expect(frame).toContain("bun test")
    expect(frame).not.toContain("[object Object]")
    expect(frame.indexOf("Bash · completed")).toBeLessThan(frame.lastIndexOf("pk"))
  })
})
