import { useRenderer } from "@opentui/react"
import { useEffect, useState } from "react"

export const PK_WORDMARK_LINES = [
  "       _    ",
  " _ __ | | __",
  "| '_ \\| |/ /",
  "| |_) |   < ",
  "| .__/|_|\\_\\",
  "|_|         ",
] as const

export const WORDMARK_INTRO_MS = 720
export const WORDMARK_FRAME_MS = 1000 / 60

const MINT_PEAK = "#b3e8cf"
const SWEEP_COLUMNS = Math.max(...PK_WORDMARK_LINES.map((line) => line.length))

export function reducedMotionEnabled(value = process.env.PK_REDUCED_MOTION): boolean {
  return /^(1|true|yes|on)$/i.test(value ?? "")
}

export function wordmarkSweepPosition(elapsedMs: number): number {
  const progress = Math.max(0, Math.min(1, elapsedMs / WORDMARK_INTRO_MS))
  return -4 + progress * (SWEEP_COLUMNS + 8)
}

function parseHexColor(color: string): [number, number, number] | undefined {
  const match = /^#([\da-f]{2})([\da-f]{2})([\da-f]{2})$/i.exec(color)
  return match ? [Number.parseInt(match[1]!, 16), Number.parseInt(match[2]!, 16), Number.parseInt(match[3]!, 16)] : undefined
}

export function wordmarkColorAt(accent: string, elapsedMs: number, column: number): string {
  if (elapsedMs >= WORDMARK_INTRO_MS) return accent
  const base = parseHexColor(accent)
  const peak = parseHexColor(MINT_PEAK)!
  if (!base) return accent
  const distance = Math.abs(column - wordmarkSweepPosition(elapsedMs))
  const intensity = 0.82 * Math.exp(-(distance * distance) / (2 * 0.92 * 0.92))
  const channels = base.map((value, index) => Math.round(value + (peak[index]! - value) * intensity))
  return `#${channels.map((channel) => channel.toString(16).padStart(2, "0")).join("")}`
}

export function Wordmark({ color = "#8ab4a1", reducedMotion }: { color?: string; reducedMotion?: boolean }) {
  const renderer = useRenderer()
  const noMotion = reducedMotion ?? reducedMotionEnabled()
  const [elapsed, setElapsed] = useState(noMotion ? WORDMARK_INTRO_MS : 0)

  useEffect(() => {
    if (noMotion) {
      setElapsed(WORDMARK_INTRO_MS)
      return
    }

    const originalFps = renderer.targetFps
    renderer.targetFps = 60
    const startedAt = performance.now()
    let timer: ReturnType<typeof setTimeout> | undefined
    let disposed = false
    const tick = () => {
      if (disposed) return
      const nextElapsed = Math.min(WORDMARK_INTRO_MS, performance.now() - startedAt)
      setElapsed(nextElapsed)
      if (nextElapsed < WORDMARK_INTRO_MS) {
        timer = setTimeout(tick, WORDMARK_FRAME_MS)
      } else if (renderer.targetFps === 60) {
        renderer.targetFps = originalFps
      }
    }
    timer = setTimeout(tick, WORDMARK_FRAME_MS)

    return () => {
      disposed = true
      if (timer !== undefined) clearTimeout(timer)
      if (renderer.targetFps === 60) renderer.targetFps = originalFps
    }
  }, [noMotion, renderer])

  const settled = noMotion || elapsed >= WORDMARK_INTRO_MS
  return <box id="pk-wordmark" style={{ flexDirection: "column" }}>
    {PK_WORDMARK_LINES.map((line, index) => {
      return <text key={index} selectable={false} fg={color}>
        {settled ? line : [...line].map((character, column) => <span key={column} fg={wordmarkColorAt(color, elapsed, column)}>{character}</span>)}
      </text>
    })}
  </box>
}
