import { useTerminalDimensions } from "@opentui/react"

export type ContextCategoryID = "system_prompt" | "tool_schemas" | "tool_calls" | "tool_results" | "messages" | "other_input"

export type ContextCategory = {
  id: ContextCategoryID
  items: number
  bytes: number
}

export type ProviderUsageCounters = {
  input_tokens?: number | null
  input_tokens_available: boolean
  output_tokens?: number | null
  output_tokens_available: boolean
  cached_input_tokens?: number | null
  cached_input_tokens_available: boolean
  cache_write_input_tokens?: number | null
  cache_write_input_tokens_available: boolean
}

export type ContextUsageSnapshot = {
  available: boolean
  measurement: "json_value_bytes"
  request_ordinal: number
  pending: boolean
  categories: ContextCategory[] | null
  total_bytes: number | null
  latest_provider_usage: ProviderUsageCounters
  context_limit_tokens: number | null
}

const categoryInfo: Record<ContextCategoryID, { label: string; color: string }> = {
  system_prompt: { label: "System prompt", color: "#75b8ff" },
  tool_schemas: { label: "Tool definitions", color: "#45d3b2" },
  tool_calls: { label: "Tool calls", color: "#b6e66b" },
  tool_results: { label: "Tool results", color: "#ffb45e" },
  messages: { label: "Messages", color: "#d69aff" },
  other_input: { label: "Other input", color: "#ff7896" },
}

const categoryOrder: ContextCategoryID[] = ["system_prompt", "tool_schemas", "tool_calls", "tool_results", "messages", "other_input"]
const gridRows = 3
const defaultGridColumns = 14

export function contextGridCellCounts(categories: ContextCategory[], cells = defaultGridColumns * gridRows): Record<ContextCategoryID, number> {
  const counts: Record<ContextCategoryID, number> = {
    system_prompt: 0, tool_schemas: 0, tool_calls: 0, tool_results: 0, messages: 0, other_input: 0,
  }
  const weights = categoryOrder.map((id) => Math.max(0, categories.find((category) => category.id === id)?.bytes ?? 0))
  const total = weights.reduce((sum, value) => sum + value, 0)
  if (total <= 0 || cells <= 0) return counts
  const shares = weights.map((weight) => (weight * cells) / total)
  const floors = shares.map(Math.floor)
  let remaining = cells - floors.reduce((sum, value) => sum + value, 0)
  const priority = shares.map((share, index) => ({ index, remainder: share - floors[index]! }))
    .sort((left, right) => right.remainder - left.remainder || left.index - right.index)
  for (const item of priority) {
    if (remaining <= 0) break
    if (weights[item.index]! <= 0) continue
    floors[item.index] = floors[item.index]! + 1
    remaining--
  }
  categoryOrder.forEach((id, index) => { counts[id] = floors[index]! })
  return counts
}

