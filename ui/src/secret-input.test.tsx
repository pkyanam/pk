import { afterEach, describe, expect, test } from "bun:test"
import { testRender } from "@opentui/react/test-utils"
import { act, useState } from "react"
import { normalizeSecretPaste, SecretInput } from "./secret-input"

const renderers: Array<Awaited<ReturnType<typeof testRender>>> = []
let latest = ""

function Harness({ focused = true, initial = "", maxLength = 4096 }: { focused?: boolean; initial?: string; maxLength?: number }) {
  const [value, setValue] = useState(initial)
  latest = value
  return <SecretInput focused={focused} value={value} onChange={setValue} maxLength={maxLength} />
}

afterEach(() => {
  for (const item of renderers.splice(0)) act(() => item.renderer.destroy())
  latest = ""
})

async function setup(props: { focused?: boolean; initial?: string; maxLength?: number } = {}) {
  const rendered = await testRender(<Harness {...props} />, { width: 80, height: 24 })
  renderers.push(rendered)
  await rendered.waitForFrame((frame) => frame.includes("Paste secret · masked") || frame.includes("••"))
  return rendered
}

describe("SecretInput", () => {
  test("pastes a long value as bullets only and keeps it out of selectable text", async () => {
    const secret = `pk_live_${"long-private-token-".repeat(12)}`
    const rendered = await setup()
    await act(async () => rendered.mockInput.pasteBracketedText(secret))
    const frame = await rendered.waitForFrame((value) => value.includes("•"))
    expect(latest).toBe(secret)
    expect(frame).toContain("•")
    expect(frame).not.toContain(secret)

    await act(async () => {
      await rendered.mockMouse.pressDown(0, 0)
      await rendered.mockMouse.moveTo(60, 0)
      await rendered.mockMouse.release(60, 0)
    })
    const selection = (rendered.renderer as any).getSelection()
    expect(selection).toBeNull()
  })

  test("accepts slash-leading text secrets and strips pasted control characters", async () => {
    const rendered = await setup()
    await act(async () => rendered.mockInput.pasteBracketedText("/abc\ndef\tghi\u0000\u007f"))
    expect(latest).toBe("/abcdefghi")
    expect(rendered.captureCharFrame()).not.toContain("/abcdefghi")
    expect(normalizeSecretPaste("/valid/base64+key=" )).toBe("/valid/base64+key=")
    expect(normalizeSecretPaste("file:///tmp/key", "text/uri-list", "text")).toBeUndefined()
    expect(normalizeSecretPaste("opaque", "application/octet-stream", "binary")).toBeUndefined()
  })

  test("accepts printable keys, backspace, Ctrl-U, while leaving Enter and Escape to the parent", async () => {
    const rendered = await setup()
    await act(async () => rendered.mockInput.typeText("aB9"))
    expect(latest).toBe("aB9")
    await act(async () => rendered.mockInput.pressBackspace())
    expect(latest).toBe("aB")
    await act(async () => rendered.mockInput.pressKey("u", { ctrl: true }))
    expect(latest).toBe("")
    await act(async () => rendered.mockInput.typeText("keep"))
    await act(async () => rendered.mockInput.pressEnter())
    await act(async () => rendered.mockInput.pressEscape())
    expect(latest).toBe("keep")
  })

  test("enforces the UTF-8 byte bound", async () => {
    const rendered = await setup({ maxLength: 12 })
    await act(async () => rendered.mockInput.pasteBracketedText("abcdefghijklmnop"))
    expect(latest).toBe("abcdefghijkl")
  })

  test("never exceeds the hard 4096-byte secret limit", async () => {
    const rendered = await setup({ maxLength: 5000 })
    await act(async () => rendered.mockInput.pasteBracketedText("x".repeat(5000)))
    expect(latest).toHaveLength(4096)
  })

  test("does not consume input or paste while unfocused", async () => {
    const rendered = await setup({ focused: false })
    await act(async () => rendered.mockInput.typeText("wrong"))
    await act(async () => rendered.mockInput.pasteBracketedText("ignored-secret"))
    expect(latest).toBe("")
    expect(rendered.captureCharFrame()).not.toContain("ignored-secret")
  })

})
