import { afterEach, beforeEach, describe, expect, test } from "bun:test"
import { destroyTreeSitterClient, getTreeSitterClient } from "@opentui/core"
import { testRender } from "@opentui/react/test-utils"
import { act } from "react"
import type { ServerEvent } from "./protocol"
import { PkApp } from "./app"
import type { PkTransport } from "./transport"

const renderers: Array<Awaited<ReturnType<typeof testRender>>> = []

afterEach(() => {
  for (const item of renderers.splice(0)) act(() => item.renderer.destroy())
  return destroyTreeSitterClient()
})

beforeEach(async () => {
  const client = getTreeSitterClient()
  await client.initialize()
  await client.preloadParser("markdown")
  await client.preloadParser("markdown_inline")
})

function makeTransport() {
  let receive = (_event: ServerEvent) => {}
  let nextID = 0
  const sent: Array<{ id: string; type: string; payload?: Record<string, unknown> }> = []
  const transport = {
    setEventHandler(handler: typeof receive) { receive = handler },
    async start() {},
    send(type: string, payload?: Record<string, unknown>) {
      const id = `stress-${++nextID}`
      sent.push({ id, type, payload })
      return id
    },
    emit(event: ServerEvent) { receive(event) },
    async close() {},
  }
  return { transport: transport as unknown as PkTransport, sent, emit: transport.emit }
}

describe("TUI long-turn recovery", () => {
  test("keeps rendering responsive after repeated progress, tools, finals, and resize", async () => {
    const fake = makeTransport()
    const setup = await testRender(<PkApp transport={fake.transport} workspace="/tmp/pk-stress" />, { width: 120, height: 36 })
    renderers.push(setup)
    await setup.waitForFrame((frame) => frame.includes("Ask pk to inspect"))
    act(() => fake.emit({ version: 1, type: "ready", payload: { model: "gpt-6-luna", effort: "low" } }))

    // Overflow the transcript's 300-row view cap while exercising the same
    // event mix that arrives during long tool-using turns.
    const turnCount = 14
    for (let turn = 0; turn < turnCount; turn++) {
      act(() => {
        fake.emit({ version: 1, id: `turn-${turn}`, type: "turn_started", payload: {} })
        fake.emit({ version: 1, id: `draft-${turn}`, type: "model_progress", payload: {
          phase: "assistant_delta", request_id: `request-${turn}`, attempt: 0, text_delta: `planning turn ${turn}`,
        } })
        fake.emit({ version: 1, id: `tool-${turn}`, type: "tool_call", payload: {
          call_id: `call-${turn}`, name: "Bash", state: "running", command_preview: "go test ./...",
        } })
        for (let step = 0; step < 24; step++) {
          fake.emit({ version: 1, id: `output-${turn}-${step}`, type: "task_output", payload: {
            role: "user", text: `Turn ${turn} progress record ${step}`,
          } })
        }
        fake.emit({ version: 1, id: `tool-done-${turn}`, type: "tool_call", payload: {
          call_id: `call-${turn}`, name: "Bash", state: "completed", command_preview: "go test ./...",
        } })
        fake.emit({ version: 1, id: `answer-${turn}`, type: "assistant", payload: { text: `Final answer for turn ${turn}` } })
        fake.emit({ version: 1, id: `turn-end-${turn}`, type: "turn_finished", payload: {} })
      })
      await setup.flush()
    }

    let frame = await setup.waitForFrame((value) => value.includes(`Final answer for turn ${turnCount - 1}`) && value.includes("Ready"))
    expect(frame).toContain("earlier activity omitted")
    expect(frame).toContain("Enter send")

    // Resize after the last final event, then verify both typing and an actual
    // mouse-selected slash command still work without another server event.
    await act(async () => setup.renderer.resize(80, 24))
    await setup.flush()
    frame = setup.captureCharFrame()
    expect(frame.split("\n")).toHaveLength(25)
    expect(frame).toContain("Ask pk to inspect")
    await act(async () => { await setup.mockInput.typeText("/") })
    await setup.flush()
    expect(setup.captureCharFrame()).toContain("/")
    await act(async () => setup.renderer.resize(120, 36))
    await setup.flush()
    frame = setup.captureCharFrame()
    const slashRow = frame.split("\n").findIndex((line) => line.includes("/model"))
    expect(slashRow).toBeGreaterThanOrEqual(0)
    await act(async () => setup.mockMouse.click(frame.split("\n")[slashRow]!.indexOf("/model"), slashRow))
    frame = await setup.waitForFrame((value) => value.includes("Select model"))
    expect(frame).toContain("Sol · balanced")
  })
})
