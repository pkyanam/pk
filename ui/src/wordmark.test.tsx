import { afterEach, describe, expect, spyOn, test } from "bun:test"
import { testRender } from "@opentui/react/test-utils"
import { act } from "react"
import { PK_WORDMARK_LINES, reducedMotionEnabled, WORDMARK_INTRO_MS, Wordmark, wordmarkColorAt, wordmarkSweepPosition } from "./wordmark"

const renderers: Array<Awaited<ReturnType<typeof testRender>>> = []

afterEach(() => {
  for (const item of renderers.splice(0)) act(() => item.renderer.destroy())
})

describe("Wordmark", () => {
  test("keeps the complete pk letters readable while the mint color animates", async () => {
    const setup = await testRender(<Wordmark color="#8ab4a1" />, { width: 80, height: 24 })
    renderers.push(setup)
    const frame = await setup.waitForFrame((value) => value.includes("| .__/|_|\\_"))
    expect((setup.renderer as any).targetFps).toBe(60)
    for (const line of PK_WORDMARK_LINES) expect(frame).toContain(line.trim())
    const middle = WORDMARK_INTRO_MS * 0.43
    const movingColumn = wordmarkSweepPosition(middle)
    expect(movingColumn % 1).not.toBe(0)
    expect(wordmarkColorAt("#8ab4a1", 0, 0)).toBe("#8ab4a1")
    expect(wordmarkColorAt("#8ab4a1", middle, Math.round(movingColumn))).not.toBe("#8ab4a1")
    expect(wordmarkColorAt("#8ab4a1", middle, Math.round(movingColumn))).not.toBe(wordmarkColorAt("#8ab4a1", middle + 16, Math.round(movingColumn)))
    expect(wordmarkColorAt("#8ab4a1", WORDMARK_INTRO_MS, 8)).toBe("#8ab4a1")
  })

  test("respects the reduced-motion environment switch", async () => {
    expect(reducedMotionEnabled("1")).toBe(true)
    expect(reducedMotionEnabled("true")).toBe(true)
    expect(reducedMotionEnabled("0")).toBe(false)
    const previous = process.env.PK_REDUCED_MOTION
    process.env.PK_REDUCED_MOTION = "1"
    const setup = await testRender(<Wordmark color="#8ab4a1" />, { width: 80, height: 24 })
    renderers.push(setup)
    const renderer = setup.renderer as any
    expect(renderer.targetFps).toBe(30)
    await setup.waitForFrame((frame) => frame.includes("| .__/|_|\\_"))
    if (previous === undefined) delete process.env.PK_REDUCED_MOTION
    else process.env.PK_REDUCED_MOTION = previous
  })

  test("clears the scheduled animation frame on unmount", async () => {
    const clear = spyOn(globalThis, "clearTimeout")
    const setup = await testRender(<Wordmark color="#8ab4a1" />, { width: 80, height: 24 })
    renderers.push(setup)
    await setup.waitForFrame((frame) => frame.includes("| .__/|_|\\_"))
    await act(async () => setup.renderer.destroy())
    expect(clear).toHaveBeenCalled()
    clear.mockRestore()
  })

  test("settles to the exact accent after its brief intro", async () => {
    const setup = await testRender(<Wordmark color="#8ab4a1" />, { width: 80, height: 24 })
    renderers.push(setup)
    await act(async () => { await new Promise((resolve) => setTimeout(resolve, WORDMARK_INTRO_MS + 80)) })
    expect((setup.renderer as any).targetFps).toBe(30)
    expect(setup.captureCharFrame()).toContain("| .__/|_|\\_")
  })
})
