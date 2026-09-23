import { afterEach, describe, expect, test } from "bun:test"
import { testRender } from "@opentui/react/test-utils"
import { act, useState } from "react"
import type { ServerEvent } from "./protocol"
import { MCPManager } from "./mcp-manager"

const renderers: Array<Awaited<ReturnType<typeof testRender>>> = []
let publish: (event: ServerEvent) => void = () => {}
let sent: Array<{ id: string; type: string; payload?: Record<string, unknown> }> = []
let sequence = 0

function Harness() {
  const [event, setEvent] = useState<ServerEvent>()
  publish = setEvent
  return <MCPManager open onClose={() => {}} event={event} send={(type, payload) => {
    const id = `request-${++sequence}`
    sent.push({ id, type, payload })
    return id
  }} />
}

afterEach(() => {
  for (const item of renderers.splice(0)) act(() => item.renderer.destroy())
  publish = () => {}
  sent = []
  sequence = 0
})

async function setupPanel() {
  const setup = await testRender(<Harness />, { width: 120, height: 40 })
  renderers.push(setup)
  await setup.waitForFrame((frame) => frame.includes("MCP connections"))
  return setup
}

function emit(id: string, type: string, payload: Record<string, unknown>) {
  act(() => publish({ version: 1, id, type, payload }))
}

