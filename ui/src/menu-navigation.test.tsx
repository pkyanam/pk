import { afterEach, describe, expect, test } from "bun:test"
import { act } from "react"
import { testRender } from "@opentui/react/test-utils"
import { hoverSlashMenu, moveSlashMenu } from "./app"
import type { PkTransport } from "./transport"
import type { ServerEvent } from "./protocol"

const renderers: Array<Awaited<ReturnType<typeof testRender>>> = []

afterEach(() => {
  for (const setup of renderers.splice(0)) act(() => setup.renderer.destroy())
})

function fakeTransport() {
  let handler = (_event: ServerEvent) => {}
  return {
    transport: {
      setEventHandler(next: typeof handler) { handler = next },
      async start() {},
      send() { return "menu-request" },
      async close() {},
    } as unknown as PkTransport,
  }
}

describe("slash menu navigation", () => {
  test("mouse scroll moves the menu and hovering its first visible row keeps that window", async () => {
    const { PkApp } = await import("./app")
    const setup = await testRender(<PkApp transport={fakeTransport().transport} workspace="/tmp/pk" />, { width: 100, height: 30 })
    renderers.push(setup)
    await setup.waitForFrame((frame) => frame.includes("Ask pk to inspect"))
    await act(async () => setup.mockInput.typeText("/"))
    const initial = await setup.waitForFrame((frame) => frame.includes("/model") && frame.includes("/plugins"))
    const initialRow = initial.split("\n").findIndex((line) => line.includes("/model"))
    const initialColumn = initial.split("\n")[initialRow]!.indexOf("/model")

    for (let step = 0; step < 8; step++) {
      await act(async () => setup.mockMouse.scroll(initialColumn, initialRow, "down"))
      await setup.flush()
    }
    const scrolled = setup.captureCharFrame()
    expect(scrolled).not.toContain("/model")
    const topRow = scrolled.split("\n").findIndex((line) => /│\s*\/\w+/.test(line))
    const firstVisible = scrolled.split("\n")[topRow]?.match(/\/\w+/)?.[0]
    expect(firstVisible).toBeTruthy()

    await act(async () => setup.mockMouse.moveTo(scrolled.split("\n")[topRow]!.indexOf(firstVisible!), topRow))
    await setup.flush()
    const hovered = setup.captureCharFrame()
    expect(hovered.split("\n").findIndex((line) => /│\s*\/\w+/.test(line))).toBe(topRow)
    expect(hovered).not.toContain("/model")

    for (let step = 0; step < 8; step++) {
      await act(async () => setup.mockMouse.scroll(initialColumn, initialRow, "up"))
      await setup.flush()
    }
    const scrolledUp = setup.captureCharFrame()
    const scrolledUpRow = scrolledUp.split("\n").findIndex((line) => /│\s*\/\w+/.test(line))
    const firstAfterUp = scrolledUp.split("\n")[scrolledUpRow]?.match(/\/\w+/)?.[0]
    expect(firstAfterUp).toBeTruthy()
    expect(firstAfterUp).not.toBe(firstVisible)
  })

  test("wheel scroll maintains an independent window when hovering its first row", () => {
    const commandCount = 30
    let state = moveSlashMenu(0, 0, 6, commandCount)
    expect(state).toEqual({ index: 6, offset: 1 })

    state = moveSlashMenu(state.index, state.offset, 1, commandCount)
    expect(state).toEqual({ index: 7, offset: 2 })
    state = moveSlashMenu(state.index, state.offset, -1, commandCount)
    expect(state).toEqual({ index: 6, offset: 2 })

    // Hovering the first visible row changes the highlight without scrolling
    // the command window back to the beginning.
    state = hoverSlashMenu(state.offset, state.offset, commandCount)
    expect(state).toEqual({ index: 2, offset: 2 })
  })

  test("keyboard movement keeps its highlighted command inside the visible window", () => {
    let state = moveSlashMenu(0, 0, 29, 30)
    expect(state).toEqual({ index: 29, offset: 24 })
    state = moveSlashMenu(state.index, state.offset, 1, 30)
    expect(state).toEqual({ index: 0, offset: 0 })
    state = moveSlashMenu(state.index, state.offset, -1, 30)
    expect(state).toEqual({ index: 29, offset: 24 })
  })
})
