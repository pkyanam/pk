import { afterEach, describe, expect, test } from "bun:test"
import { testRender } from "@opentui/react/test-utils"
import { act, useState } from "react"
import type { ServerEvent } from "./protocol"
import { ManagedSession, SessionManager } from "./session-manager"

const renderers: Array<Awaited<ReturnType<typeof testRender>>> = []
let publish: (event: ServerEvent) => void = () => {}
let sent: Array<{ id: string; type: string; payload?: Record<string, unknown> }> = []
let loaded: ManagedSession[] = []
let sequence = 0
let closePanel = () => {}
let reopenPanel = () => {}

function Harness() {
  const [event, setEvent] = useState<ServerEvent>()
  const [open, setOpen] = useState(true)
  publish = setEvent
  closePanel = () => setOpen(false)
  reopenPanel = () => setOpen(true)
  return <SessionManager open={open} onClose={() => setOpen(false)} event={event} onLoad={(session) => loaded.push(session)} send={(type, payload) => {
    const id = `request-${++sequence}`
    sent.push({ id, type, payload })
    return id
  }} />
}

afterEach(() => {
  for (const item of renderers.splice(0)) act(() => item.renderer.destroy())
  publish = () => {}
  sent = []
  loaded = []
  sequence = 0
  closePanel = () => {}
  reopenPanel = () => {}
})

async function setupPanel(width = 120, height = 36) {
  const setup = await testRender(<Harness />, { width, height })
  renderers.push(setup)
  await setup.waitForFrame((frame) => frame.includes("Saved sessions"))
  return setup
}

function emit(id: string, type: string, payload: Record<string, unknown>) {
  act(() => publish({ version: 1, id, type, payload }))
}

const sampleSession: ManagedSession = {
  id: "session-a", title: "Release planning", preview: "Review the checklist", workspace: "/work/pk",
  created_at: "2026-09-22T12:00:00Z", updated_at: "2026-09-23T12:00:00Z", item_count: 8, active: false,
}

