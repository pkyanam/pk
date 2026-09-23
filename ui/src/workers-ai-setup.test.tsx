import { afterEach, describe, expect, test } from "bun:test"
import { testRender } from "@opentui/react/test-utils"
import { act } from "react"
import { WorkersAISetup } from "./workers-ai-setup"

const renderers: Array<Awaited<ReturnType<typeof testRender>>> = []
let submitted: Array<[string, string]> = []
let closed = 0

afterEach(() => {
  for (const item of renderers.splice(0)) act(() => item.renderer.destroy())
  submitted = []
  closed = 0
})

async function setup(width = 100, height = 32) {
  const rendered = await testRender(<WorkersAISetup open onClose={() => { closed++ }} onSubmit={(account, token) => { submitted.push([account, token]); return true }} />, { width, height })
  renderers.push(rendered)
  await rendered.waitForFrame((frame) => frame.includes("Connect Cloudflare Workers AI"))
  return rendered
}

describe("WorkersAISetup", () => {
  test("collects account ID and a masked paste-friendly API token without rendering the secret", async () => {
    const rendered = await setup()
    const accountID = "0123456789abcdef0123456789abcdef"
    const token = "cf-private-token-never-render-this"
    await act(async () => rendered.mockInput.pasteBracketedText(accountID))
    await act(async () => rendered.mockInput.pressTab())
    await act(async () => rendered.mockInput.pasteBracketedText(token))
    await rendered.flush()
    const masked = rendered.captureCharFrame()
    expect(masked).toContain(accountID)
    expect(masked).toContain("••")
    expect(masked).not.toContain(token)
    await act(async () => rendered.mockInput.pressEnter())
    expect(submitted).toEqual([[accountID, token]])
    expect(rendered.captureCharFrame()).not.toContain(token)
  })

  test("rejects missing account ID or token before sending credentials", async () => {
    const rendered = await setup()
    let frame = rendered.captureCharFrame()
    let row = frame.split("\n").findIndex((line) => line.includes("Connect · Enter"))
    await act(async () => rendered.mockMouse.click(frame.split("\n")[row]!.indexOf("Connect"), row))
    expect(submitted).toEqual([])
    expect(rendered.captureCharFrame()).toContain("Enter your Cloudflare account ID.")
    await act(async () => rendered.mockInput.pasteBracketedText("not-an-account-id"))
    await act(async () => rendered.mockInput.pressTab())
    await act(async () => rendered.mockInput.pasteBracketedText("token"))
    frame = rendered.captureCharFrame()
    row = frame.split("\n").findIndex((line) => line.includes("Connect · Enter"))
    await act(async () => rendered.mockMouse.click(frame.split("\n")[row]!.indexOf("Connect"), row))
    await rendered.flush()
    expect(submitted).toEqual([])
    expect(rendered.captureCharFrame()).toContain("32-character hexadecimal account ID")
  })

  test("requires a token after a valid account ID", async () => {
    const rendered = await setup()
    await act(async () => rendered.mockInput.pasteBracketedText("0123456789abcdef0123456789abcdef"))
    await act(async () => rendered.mockInput.pressTab())
    await act(async () => rendered.mockInput.pressEnter())
    await rendered.flush()
    expect(submitted).toEqual([])
    expect(rendered.captureCharFrame()).toContain("Paste a Cloudflare API token")
  })

  test("cancel does not submit and closes the setup", async () => {
    const rendered = await setup()
    const frame = rendered.captureCharFrame()
    const row = frame.split("\n").findIndex((line) => line.includes("Cancel"))
    await act(async () => rendered.mockMouse.click(frame.split("\n")[row]!.indexOf("Cancel"), row))
    expect(closed).toBe(1)
    expect(submitted).toEqual([])
  })

  test("keeps the setup actions visible in an 80 by 24 terminal", async () => {
    const rendered = await setup(80, 24)
    const frame = rendered.captureCharFrame()
    expect(frame).toContain("Connect · Enter")
    expect(frame).toContain("Cancel")
    expect(frame).toContain("workers-ai/get-started/rest-api")
  })
})
