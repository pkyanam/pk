import { afterEach, describe, expect, test } from "bun:test"
import { ImageRenderable, NativeImage } from "@opentui/core"
import { testRender } from "@opentui/react/test-utils"
import { act } from "react"
import { mkdtemp, open, realpath, rm, symlink, writeFile } from "node:fs/promises"
import os from "node:os"
import path from "node:path"
import { __inlineImagePreviewMemoryForTests, fittedPreviewSize, InlineImage, isLocalImageReference, resolveLocalImageSource } from "./inline-image"

const openRenderers: Array<Awaited<ReturnType<typeof testRender>>> = []
const pixelPNG = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAAAAAA6fptVAAAACklEQVR4nGNgAAAAAgABSK+kcQAAAABJRU5ErkJggg==", "base64")

function pngWithDimensions(width: number, height: number): Buffer {
  const png = Buffer.from(pixelPNG)
  png.writeUInt32BE(width, 16)
  png.writeUInt32BE(height, 20)
  let crc = 0xffffffff
  for (let i = 12; i < 29; i++) {
    crc ^= png[i]
    for (let bit = 0; bit < 8; bit++) crc = (crc >>> 1) ^ (crc & 1 ? 0xedb88320 : 0)
  }
  png.writeUInt32BE((crc ^ 0xffffffff) >>> 0, 29)
  return png
}

afterEach(async () => {
  for (const setup of openRenderers.splice(0)) act(() => setup.renderer.destroy())
})

function findDescendant(root: any, predicate: (renderable: any) => boolean): any | undefined {
  if (predicate(root)) return root
  for (const child of root.getChildren?.() ?? []) {
    const match = findDescendant(child, predicate)
    if (match) return match
  }
  return undefined
}

