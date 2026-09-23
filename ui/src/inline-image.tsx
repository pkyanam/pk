import { fileURLToPath, pathToFileURL } from "node:url"
import { constants } from "node:fs"
import { open, realpath } from "node:fs/promises"
import path from "node:path"
import { useEffect, useRef, useState } from "react"
import { fg, imageInfo, link, NativeImage, StyledText, type ImageRenderProtocol, underline } from "@opentui/core"

const MAX_IMAGE_BYTES = 32 * 1024 * 1024
const MAX_IMAGE_DIMENSION = 8192
const MAX_IMAGE_PIXELS = 16_000_000
const MAX_RETAINED_PREVIEW_PIXELS = 32_000_000
const MAX_WIDTH = 72
const MAX_HEIGHT = 32
const CELL_ASPECT_RATIO = 0.5
const IMAGE_EXTENSIONS = new Set([".png", ".jpg", ".jpeg", ".webp", ".gif"])

let retainedPreviewPixels = 0
let decoderBusy = false
type DecodeWaiter = { signal: AbortSignal; resolve: (release: () => void) => void; reject: (error: Error) => void; abort: () => void }
const decodeWaiters: DecodeWaiter[] = []

function reservePreviewPixels(pixels: number): (() => void) | null {
  if (!Number.isSafeInteger(pixels) || pixels < 1 || pixels > MAX_RETAINED_PREVIEW_PIXELS - retainedPreviewPixels) return null
  retainedPreviewPixels += pixels
  let released = false
  return () => {
    if (released) return
    released = true
    retainedPreviewPixels = Math.max(0, retainedPreviewPixels - pixels)
  }
}

function makeDecodeRelease(): () => void {
  let released = false
  return () => {
    if (released) return
    released = true
    while (decodeWaiters.length) {
      const next = decodeWaiters.shift()!
      next.signal.removeEventListener("abort", next.abort)
      if (next.signal.aborted) {
        next.reject(new Error("image decode canceled"))
        continue
      }
      next.resolve(makeDecodeRelease())
      return
    }
    decoderBusy = false
  }
}

function acquireDecodeSlot(signal: AbortSignal): Promise<() => void> {
  if (signal.aborted) return Promise.reject(new Error("image decode canceled"))
  if (!decoderBusy) {
    decoderBusy = true
    return Promise.resolve(makeDecodeRelease())
  }
  return new Promise((resolve, reject) => {
    const waiter: DecodeWaiter = {
      signal,
      resolve,
      reject,
      abort: () => {
        const index = decodeWaiters.indexOf(waiter)
        if (index >= 0) decodeWaiters.splice(index, 1)
        reject(new Error("image decode canceled"))
      },
    }
    decodeWaiters.push(waiter)
    signal.addEventListener("abort", waiter.abort, { once: true })
  })
}

export const __inlineImagePreviewMemoryForTests = {
  reserve: reservePreviewPixels,
  retainedPixels: () => retainedPreviewPixels,
}

function hasScheme(value: string): boolean {
  return /^[a-z][a-z0-9+.-]*:/i.test(value) && !/^[a-z]:[\\/]/i.test(value)
}

function imagePathPart(source: string): string | null {
  const value = source.trim()
  if (!value || value.length > 4096 || /[\u0000-\u001f\u007f]/.test(value)) return null
  if (value.startsWith("sandbox:")) {
    try {
      const url = new URL(value)
      if (url.protocol !== "sandbox:" || url.host || url.search || url.hash) return null
      return decodeURIComponent(url.pathname)
    } catch {
      return null
    }
  }
  if (value.startsWith("file:")) {
    try {
      const url = new URL(value)
      if (url.protocol !== "file:" || url.search || url.hash) return null
      return fileURLToPath(url)
    } catch {
      return null
    }
  }
  if (hasScheme(value)) return null
  return value
}

export function isLocalImageReference(source: string): boolean {
  const pathPart = imagePathPart(source)
  return pathPart !== null && IMAGE_EXTENSIONS.has(path.extname(pathPart).toLowerCase())
}

function isWithin(root: string, file: string): boolean {
  const relative = path.relative(root, file)
  return relative === "" || (!path.isAbsolute(relative) && relative !== ".." && !relative.startsWith(`..${path.sep}`))
}

