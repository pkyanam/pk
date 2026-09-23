import { useRenderer } from "@opentui/react"
import { useEffect, useState } from "react"

export const PK_WORDMARK_LINES = [
  "########   ##     ##",
  "##    ###  ##    ## ",
  "##    ###  ##   ##  ",
  "########   ######   ",
  "##         ##   ##  ",
  "##         ##    ## ",
  "##         ##     ##",
] as const

export const WORDMARK_INTRO_MS = 720
export const WORDMARK_LOOP_MS = 3200
export const WORDMARK_FRAME_MS = 1000 / 60

const MINT_PEAK = "#d0ffe9"
const METAL_GLOW = "#f1fff8"
const DARK_PEAK = "#0b4734"
const DARK_GLOW = "#123b2f"
const SWEEP_COLUMNS = Math.max(...PK_WORDMARK_LINES.map((line) => line.length))

export function reducedMotionEnabled(value = process.env.PK_REDUCED_MOTION): boolean {
  return /^(1|true|yes|on)$/i.test(value ?? "")
}

export function wordmarkSweepPosition(elapsedMs: number): number {
  const phase = ((elapsedMs % WORDMARK_LOOP_MS) + WORDMARK_LOOP_MS) % WORDMARK_LOOP_MS
  const oscillation = (1 - Math.cos((phase / WORDMARK_LOOP_MS) * Math.PI * 2)) / 2
  return 1 + oscillation * (SWEEP_COLUMNS - 2)
}

function parseHexColor(color: string): [number, number, number] | undefined {
  const match = /^#([\da-f]{2})([\da-f]{2})([\da-f]{2})$/i.exec(color)
  return match ? [Number.parseInt(match[1]!, 16), Number.parseInt(match[2]!, 16), Number.parseInt(match[3]!, 16)] : undefined
}

function blend(base: [number, number, number], glow: [number, number, number], amount: number): string {
  const channels = base.map((value, index) => Math.round(value + (glow[index]! - value) * amount))
  return `#${channels.map((channel) => channel.toString(16).padStart(2, "0")).join("")}`
}

export function wordmarkColorAt(accent: string, elapsedMs: number, column: number, row = 0, lightSurface = false): string {
  const base = parseHexColor(accent)
  if (!base) return accent
  const mint = parseHexColor(lightSurface ? DARK_PEAK : MINT_PEAK)!
  const metal = parseHexColor(lightSurface ? DARK_GLOW : METAL_GLOW)!
  const phase = (((elapsedMs % WORDMARK_LOOP_MS) + WORDMARK_LOOP_MS) % WORDMARK_LOOP_MS) / WORDMARK_LOOP_MS * Math.PI * 2
  const waveCenter = wordmarkSweepPosition(elapsedMs) + Math.sin(phase + row * 0.78) * 0.6
  const distance = Math.abs(column - waveCenter)
  const shimmer = Math.exp(-(distance * distance) / (2 * 1.05 * 1.05))
  const fluid = (Math.sin(phase * 2 - column * 0.58 + row * 0.8) + 1) / 2
  const mintAmount = 0.12 + shimmer * 0.58 + fluid * 0.12
  const metalAmount = shimmer * 0.18
  const tinted = parseHexColor(blend(base, mint, mintAmount))!
  return blend(tinted, metal, metalAmount)
}

export function Wordmark({ color = "#8ab4a1", reducedMotion, lightSurface = false }: { color?: string; reducedMotion?: boolean; lightSurface?: boolean }) {
  const renderer = useRenderer()
  const noMotion = reducedMotion ?? reducedMotionEnabled()
  const [elapsed, setElapsed] = useState(0)

  useEffect(() => {
    if (noMotion) {
      setElapsed(0)
      return
    }

    const originalFps = renderer.targetFps
    renderer.targetFps = 60
    const startedAt = performance.now()
    let timer: ReturnType<typeof setTimeout> | undefined
    let disposed = false
    const tick = () => {
      if (disposed) return
      setElapsed((performance.now() - startedAt) % WORDMARK_LOOP_MS)
      timer = setTimeout(tick, WORDMARK_FRAME_MS)
    }
    timer = setTimeout(tick, WORDMARK_FRAME_MS)

    return () => {
      disposed = true
      if (timer !== undefined) clearTimeout(timer)
      if (renderer.targetFps === 60) renderer.targetFps = originalFps
    }
  }, [noMotion, renderer])

  return <box id="pk-wordmark" style={{ flexDirection: "column" }}>
    {PK_WORDMARK_LINES.map((line, row) => <text key={row} selectable={false}>
      {[...line].map((character, column) => <span key={column} fg={noMotion ? color : wordmarkColorAt(color, elapsed, column, row, lightSurface)}>{character}</span>)}
    </text>)}
  </box>
}