function formatBytes(value: number): string {
  const bytes = Number.isFinite(value) ? Math.max(0, Math.floor(value)) : 0
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(bytes >= 10 * 1024 ? 0 : 1)} KiB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MiB`
}

function formatTokens(value: number | null | undefined, available: boolean): string {
  if (!available || value == null || !Number.isFinite(value) || value < 0) return "—"
  return Math.floor(value).toLocaleString()
}

function percentage(bytes: number, total: number): string {
  if (total <= 0) return "0%"
  return `${Math.round((Math.max(0, bytes) / total) * 100)}%`
}

function orderedCategories(categories: ContextCategory[]): ContextCategory[] {
  const byID = new Map(categories.map((category) => [category.id, category]))
  return categoryOrder.map((id) => byID.get(id) ?? { id, items: 0, bytes: 0 })
}

export function ContextUsage({ snapshot }: { snapshot?: ContextUsageSnapshot | null }) {
  const { width } = useTerminalDimensions()
  if (snapshot?.pending) {
    return <box id="context-usage" style={{ flexDirection: "column", gap: 1 }}>
      <text fg="#e5e8eb" content={snapshot.request_ordinal > 0 ? `Context composition · request ${snapshot.request_ordinal}` : "Context composition"} />
      <text fg="#ffc06d" content="No completed response is recorded for this request. It may still be running or have been interrupted." />
      <text fg="#b0bac5" content="Latest provider usage · input tokens — · cached tokens — · cache-write tokens — · output tokens —" />
      <text fg="#a6b3c0" content="Context window capacity · unavailable (no validated limit)" />
    </box>
  }
  if (!snapshot?.available) {
    return <box id="context-usage" style={{ flexDirection: "column", gap: 1 }}>
      <text fg="#e5e8eb" content="Context composition" />
      <text fg="#b0bac5" content="No saved context snapshot is available for this session." />
      <text fg="#a6b3c0" content="Usage totals and context composition are separate measurements." />
    </box>
  }

  const categories = orderedCategories(snapshot.categories ?? [])
  const total = categories.reduce((sum, category) => sum + Math.max(0, category.bytes), 0)
  const gridColumns = width < 56 ? 6 : width < 96 ? 10 : defaultGridColumns
  const gridCells = gridColumns * gridRows
  const compactLegend = width < 96
  const counts = contextGridCellCounts(categories, gridCells)
  const cells: Array<{ id: ContextCategoryID | null; key: string }> = categoryOrder.flatMap((id) => Array.from({ length: counts[id] }, (_, index) => ({ id, key: `${id}-${index}` })))
  while (cells.length < gridCells) cells.push({ id: null, key: `empty-${cells.length}` })
  const rows = Array.from({ length: gridRows }, (_, row) => cells.slice(row * gridColumns, (row + 1) * gridColumns))
  const usage = snapshot.latest_provider_usage
  const heading = snapshot.request_ordinal > 0 ? `Context composition · request ${snapshot.request_ordinal}` : "Context composition"

  return <box id="context-usage" style={{ flexDirection: "column", gap: 1 }}>
    <text fg="#e5e8eb" content={heading} />
    <text fg="#b0bac5" content="Share of request bytes · not token usage" />
    <text fg="#c3ccd6" content={`Context input · ${snapshot.total_bytes == null ? "—" : `${formatBytes(snapshot.total_bytes)} JSON-value bytes`}`} />
    <box id="context-usage-grid" style={{ flexDirection: "column", gap: 1 }}>
      {rows.map((row, rowIndex) => <text key={`row-${rowIndex}`} selectable={false}>
        {row.map((cell) => <span key={cell.key} fg={cell.id ? categoryInfo[cell.id].color : "#59616b"}>{cell.id ? "■" : "·"} </span>)}
      </text>)}
    </box>
    <box style={{ flexDirection: "column" }}>
      {categories.map((category) => <text key={category.id}>
        <span fg={categoryInfo[category.id].color}>■ </span>
        <span fg={categoryInfo[category.id].color}>{compactLegend ? shortCategoryLabel(category.id) : categoryInfo[category.id].label}</span>
        <span fg="#c3ccd6"> · {formatBytes(category.bytes)} · {percentage(category.bytes, total)} · {compactLegend ? category.items : `${category.items} ${category.items === 1 ? "item" : "items"}`}</span>
      </text>)}
    </box>
    <text fg="#c3ccd6" content={`Latest provider usage · input tokens ${formatTokens(usage.input_tokens, usage.input_tokens_available)} · cached tokens ${formatTokens(usage.cached_input_tokens, usage.cached_input_tokens_available)} · cache-write tokens ${formatTokens(usage.cache_write_input_tokens, usage.cache_write_input_tokens_available)} · output tokens ${formatTokens(usage.output_tokens, usage.output_tokens_available)}`} />
    <text fg="#a6b3c0" content={snapshot.context_limit_tokens == null ? "Context window capacity · unavailable (no validated limit)" : `Context window capacity · ${snapshot.context_limit_tokens.toLocaleString()} tokens`} />
  </box>
}

function shortCategoryLabel(id: ContextCategoryID): string {
  switch (id) {
    case "system_prompt": return "System"
    case "tool_schemas": return "Definitions"
    case "tool_calls": return "Calls"
    case "tool_results": return "Results"
    case "messages": return "Messages"
    case "other_input": return "Other"
  }
}