type ImagePreflight = { path: string; bytes: Buffer; width: number; height: number }

async function inspectLocalImageSource(source: string, workspace: string): Promise<ImagePreflight> {
  const pathPart = imagePathPart(source)
  if (!pathPart) throw new Error("unsupported image source")

  const sandbox = source.trim().startsWith("sandbox:")
  const candidate = path.isAbsolute(pathPart) ? pathPart : path.resolve(workspace, pathPart)
  const [workspaceRoot, resolved] = await Promise.all([realpath(workspace), realpath(candidate)])
  let allowed = isWithin(workspaceRoot, resolved)
  if (!allowed && sandbox) {
    const tempRoots = await Promise.all(["/tmp", "/private/tmp"].map(async (root) => {
      try { return await realpath(root) } catch { return null }
    }))
    allowed = tempRoots.some((root) => root !== null && isWithin(root, resolved))
  }
  if (!allowed) throw new Error("image path is outside the workspace")

  if (!IMAGE_EXTENSIONS.has(path.extname(resolved).toLowerCase())) {
    throw new Error("unsupported image format")
  }
  const handle = await open(resolved, constants.O_RDONLY | constants.O_NONBLOCK | (constants.O_NOFOLLOW ?? 0))
  try {
    const info = await handle.stat()
    if (!info.isFile() || info.size <= 0 || info.size > MAX_IMAGE_BYTES) {
      throw new Error("image file is empty or exceeds the preview limit")
    }
    const bytes = Buffer.alloc(info.size)
    const { bytesRead } = await handle.read(bytes, 0, info.size, 0)
    if (bytesRead !== info.size) throw new Error("image changed while validating")
    const dimensions = imageInfo(bytes)
    if (
      dimensions.width < 1 || dimensions.height < 1 ||
      dimensions.width > MAX_IMAGE_DIMENSION || dimensions.height > MAX_IMAGE_DIMENSION ||
      dimensions.width * dimensions.height > MAX_IMAGE_PIXELS
    ) {
      throw new Error("image dimensions exceed the preview limit")
    }
    return { path: resolved, bytes, width: dimensions.width, height: dimensions.height }
  } finally {
    await handle.close()
  }
}

/** Resolve and validate a workspace-local file or a generated sandbox image. */
export async function resolveLocalImageSource(source: string, workspace: string): Promise<string> {
  return (await inspectLocalImageSource(source, workspace)).path
}

function dimension(value: number | undefined, maximum: number, fallback: number): number {
  if (value === undefined || !Number.isFinite(value)) return fallback
  return Math.max(1, Math.min(maximum, Math.floor(value)))
}

/** Size the image box to the fitted bitmap so native fit doesn't center it in empty columns. */
export function fittedPreviewSize(sourceWidth: number, sourceHeight: number, maxWidth: number, maxHeight: number): { width: number; height: number } {
  if (sourceWidth < 1 || sourceHeight < 1) return { width: maxWidth, height: maxHeight }
  const sourceAspect = sourceWidth / sourceHeight
  const maxAspect = (maxWidth * CELL_ASPECT_RATIO) / maxHeight
  if (sourceAspect > maxAspect) {
    return { width: maxWidth, height: Math.max(1, Math.min(maxHeight, Math.round(maxWidth * CELL_ASPECT_RATIO / sourceAspect))) }
  }
  return { width: Math.max(1, Math.min(maxWidth, Math.round(maxHeight * sourceAspect / CELL_ASPECT_RATIO))), height: maxHeight }
}

function safeAlt(value: string | undefined): string {
  return (value ?? "image").replace(/[\u0000-\u001f\u007f]/g, " ").trim().slice(0, 100) || "image"
}

export interface InlineImageProps {
  source: string
  workspace: string
  alt?: string
  width?: number
  height?: number
  protocol?: ImageRenderProtocol
  /** Resolve and retain only the safe original-file link, without another bitmap preview. */
  preview?: boolean
}

type PreviewLease = { image: NativeImage | null; releasePixels: (() => void) | null }

