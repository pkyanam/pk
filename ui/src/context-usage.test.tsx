import { afterEach, describe, expect, test } from "bun:test"
import { testRender } from "@opentui/react/test-utils"
import { act } from "react"
import { ContextUsage, contextGridCellCounts, type ContextCategory, type ContextUsageSnapshot } from "./context-usage"

const renderers: Array<Awaited<ReturnType<typeof testRender>>> = []

afterEach(() => {
  for (const setup of renderers.splice(0)) act(() => setup.renderer.destroy())
})

const categories: ContextCategory[] = [
  { id: "system_prompt", items: 1, bytes: 500 },
  { id: "tool_schemas", items: 8, bytes: 200 },
  { id: "tool_calls", items: 2, bytes: 100 },
  { id: "tool_results", items: 2, bytes: 300 },
  { id: "messages", items: 12, bytes: 800 },
  { id: "other_input", items: 1, bytes: 100 },
]

function snapshot(overrides: Partial<ContextUsageSnapshot> = {}): ContextUsageSnapshot {
  return {
    available: true,
    measurement: "json_value_bytes",
    request_ordinal: 4,
    pending: false,
    categories,
    total_bytes: 2000,
    latest_provider_usage: {
      input_tokens: 3800, input_tokens_available: true,
      output_tokens: 244, output_tokens_available: true,
      cached_input_tokens: 0, cached_input_tokens_available: true,
      cache_write_input_tokens: 512, cache_write_input_tokens_available: true,
    },
    context_limit_tokens: null,
    ...overrides,
  }
}

describe("context usage grid", () => {
  test("allocates a stable byte-proportional grid without inventing capacity", () => {
    const counts = contextGridCellCounts(categories)
    expect(Object.values(counts).reduce((sum, value) => sum + value, 0)).toBe(42)
    expect(counts.messages).toBeGreaterThan(counts.system_prompt)
    expect(contextGridCellCounts(categories.map((category) => ({ ...category, bytes: 0 }))).messages).toBe(0)
  })

  test.each([{ width: 44, height: 30 }, { width: 80, height: 24 }, { width: 120, height: 36 }])("renders spaced byte cells and separate provider counters at $width × $height", async ({ width, height }) => {
    const setup = await testRender(<ContextUsage snapshot={snapshot()} />, { width, height })
    renderers.push(setup)
    const frame = await setup.waitForFrame((value) => value.includes("Context composition · request 4"))
    const compact = frame.replace(/\s+/g, " ")
    expect(compact).toContain("Share of request bytes · not token usage")
    expect(compact).toContain(width < 96 ? "System ·" : "System prompt")
    expect(compact).toContain(width < 96 ? "Definitions ·" : "Tool definitions")
    expect(compact).toContain(width < 96 ? "Calls ·" : "Tool calls")
    expect(compact).toContain(width < 96 ? "Results ·" : "Tool results")
    expect(compact).toContain("Messages")
    expect(compact).toContain("Latest provider usage · input tokens 3,800 · cached tokens 0 · cache-write tokens 512 · output tokens 244")
    expect(compact).toContain("Context window capacity · unavailable (no validated limit)")
    const gridLines = frame.split("\n").filter((line) => line.includes("■"))
    expect(gridLines.length).toBeGreaterThan(0)
    expect(gridLines.every((line) => !line.includes("■■"))).toBe(true)
    expect(gridLines.every((line) => line.length <= width + 1)).toBe(true)
    expect(frame.split("\n")).toHaveLength(height + 1)
  })

  test("marks live requests and unavailable snapshots clearly", async () => {
    const live = await testRender(<ContextUsage snapshot={snapshot({ pending: true })} />, { width: 80, height: 24 })
    renderers.push(live)
    const liveFrame = await live.waitForFrame((value) => value.includes("No completed response is recorded"))
    const compactLive = liveFrame.replace(/\s+/g, " ")
    expect(compactLive).toContain("It may still be running or have been interrupted")
    expect(compactLive).toContain("input tokens —")
    expect(compactLive).not.toContain("System prompt")

    const missing = await testRender(<ContextUsage snapshot={{ ...snapshot(), available: false }} />, { width: 80, height: 24 })
    renderers.push(missing)
    expect(await missing.waitForFrame((value) => value.includes("No saved context snapshot"))).toContain("separate measurements")
  })
})