describe("local inline image preview", () => {
  test("fits the bitmap to the narrow width without reserving centered empty columns", () => {
    expect(fittedPreviewSize(1600, 900, 35, 12)).toEqual({ width: 35, height: 10 })
    expect(fittedPreviewSize(900, 1600, 35, 12)).toEqual({ width: 14, height: 12 })
  })

  test("classifies only local image references", () => {
    expect(isLocalImageReference("sandbox:/private/tmp/render.png")).toBe(true)
    expect(isLocalImageReference("images/screen.webp")).toBe(true)
    expect(isLocalImageReference("https://example.com/image.png")).toBe(false)
    expect(isLocalImageReference("data:image/png;base64,abc")).toBe(false)
    expect(isLocalImageReference("notes.txt")).toBe(false)
  })

  test("resolves in-workspace and sandbox temp images, rejects traversal and symlinks out", async () => {
    const workspace = await mkdtemp(path.join(os.tmpdir(), "pk-image-workspace-"))
    const outside = await mkdtemp(path.join(os.tmpdir(), "pk-image-outside-"))
    try {
      const image = path.join(workspace, "screen.png")
      const outsideImage = path.join(outside, "secret.png")
      await writeFile(image, pixelPNG)
      await writeFile(outsideImage, pixelPNG)
      await symlink(outsideImage, path.join(workspace, "link.png"))

      expect(await resolveLocalImageSource("screen.png", workspace)).toBe(await realpath(image))
      expect(await resolveLocalImageSource(image, workspace)).toBe(await realpath(image))
      await expect(resolveLocalImageSource(path.relative(workspace, outsideImage), workspace)).rejects.toThrow()
      await expect(resolveLocalImageSource("link.png", workspace)).rejects.toThrow()
      await expect(resolveLocalImageSource("https://example.com/image.png", workspace)).rejects.toThrow()
      await expect(resolveLocalImageSource(outsideImage, workspace)).rejects.toThrow()

      const tempDir = await mkdtemp(path.join("/tmp", "pk-sandbox-image-"))
      try {
        const tempImage = path.join(tempDir, "generated.png")
        await writeFile(tempImage, pixelPNG)
        expect(await resolveLocalImageSource(`sandbox:${tempImage}`, workspace)).toBe(await realpath(tempImage))
      } finally {
        await rm(tempDir, { recursive: true, force: true })
      }
    } finally {
      await rm(workspace, { recursive: true, force: true })
      await rm(outside, { recursive: true, force: true })
    }
  })

  test("rejects oversized images before passing them to OpenTUI", async () => {
    const workspace = await mkdtemp(path.join(os.tmpdir(), "pk-image-size-"))
    try {
      const large = path.join(workspace, "large.png")
      const handle = await open(large, "w")
      try { await handle.truncate(32 * 1024 * 1024 + 1) } finally { await handle.close() }
      await expect(resolveLocalImageSource(large, workspace)).rejects.toThrow("preview limit")
    } finally {
      await rm(workspace, { recursive: true, force: true })
    }
  })

  test("rejects oversized decoded dimensions from image headers", async () => {
    const workspace = await mkdtemp(path.join(os.tmpdir(), "pk-image-dimensions-"))
    try {
      const large = path.join(workspace, "large.png")
      await writeFile(large, pngWithDimensions(5000, 4000))
      await expect(resolveLocalImageSource(large, workspace)).rejects.toThrow("dimensions exceed")
    } finally {
      await rm(workspace, { recursive: true, force: true })
    }
  })

  test("bounds retained preview pixels and releases reservations idempotently", () => {
    const release = __inlineImagePreviewMemoryForTests.reserve(32_000_000)
    expect(release).toBeFunction()
    expect(__inlineImagePreviewMemoryForTests.retainedPixels()).toBe(32_000_000)
    expect(__inlineImagePreviewMemoryForTests.reserve(1)).toBeNull()
    release?.()
    release?.()
    expect(__inlineImagePreviewMemoryForTests.retainedPixels()).toBe(0)
    const smallRelease = __inlineImagePreviewMemoryForTests.reserve(1024)
    expect(smallRelease).toBeFunction()
    smallRelease?.()
  })

  test("keeps the full-image link when the retained-pixel budget is exhausted", async () => {
    const workspace = await mkdtemp(path.join(os.tmpdir(), "pk-inline-budget-"))
    const holdBudget = __inlineImagePreviewMemoryForTests.reserve(32_000_000)
    try {
      await writeFile(path.join(workspace, "pixel.png"), pixelPNG)
      const setup = await testRender(<InlineImage source="pixel.png" workspace={workspace} />, { width: 80, height: 24 })
      openRenderers.push(setup)
      const frame = await setup.waitForFrame((value) => value.includes("Preview unavailable · pixel.png"))
      expect(frame).toContain("Preview unavailable · pixel.png")
      expect(frame).toContain("Open original")
      expect(findDescendant(setup.renderer.root, (renderable) => renderable instanceof ImageRenderable)).toBeUndefined()
    } finally {
      holdBudget?.()
      await rm(workspace, { recursive: true, force: true })
    }
  })

  test("renders a validated PNG with a readable caption, left-aligned narrow frame, and breathing room", async () => {
    const workspace = await mkdtemp(path.join(os.tmpdir(), "pk-inline-render-"))
    try {
      const imagePath = path.join(workspace, "pixel.png")
      await writeFile(imagePath, pixelPNG)
      const setup = await testRender(<InlineImage source="pixel.png" workspace={workspace} alt="generated preview" width={35} height={12} />, { width: 35, height: 24 })
      openRenderers.push(setup)
      await setup.waitForFrame((frame) => !frame.includes("Loading image preview"))
      expect(setup.captureCharFrame()).not.toContain("Loading image preview")
      const image = findDescendant(setup.renderer.root, (renderable) => renderable instanceof ImageRenderable)
      expect(image).toBeDefined()
      expect(image.source).toBeInstanceOf(NativeImage)
      expect(image.protocol).toBe("auto")
      expect(image.fit).toBe("fit")
      expect(image.width).toBe(24)
      expect(image.height).toBe(12)
      const frame = setup.captureCharFrame().split("\n")
      const captionRow = frame.findIndex((line) => line.includes("Preview · pixel.png"))
      const actionRow = frame.findIndex((line) => line.includes("Open original"))
      expect(captionRow).toBeGreaterThan(0)
      expect(frame[captionRow - 1]?.trim()).toBe("")
      expect(actionRow).toBeGreaterThan(captionRow)
      expect(frame[actionRow + 1]?.trim()).toBe("")
      await act(async () => { await image.loadPromise })
      expect(image.image?.width).toBe(1)
      expect(__inlineImagePreviewMemoryForTests.retainedPixels()).toBe(1)
      act(() => setup.renderer.destroy())
      openRenderers.pop()
      expect(__inlineImagePreviewMemoryForTests.retainedPixels()).toBe(0)
    } finally {
      await rm(workspace, { recursive: true, force: true })
    }
  })
})