/** Native OpenTUI image preview. Its ImageRenderable owns load cancellation and disposal. */
export function InlineImage({ source, workspace, alt, width, height, protocol, preview = true }: InlineImageProps) {
  const [localPath, setLocalPath] = useState<string | null>(null)
  const [previewImage, setPreviewImage] = useState<NativeImage | null>(null)
  const [sourceSize, setSourceSize] = useState<{ width: number; height: number } | null>(null)
  const [previewSkipped, setPreviewSkipped] = useState(false)
  const activeLeaseRef = useRef<PreviewLease | null>(null)
  const [failed, setFailed] = useState(false)
  const [loadError, setLoadError] = useState(false)
  const previewWidth = dimension(width, MAX_WIDTH, 56)
  const previewHeight = dimension(height, MAX_HEIGHT, 18)

  useEffect(() => {
    let active = true
    const controller = new AbortController()
    const lease: PreviewLease = { image: null, releasePixels: null }
    activeLeaseRef.current = lease
    setLocalPath(null)
    setPreviewImage(null)
    setSourceSize(null)
    setPreviewSkipped(false)
    setFailed(false)
    setLoadError(false)
    void (async () => {
      try {
        if (!preview) {
          const resolved = await resolveLocalImageSource(source, workspace)
          if (active) setLocalPath(resolved)
          return
        }
        const releaseDecode = await acquireDecodeSlot(controller.signal)
        try {
          const preflight = await inspectLocalImageSource(source, workspace)
          if (!active) return
          setLocalPath(preflight.path)
          setSourceSize({ width: preflight.width, height: preflight.height })
          lease.releasePixels = reservePreviewPixels(preflight.width * preflight.height)
          if (!lease.releasePixels) {
            preflight.bytes = Buffer.alloc(0)
            setPreviewSkipped(true)
            return
          }
          const retainedImage = await NativeImage.load(preflight.bytes, { signal: controller.signal })
          preflight.bytes = Buffer.alloc(0)
          if (!active || controller.signal.aborted) {
            retainedImage.dispose()
            return
          }
          lease.image = retainedImage
          setPreviewImage(retainedImage)
        } finally {
          releaseDecode()
        }
      } catch {
        lease.image?.dispose()
        lease.image = null
        lease.releasePixels?.()
        lease.releasePixels = null
        if (active) setFailed(true)
      }
    })()
    return () => {
      active = false
      controller.abort()
      if (activeLeaseRef.current === lease) activeLeaseRef.current = null
      lease.image?.dispose()
      lease.image = null
      lease.releasePixels?.()
      lease.releasePixels = null
    }
  }, [preview, source, workspace])

  if (!localPath) return <text>{failed ? `Image preview unavailable · ${safeAlt(alt)}` : "Loading image preview…"}</text>
  const filename = safeAlt(path.basename(localPath)).slice(0, 64)
  const fileURL = pathToFileURL(localPath).href
  const fileLink = <text style={{ height: 1, flexShrink: 0 }} content={new StyledText([link(fileURL)(underline(fg("#858e96")("Open original")))])} />
  const caption = <text style={{ height: 1, flexShrink: 0 }} fg="#8ab4a1" content={`Preview · ${filename}`} />
  if (!preview) return <box style={{ flexDirection: "row", gap: 1, width: "auto", flexShrink: 0 }}>
    <text style={{ height: 1, flexShrink: 0 }} fg="#8ab4a1" content={`Image · ${filename}`} />
    {fileLink}
  </box>
  const frameSize = fittedPreviewSize(sourceSize?.width ?? 1, sourceSize?.height ?? 1, previewWidth, previewHeight)
  if (previewSkipped || failed || loadError || !previewImage) return <box flexDirection="column" width="auto">
    <text fg="#8ab4a1">{previewSkipped ? `Preview unavailable · ${filename}` : failed || loadError ? `Preview unavailable · ${filename}` : "Loading image preview…"}</text>
    {fileLink}
  </box>
  return <box style={{ flexDirection: "column", width: "auto", alignItems: "flex-start", marginTop: 1, marginBottom: 1, flexShrink: 0 }}>
    {caption}
    <image source={previewImage} width={frameSize.width} height={frameSize.height} fit="fit" protocol={protocol ?? "auto"} style={{ flexShrink: 0 }} onError={() => {
      setLoadError(true)
      const lease = activeLeaseRef.current
      if (lease?.image === previewImage) {
        lease.image.dispose()
        lease.image = null
        lease.releasePixels?.()
        lease.releasePixels = null
      }
      setPreviewImage(null)
    }} />
    {fileLink}
  </box>
}
