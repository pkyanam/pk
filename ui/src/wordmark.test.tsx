import { afterEach, describe, expect, spyOn, test } from "bun:test"
import { testRender } from "@opentui/react/test-utils"
import { act } from "react"
import { PK_WORDMARK_LINES, reducedMotionEnabled, WORDMARK_FRAME_MS, WORDMARK_LOOP_MS, Wordmark, wordmarkColorAt, wordmarkSweepPosition } from "./wordmark"

const renderers: Array<Awaited<ReturnType<typeof testRender>>> = []

afterEach(() => {
  for (const item of renderers.splice(0)) act(() => item.renderer.destroy())
})

describe("Wordmark", () => {
  test("keeps the complete pk silhouette readable through a repeating metallic shimmer", async () => {
    const setup = await testRender(<Wordmark color="#8ab4a1" />, { width: 80, height: 24 })
    renderers.push(setup)
    const frame = await setup.waitForFrame((value) => value.includes("########   ######"))
    expect((setup.renderer as any).targetFps).toBe(60)
    for (const line of PK_WORDMARK_LINES) expect(frame).toContain(line.trim())
    expect(PK_WORDMARK_LINES[0]).toBe("########   ##     ##")
    expect(PK_WORDMARK_LINES[3]).toBe("########   ######   ")

    const t = 1100
    const movingColumn = wordmarkSweepPosition(t)
    expect(movingColumn % 1).not.toBe(0)
    expect(wordmarkColorAt("#8ab4a1", t, Math.round(movingColumn))).not.toBe(wordmarkColorAt("#8ab4a1", t + 800, Math.round(movingColumn)))
    expect(wordmarkColorAt("#8ab4a1", t, Math.round(movingColumn))).toBe(wordmarkColorAt("#8ab4a1", t + WORDMARK_LOOP_MS, Math.round(movingColumn)))
    expect(wordmarkColorAt("#8ab4a1", t, 3, 0)).not.toBe(wordmarkColorAt("#8ab4a1", t, 3, 4))
  })

  test("keeps animating after the original intro window", async () => {
    const setup = await testRender(<Wordmark color="#8ab4a1" />, { width: 80, height: 24 })
    renderers.push(setup)
    await act(async () => { await new Promise((resolve) => setTimeout(resolve, 850)) })
    expect((setup.renderer as any).targetFps).toBe(60)
    expect(setup.captureCharFrame()).toContain("########   ######")
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
    await setup.waitForFrame((frame) => frame.includes("########   ######"))
    if (previous === undefined) delete process.env.PK_REDUCED_MOTION
    else process.env.PK_REDUCED_MOTION = previous
  })

  test("clears the scheduled animation timer and restores renderer FPS on unmount", async () => {
    const clear = spyOn(globalThis, "clearTimeout")
    const setup = await testRender(<Wordmark color="#8ab4a1" />, { width: 80, height: 24 })
    renderers.push(setup)
    await setup.waitForFrame((frame) => frame.includes("########   ######"))
    await act(async () => setup.renderer.destroy())
    expect(clear).toHaveBeenCalled()
    expect((setup.renderer as any).targetFps).toBe(30)
    clear.mockRestore()
  })

  test("has a bounded frame cadence", () => {
    expect(WORDMARK_FRAME_MS).toBeGreaterThanOrEqual(16)
    expect(WORDMARK_FRAME_MS).toBeLessThan(17)
  })
})