describe("MCPManager", () => {
  test("opening only lists configuration and shows safe summaries", async () => {
    const setup = await setupPanel()
    expect(sent.map((item) => item.type)).toEqual(["mcp_list"])
    const list = sent[0]!
    emit(list.id, "mcp_catalog", { servers: [{ id: "notes", transport: "http", url: "https://mcp.example.test/api", auth_mode: "bearer_env", auth_status: "configured", credential_env: ["MCP_NOTES_TOKEN"], command: "", arguments_count: 0, environment_keys: [] }, { id: "calendar", transport: "stdio", command: "/bin/server", arguments_count: 1, environment_keys: [] }], tools: [{ server_id: "notes", name: "mcp_notes_search_12345678", description: "Search notes" }] })
    const frame = await setup.waitForFrame((value) => value.includes("MCP_NOTES_TOKEN"))
    expect(frame).toContain("notes")
    expect(frame).toContain("MCP_NOTES_TOKEN")
    expect(frame).not.toContain("secret-value")
    expect(sent.some((item) => item.type === "mcp_login" || item.type === "mcp_add")).toBe(false)
    await act(async () => setup.mockInput.pressKey("ARROW_DOWN"))
    await setup.flush()
    expect(setup.captureCharFrame()).toContain("calendar · Local stdio")
  })

  test("adds a remote endpoint with a one-shot masked bearer credential", async () => {
    const setup = await setupPanel()
    const list = sent.find((item) => item.type === "mcp_list")!
    emit(list.id, "mcp_catalog", { servers: [], tools: [] })
    await act(async () => setup.mockInput.pressKey("a"))
    await setup.flush()
    await act(async () => setup.mockInput.pressKey("ARROW_RIGHT"))
    await setup.flush()
    await act(async () => setup.mockInput.pressTab())
    await setup.flush()
    await act(async () => { await setup.mockInput.typeText("remote") })
    await act(async () => setup.mockInput.pressTab())
    await setup.flush()
    await act(async () => { await setup.mockInput.typeText("https://mcp.example.test/api") })
    await act(async () => setup.mockInput.pressTab())
    await setup.flush()
    for (let index = 0; index < 4; index++) await act(async () => setup.mockInput.pressKey("ARROW_RIGHT"))
    await setup.flush()
    await act(async () => setup.mockInput.pressTab())
    await setup.flush()
    const credential = "never-display-this-token"
    await act(async () => { await setup.mockInput.typeText(credential) })
    await setup.flush()
    const maskedFrame = setup.captureCharFrame()
    expect(maskedFrame).not.toContain(credential)
    expect(maskedFrame).toContain("••••••")
    const maskRow = maskedFrame.split("\n").findIndex((line) => line.includes("••••"))
    const maskColumn = maskedFrame.split("\n")[maskRow]!.indexOf("••••")
    await act(async () => {
      await setup.mockMouse.pressDown(maskColumn, maskRow)
      await setup.mockMouse.moveTo(maskColumn + 8, maskRow)
      await setup.mockMouse.release(maskColumn + 8, maskRow)
    })
    const selected = setup.renderer.getSelection()?.selectedRenderables?.map((node: any) => node.getSelectedText()).join("") ?? ""
    expect(selected).not.toContain(credential)
    await act(async () => setup.mockInput.pressTab())
    await act(async () => setup.mockInput.pressEnter())
    const add = sent.find((item) => item.type === "mcp_add")
    expect(add?.payload?.credential_kind).toBe("bearer")
    expect(add?.payload?.secret).toBe(credential)
    expect(add?.payload?.server).toEqual({ id: "remote", url: "https://mcp.example.test/api" })
    expect(setup.captureCharFrame()).not.toContain(credential)
    emit(add!.id, "mcp_updated", { next_session_only: true, servers: [{ id: "remote", transport: "http", url: "https://mcp.example.test/api", auth_mode: "bearer_secret", auth_status: "configured", command: "", arguments_count: 0, environment_keys: [] }], tools: [] })
    await setup.waitForFrame((frame) => frame.includes("use /new to activate"))
    expect(setup.captureCharFrame()).not.toContain(credential)
    expect((setup.renderer.root as any).findDescendantById("mcp-secret-input")).toBeUndefined()
  })

  test("adds a local stdio server with argument boundaries preserved", async () => {
    const setup = await setupPanel()
    const list = sent.find((item) => item.type === "mcp_list")!
    emit(list.id, "mcp_catalog", { servers: [], tools: [] })
    await act(async () => setup.mockInput.pressKey("a"))
    await setup.flush()
    await act(async () => setup.mockInput.pressTab())
    await act(async () => { await setup.mockInput.typeText("local") })
    await act(async () => setup.mockInput.pressTab())
    await act(async () => { await setup.mockInput.typeText("/usr/local/bin/mcp-server") })
    await act(async () => setup.mockInput.pressTab())
    await act(async () => { await setup.mockInput.typeText('serve --label "My Server"') })
    await act(async () => setup.mockInput.pressTab())
    await act(async () => setup.mockInput.pressTab())
    await act(async () => setup.mockInput.pressEnter())
    const add = sent.find((item) => item.type === "mcp_add")
    expect(add?.payload?.server).toEqual({ id: "local", command: "/usr/local/bin/mcp-server", args: ["serve", "--label", "My Server"], env: {} })
    expect(JSON.stringify(add?.payload)).not.toContain("shell")
  })

  test("Tab follows the visible form order and Enter submits only from Save", async () => {
    const setup = await setupPanel()
    const list = sent.find((item) => item.type === "mcp_list")!
    emit(list.id, "mcp_catalog", { servers: [], tools: [] })
    await act(async () => setup.mockInput.pressKey("a"))
    await setup.flush()

    await act(async () => setup.mockInput.pressTab())
    await setup.flush()
    expect(setup.captureCharFrame()).toContain("› Server ID")
    await act(async () => { await setup.mockInput.typeText("local") })
    await act(async () => setup.mockInput.pressEnter())
    expect(sent.some((item) => item.type === "mcp_add")).toBe(false)
    await setup.flush()
    expect(setup.captureCharFrame()).toContain("› Server ID")

    await act(async () => setup.mockInput.pressTab({ shift: true }))
    await setup.flush()
    expect(setup.captureCharFrame()).toContain("› Connection type")
    await act(async () => setup.mockInput.pressTab()) // ID
    await act(async () => setup.mockInput.pressTab()) // executable
    await act(async () => { await setup.mockInput.typeText("/bin/echo") })
    await act(async () => setup.mockInput.pressTab()) // args
    await act(async () => setup.mockInput.pressTab()) // working directory
    await act(async () => setup.mockInput.pressTab()) // Save
    await setup.flush()
    expect(setup.captureCharFrame()).toContain("Enter saves · Esc cancels")
    await act(async () => setup.mockInput.pressEnter())
    expect(sent.find((item) => item.type === "mcp_add")?.payload?.server).toEqual({ id: "local", command: "/bin/echo", args: [], env: {} })
  })

  test("invalid Save leaves the form open and does not send a request", async () => {
    const setup = await setupPanel()
    const list = sent.find((item) => item.type === "mcp_list")!
    emit(list.id, "mcp_catalog", { servers: [], tools: [] })
    await setup.waitForFrame((frame) => frame.includes("No servers configured"))
    await act(async () => setup.mockInput.pressKey("a"))
    await setup.flush()
    for (let index = 0; index < 5; index++) await act(async () => setup.mockInput.pressTab())
    await setup.flush()
    expect(setup.captureCharFrame()).toContain("Enter saves · Esc cancels")
    await act(async () => setup.mockInput.pressEnter())
    await setup.flush()
    expect(sent.some((item) => item.type === "mcp_add")).toBe(false)
    expect(setup.captureCharFrame()).toContain("Save server")
    expect(setup.captureCharFrame()).toContain("› Server ID")
    expect(setup.captureCharFrame()).toContain("Enter a server ID and executable path.")
  })

  test("mouse activates Save and Cancel only with the left button", async () => {
    const setup = await setupPanel()
    const list = sent.find((item) => item.type === "mcp_list")!
    emit(list.id, "mcp_catalog", { servers: [], tools: [] })
    await setup.waitForFrame((frame) => frame.includes("No servers configured"))
    await act(async () => setup.mockInput.pressKey("a"))
    await setup.flush()
    await act(async () => setup.mockInput.pressTab())
    await act(async () => { await setup.mockInput.typeText("clicked") })
    await setup.flush()
    await act(async () => setup.mockInput.pressTab())
    await setup.flush()
    await act(async () => { await setup.mockInput.typeText("/bin/echo") })
    await setup.flush()
    const frame = setup.captureCharFrame()
    expect(frame).toContain("/bin/echo")
    const row = frame.split("\n").findIndex((line) => line.includes("Save server"))
    const column = frame.split("\n")[row]!.indexOf("Save server")
    await act(async () => setup.mockMouse.click(column, row, 2))
    expect(sent.some((item) => item.type === "mcp_add")).toBe(false)
    await act(async () => setup.mockMouse.click(column, row, 0))
    await setup.flush()
    const add = sent.find((item) => item.type === "mcp_add")!
    expect(add).toBeDefined()
    emit(add.id, "mcp_updated", { servers: [], tools: [] })
    await setup.waitForFrame((value) => value.includes("No servers configured"))

    await act(async () => setup.mockInput.pressKey("a"))
    const cancelFrame = await setup.waitForFrame((value) => value.includes("Cancel · Esc"))
    const cancelRow = cancelFrame.split("\n").findIndex((line) => line.includes("Cancel · Esc"))
    const cancelColumn = cancelFrame.split("\n")[cancelRow]!.indexOf("Cancel · Esc")
    await act(async () => setup.mockMouse.click(cancelColumn, cancelRow, 0))
    expect(setup.captureCharFrame()).not.toContain("Connection type:")
  })
})