describe("SessionManager", () => {
  test("loads an inactive session from the searchable session list", async () => {
    const setup = await setupPanel()
    const list = sent.find((item) => item.type === "sessions_list")!
    emit(list.id, "sessions", { sessions: [sampleSession] })
    await setup.waitForFrame((frame) => frame.includes("Release planning"))
    await act(async () => setup.mockInput.pressEnter())
    expect(loaded).toEqual([sampleSession])
  })

  test("arrow down selects and loads the second session", async () => {
    const setup = await setupPanel()
    const list = sent.find((item) => item.type === "sessions_list")!
    const second = { ...sampleSession, id: "session-b", title: "Build notes" }
    emit(list.id, "sessions", { sessions: [sampleSession, second] })
    await setup.waitForFrame((frame) => frame.includes("Build notes"))
    await act(async () => setup.mockInput.pressKey("ARROW_DOWN"))
    await setup.flush()
    await act(async () => setup.mockInput.pressEnter())
    expect(loaded).toEqual([second])
  })

  test("mouse Open action resumes the focused session without toggling its bulk selection", async () => {
    const setup = await setupPanel()
    const list = sent.find((item) => item.type === "sessions_list")!
    const second = { ...sampleSession, id: "session-b", title: "Build notes" }
    emit(list.id, "sessions", { sessions: [sampleSession, second] })
    const frame = await setup.waitForFrame((value) => value.includes("Build notes"))
    const row = frame.split("\n").findIndex((line) => line.includes("Build notes"))
    await act(async () => setup.mockMouse.click(frame.split("\n")[row]!.indexOf("Build notes"), row))
    await setup.flush()
    expect(setup.captureCharFrame()).toContain("[x] Build notes")
    const openFrame = setup.captureCharFrame()
    const openRow = openFrame.split("\n").findIndex((line) => line.includes("Open session · click"))
    expect(openRow).toBeGreaterThanOrEqual(0)
    await act(async () => setup.mockMouse.click(openFrame.split("\n")[openRow]!.indexOf("Open session"), openRow))
    expect(loaded).toEqual([second])
  })

  test("Open action remains visible in a compact 80 by 24 terminal", async () => {
    const setup = await setupPanel(80, 24)
    const list = sent.find((item) => item.type === "sessions_list")!
    emit(list.id, "sessions", { sessions: [sampleSession] })
    const frame = await setup.waitForFrame((value) => value.includes("Release planning"))
    expect(frame).toContain("Open session")
  })

  test("mouse Open stays disabled for the active session", async () => {
    const setup = await setupPanel()
    const list = sent.find((item) => item.type === "sessions_list")!
    emit(list.id, "sessions", { sessions: [{ ...sampleSession, active: true }] })
    const frame = await setup.waitForFrame((value) => value.includes("Already open"))
    const row = frame.split("\n").findIndex((line) => line.includes("Already open"))
    await act(async () => setup.mockMouse.click(frame.split("\n")[row]!.indexOf("Already open"), row))
    expect(loaded).toEqual([])
  })

  test("mouse Open stays disabled while archive is in progress", async () => {
    const setup = await setupPanel()
    const list = sent.find((item) => item.type === "sessions_list")!
    emit(list.id, "sessions", { sessions: [sampleSession] })
    let frame = await setup.waitForFrame((value) => value.includes("Release planning"))
    let row = frame.split("\n").findIndex((line) => line.includes("Release planning"))
    await act(async () => setup.mockMouse.click(frame.split("\n")[row]!.indexOf("Release planning"), row))
    await setup.flush()
    await act(async () => setup.mockInput.pressKey("a"))
    expect(sent.some((item) => item.type === "sessions_archive")).toBe(true)
    frame = await setup.waitForFrame((value) => value.includes("Open session · wait for operation"))
    row = frame.split("\n").findIndex((line) => line.includes("Open session · wait for operation"))
    await act(async () => setup.mockMouse.click(frame.split("\n")[row]!.indexOf("Open session"), row))
    expect(loaded).toEqual([])
  })

  test("reopening clears the previous search query before listing again", async () => {
    const setup = await setupPanel()
    await act(async () => setup.mockInput.pressKey("/"))
    await setup.flush()
    const search = (setup.renderer.root as any).findDescendantById("session-search")
    act(() => { search.value = "stale-filter"; search.submit() })
    await setup.flush()
    expect(sent.filter((item) => item.type === "sessions_list").at(-1)?.payload?.query).toBe("stale-filter")
    act(() => closePanel())
    await setup.flush()
    act(() => reopenPanel())
    await setup.flush()
    expect(sent.filter((item) => item.type === "sessions_list").at(-1)?.payload?.query).toBe("")
    expect((setup.renderer.root as any).findDescendantById("session-search")?.value).toBe("")
  })

  test("archives selected sessions into recoverable trash and reports the result", async () => {
    const setup = await setupPanel()
    const list = sent.find((item) => item.type === "sessions_list")!
    emit(list.id, "sessions", { sessions: [sampleSession] })
    const sessionFrame = await setup.waitForFrame((frame) => frame.includes("Release planning"))
    const rowY = sessionFrame.split("\n").findIndex((line) => line.includes("Release planning"))
    const rowX = sessionFrame.split("\n")[rowY]!.indexOf("Release planning")
    await act(async () => setup.mockMouse.click(rowX, rowY))
    act(() => setup.mockInput.pressKey("a"))
    const archive = sent.find((item) => item.type === "sessions_archive")!
    expect(archive.payload?.session_ids).toEqual(["session-a"])
    emit(archive.id, "sessions_archived", { results: [{ session_id: "session-a", trash_id: "trash-a", ok: true }] })
    await setup.waitForFrame((frame) => frame.includes("recoverable from Trash"))
  })

  test("requires an explicit confirmation before permanent purge", async () => {
    const setup = await setupPanel()
    const list = sent.find((item) => item.type === "sessions_list")!
    emit(list.id, "sessions", { sessions: [] })
    await act(async () => setup.mockInput.pressKey("2"))
    const trashList = sent.filter((item) => item.type === "sessions_trash_list").at(-1)!
    emit(trashList.id, "sessions_trash", { sessions: [{ trash_id: "trash-a", session_id: "session-a", archived_at: "2026-09-23T12:00:00Z", title: "Old session", state: "archived" }] })
    const trashFrame = await setup.waitForFrame((frame) => frame.includes("Old session"))
    const rowY = trashFrame.split("\n").findIndex((line) => line.includes("Old session"))
    const rowX = trashFrame.split("\n")[rowY]!.indexOf("Old session")
    await act(async () => setup.mockMouse.click(rowX, rowY))
    act(() => setup.mockInput.pressKey("p"))
    await setup.waitForFrame((frame) => frame.includes("This cannot be undone"))
    expect(sent.some((item) => item.type === "sessions_purge")).toBe(false)
    await act(async () => setup.mockInput.pressEscape())
    expect(sent.some((item) => item.type === "sessions_purge")).toBe(false)
    act(() => setup.mockInput.pressKey("p"))
    await setup.waitForFrame((frame) => frame.includes("This cannot be undone"))
    act(() => setup.mockInput.pressKey("y"))
    const purge = sent.find((item) => item.type === "sessions_purge")!
    expect(purge.payload?.trash_ids).toEqual(["trash-a"])
  })

  test("right mouse button cannot confirm permanent purge", async () => {
    const setup = await setupPanel()
    const list = sent.find((item) => item.type === "sessions_list")!
    emit(list.id, "sessions", { sessions: [] })
    await act(async () => setup.mockInput.pressKey("2"))
    const trashList = sent.filter((item) => item.type === "sessions_trash_list").at(-1)!
    emit(trashList.id, "sessions_trash", { sessions: [{ trash_id: "trash-a", session_id: "session-a", archived_at: "2026-09-23T12:00:00Z", title: "Old session", state: "archived" }] })
    let frame = await setup.waitForFrame((value) => value.includes("Old session"))
    let row = frame.split("\n").findIndex((line) => line.includes("Old session"))
    await act(async () => setup.mockMouse.click(frame.split("\n")[row]!.indexOf("Old session"), row))
    await act(async () => setup.mockInput.pressKey("p"))
    frame = await setup.waitForFrame((value) => value.includes("Delete permanently · Y"))
    row = frame.split("\n").findIndex((line) => line.includes("Delete permanently · Y"))
    await act(async () => setup.mockMouse.click(frame.split("\n")[row]!.indexOf("Delete permanently"), row, 2))
    expect(sent.some((item) => item.type === "sessions_purge")).toBe(false)
    expect(setup.captureCharFrame()).toContain("This cannot be undone")
  })

  test("restores selected trashed sessions", async () => {
    const setup = await setupPanel()
    const list = sent.find((item) => item.type === "sessions_list")!
    emit(list.id, "sessions", { sessions: [] })
    await act(async () => setup.mockInput.pressKey("2"))
    const trashList = sent.filter((item) => item.type === "sessions_trash_list").at(-1)!
    emit(trashList.id, "sessions_trash", { sessions: [{ trash_id: "trash-a", session_id: "session-a", archived_at: "2026-09-23T12:00:00Z", title: "Old session", state: "archived" }] })
    const frame = await setup.waitForFrame((value) => value.includes("Old session"))
    const rowY = frame.split("\n").findIndex((line) => line.includes("Old session"))
    const rowX = frame.split("\n")[rowY]!.indexOf("Old session")
    await act(async () => setup.mockMouse.click(rowX, rowY))
    act(() => setup.mockInput.pressKey("r"))
    const restore = sent.find((item) => item.type === "sessions_restore")!
    expect(restore.payload?.trash_ids).toEqual(["trash-a"])
    emit(restore.id, "sessions_restored", { results: [{ session_id: "session-a", ok: true }] })
    await setup.waitForFrame((value) => value.includes("Restored 1 session"))
  })
})
