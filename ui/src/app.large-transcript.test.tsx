import { afterEach, beforeAll, describe, expect, test } from "bun:test"
import { destroyTreeSitterClient, getTreeSitterClient } from "@opentui/core"
import { testRender } from "@opentui/react/test-utils"
import { act } from "react"
import { PkApp } from "./app"
import type { ServerEvent } from "./protocol"
import type { PkTransport } from "./transport"

const renderers: Array<Awaited<ReturnType<typeof testRender>>> = []
const ANSWER_BYTES = 16 * 1024
const ANSWER_COUNT = 3

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

function largeAnswer(index: number, markdown: boolean) {
  const line = markdown
    ? `**Markdown answer ${index}** with a [link](https://example.test) and a bullet.\n\n`
    : `Plain answer ${index} with ordinary prose and a bullet-like sentence.\n\n`
  const repeated = line.repeat(Math.ceil(ANSWER_BYTES / line.length)).slice(0, ANSWER_BYTES)
  return `${repeated}\nEND-MARKER-${index}`
}

type Sample = { format: "markdown" | "plain"; initialMs: number; updateMs: number }

describe("large transcript renderer bounds", () => {
  test("keeps the composer visible and measures matched fresh-renderer samples", async () => {
    const samples: Sample[] = []

    // Alternate order to reduce order/warmup bias. Parser initialization and
    // grammar loading happen once above, while every condition gets a new app.
    for (const formats of [["markdown", "plain"], ["plain", "markdown"], ["markdown", "plain"]] as const) {
      for (const format of formats) {
        const markdown = format === "markdown"
        const fake = fakeTransport()
        const setup = await testRender(<PkApp transport={fake.transport} workspace="/tmp/pk-large-transcript" />, { width: 120, height: 36 })
        renderers.push(setup)
        await setup.waitForFrame((frame) => frame.includes("Ask pk to inspect"))
        act(() => fake.emit({ version: 1, type: "ready", payload: { model: "gpt-6-luna", effort: "medium" } }))

        const start = performance.now()
        await act(async () => {
          for (let index = 0; index < ANSWER_COUNT; index++) {
            fake.emit({ version: 1, id: `${format}-${index}`, type: "task_output", payload: { role: "assistant", text: largeAnswer(index, markdown) } })
          }
        })
        await setup.flush()
        const initialMs = performance.now() - start

        const transcript = (setup.renderer.root as any).findDescendantById("transcript")
        expect(transcript.getChildren().length).toBeGreaterThanOrEqual(ANSWER_COUNT)
        expect(setup.captureCharFrame()).toContain("Ask pk to inspect")

        // This changes app-level status state and re-renders the shell, while
        // leaving the transcript entries unchanged. It approximates unrelated
        // status/clock updates without waiting on a wall-clock timer.
        const updateStart = performance.now()
        act(() => fake.emit({ version: 1, type: "release_status", payload: { reload_available: true } }))
        await setup.flush()
        const updateMs = performance.now() - updateStart

        await act(async () => setup.renderer.resize(80, 24))
        await setup.flush()
        expect(setup.captureCharFrame().split("\n")).toHaveLength(25)
        expect(setup.captureCharFrame()).toContain("Ask pk to inspect")
        const bottom = transcript.scrollTop
        await act(async () => setup.mockMouse.scroll(10, 8, "up"))
        await setup.flush()
        expect(setup.captureCharFrame()).toContain("Ask pk to inspect")
        expect(transcript.scrollTop).toBeLessThanOrEqual(bottom)
        await act(async () => setup.mockMouse.scroll(10, 8, "down"))
        await setup.flush()
        expect(setup.captureCharFrame()).toContain("Ask pk to inspect")

        samples.push({ format, initialMs, updateMs })
        act(() => setup.renderer.destroy())
        renderers.splice(renderers.indexOf(setup), 1)
      }
    }

    const median = (values: number[]) => [...values].sort((a, b) => a - b)[Math.floor(values.length / 2)]!
    const markdown = samples.filter((sample) => sample.format === "markdown")
    const plain = samples.filter((sample) => sample.format === "plain")
    const summarize = (values: Sample[], key: "initialMs" | "updateMs") => ({ raw: values.map((sample) => Number(sample[key].toFixed(1))), median: Number(median(values.map((sample) => sample[key])).toFixed(1)) })
    console.info("large transcript diagnostic ms", JSON.stringify({ bytesPerAnswer: ANSWER_BYTES, answers: ANSWER_COUNT, order: samples.map((sample) => sample.format), markdown: { initial: summarize(markdown, "initialMs"), unrelatedUpdate: summarize(markdown, "updateMs") }, plain: { initial: summarize(plain, "initialMs"), unrelatedUpdate: summarize(plain, "updateMs") } }))
  })
})
