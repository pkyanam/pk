import { afterEach, beforeAll, describe, expect, test } from "bun:test"
import { destroyTreeSitterClient, getTreeSitterClient } from "@opentui/core"
import { testRender } from "@opentui/react/test-utils"
import { act } from "react"
import { PkApp } from "./app"
import type { ServerEvent } from "./protocol"
import type { PkTransport } from "./transport"

const renderers: Array<Awaited<ReturnType<typeof testRender>>> = []

beforeAll(async () => {
  const client = getTreeSitterClient()
  await client.initialize()
  await client.preloadParser("markdown")
  await client.preloadParser("markdown_inline")
})

afterEach(() => {
  for (const setup of renderers.splice(0)) act(() => setup.renderer.destroy())
  return destroyTreeSitterClient()
})

function fakeTransport() {
  let receive = (_event: ServerEvent) => {}
  const transport = {
    setEventHandler(handler: typeof receive) { receive = handler },
    async start() {},
    send() { return "fixture-request" },
    async close() {},
  }
  return {
    transport: transport as unknown as PkTransport,
    emit(event: ServerEvent) { receive(event) },
  }
}

function largeAnswer(index: number, markdown: boolean, targetBytes = 32 * 1024) {
  const line = markdown ? `**Markdown answer ${index}** with a [link](https://example.test) and a bullet.\n\n` : `Plain answer ${index} with ordinary prose and a bullet-like sentence.\n\n`
  return `${line.repeat(Math.ceil(targetBytes / line.length)).slice(0, targetBytes)}\nEND-MARKER-${index}`
}

describe("large transcript renderer bounds", () => {
  test("keeps composer visible after large Markdown and plain-text responses, resize, and scroll", async () => {
    const fake = fakeTransport()
    const setup = await testRender(<PkApp transport={fake.transport} workspace="/tmp/pk-large-transcript" />, { width: 120, height: 36 })
    renderers.push(setup)
    await setup.waitForFrame((frame) => frame.includes("Ask pk to inspect"))
    act(() => fake.emit({ version: 1, type: "ready", payload: { model: "gpt-6-luna", effort: "medium" } }))

    const elapsed: Record<string, number> = {}
    for (const markdown of [true, false]) {
      const label = markdown ? "markdown" : "plain"
      const start = performance.now()
      await act(async () => {
        for (let index = 0; index < 4; index++) {
          fake.emit({ version: 1, id: `${label}-${index}`, type: "task_output", payload: { role: "assistant", text: largeAnswer(index, markdown) } })
        }
      })
      await setup.flush()
      elapsed[label] = performance.now() - start
      const frame = setup.captureCharFrame()
      const transcript = (setup.renderer.root as any).findDescendantById("transcript")
      expect(transcript.getChildren().length).toBeGreaterThanOrEqual(4)
      expect(frame).toContain("Ask pk to inspect")
    }

    await act(async () => setup.renderer.resize(80, 24))
    await setup.flush()
    let frame = setup.captureCharFrame()
    expect(frame.split("\n")).toHaveLength(25)
    expect(frame).toContain("Ask pk to inspect")
    const transcript = (setup.renderer.root as any).findDescendantById("transcript")
    const bottom = transcript.scrollTop
    await act(async () => setup.mockMouse.scroll(10, 8, "up"))
    await setup.flush()
    frame = setup.captureCharFrame()
    expect(frame).toContain("Ask pk to inspect")
    expect(transcript.scrollTop).toBeLessThanOrEqual(bottom)
    await act(async () => setup.mockMouse.scroll(10, 8, "down"))
    await setup.flush()
    expect(setup.captureCharFrame()).toContain("Ask pk to inspect")

    console.info(`large transcript render elapsed ms (4 × 32 KiB): markdown=${elapsed.markdown!.toFixed(1)} plain=${elapsed.plain!.toFixed(1)}`)
  })
})
