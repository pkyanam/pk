import { useKeyboard, usePaste, useRenderer, useSelectionHandler } from "@opentui/react"
import { SyntaxStyle, type TextareaRenderable } from "@opentui/core"
import { memo, useEffect, useRef, useState } from "react"
import type { PkTransport } from "./transport"
import type { ServerEvent } from "./protocol"

type Role = "user" | "assistant" | "system" | "tool"
type Entry = { id: number; role: Role; text: string; callId?: string; toolName?: string; toolState?: string; startedAt?: number; elapsedMs?: number; commandPreview?: string; detail?: string; provisional?: boolean; delivery?: "queued" | "accepted" | "rejected"; deliveryMessage?: string }
type ToolActivity = { id: string; name: string; state: string; startedAt: number; detail?: string }
type StreamDraft = { outerId: string; requestId: string; attempt: number; itemId: string; entryId: number; text: string }
type ActiveStreamAttempt = { outerId: string; requestId: string; attempt: number }
type StreamProgress = { outerId: string; requestId: string; label: string }
type Model = { id: string; label: string }
type PendingQuestion = { id: string; text: string; choices: string[]; kind: "question" | "confirmation"; answering?: boolean; submittedAnswer?: string }
type SlashCommand = { name: string; description: string; action: "model" | "effort" | "tasks" | "skills" | "plugins" | "plugin" | "mcp" | "tools" | "update" | "rollback" | "reload" | "new" | "attach" | "detach" | "cancel" | "status" | "login" | "task" | "file" | "files" | "paste" | "help" | "exit" }
type Maintenance = { id: string; kind: "update" | "rollback"; startedAt: number; progress: string }
type SkillOption = { name: string; description: string; path: string; bundled?: boolean; saved?: boolean }
type PluginOption = { id: string; version?: string; manifest_path?: string; enabled: boolean; tools?: string[]; commands?: string[]; error?: string }
type MCPServerOption = { id: string; command: string; arguments_count: number; environment_keys: string[]; working_directory?: string }
type MCPToolOption = { server_id: string; server_tool_name: string; name: string; description: string; input_schema?: Record<string, unknown> }
type ModelToolOption = { name: string; description: string; source?: string }

const models: Model[] = [
  { id: "gpt-6-luna", label: "Luna · fast" },
  { id: "gpt-6-sol", label: "Sol · balanced" },
  { id: "gpt-6-astra", label: "Astra · deep" },
]
const efforts = ["low", "medium", "high", "xhigh", "max"]
const slashCommands: SlashCommand[] = [
  { name: "/model", description: "Choose the model for the next turn", action: "model" },
  { name: "/effort", description: "Set reasoning effort", action: "effort" },
  { name: "/tasks", description: "Browse durable agent tasks", action: "tasks" },
  { name: "/skills", description: "Browse available skills and insert one into your prompt", action: "skills" },
  { name: "/plugins", description: "Inspect installed plugins and their state", action: "plugins" },
  { name: "/plugin", description: "Enable or disable a plugin manifest", action: "plugin" },
  { name: "/mcp", description: "List, add, or remove MCP servers · configuration only changes new sessions", action: "mcp" },
  { name: "/tools", description: "Inspect the tools available to the active model session", action: "tools" },
  { name: "/update", description: "Fetch and install the latest pk release · or build a local checkout", action: "update" },
  { name: "/rollback", description: "Restore the previous managed pk release", action: "rollback" },
  { name: "/reload", description: "Restart pk and resume this session", action: "reload" },
  { name: "/task", description: "Create, attach, or control a durable task", action: "task" },
  { name: "/file", description: "Queue an explicit file for your next prompt", action: "file" },
  { name: "/files", description: "Review, remove, or clear queued files", action: "files" },
  { name: "/paste", description: "Import files or images from the system clipboard", action: "paste" },
  { name: "/new", description: "Start a fresh session", action: "new" },
  { name: "/attach", description: "Resume a saved session", action: "attach" },
  { name: "/detach", description: "Leave this session running", action: "detach" },
  { name: "/cancel", description: "Stop the current turn", action: "cancel" },
  { name: "/status", description: "Show session and model status", action: "status" },
  { name: "/login", description: "Connect or refresh ChatGPT login", action: "login" },
  { name: "/help", description: "Show keyboard shortcuts", action: "help" },
  { name: "/exit", description: "Close pk", action: "exit" },
]
const palette = {
  bg: "#111315", panel: "#181b1e", raised: "#202428", line: "#2b3035",
  text: "#e5e8eb", muted: "#858e96", dim: "#5d666e", accent: "#8ab4a1",
  blue: "#9db8d8", amber: "#d3ac72", red: "#d88787", green: "#8ab4a1",
}
const markdownStyle = SyntaxStyle.fromStyles({
  default: { fg: palette.text },
  "markup.heading": { fg: palette.text, bold: true },
  "markup.heading.1": { fg: palette.text, bold: true },
  "markup.list": { fg: palette.accent },
  "markup.link": { fg: palette.blue, underline: true },
  "markup.link.url": { fg: palette.blue, underline: true },
  "markup.raw": { fg: palette.accent },
  "markup.raw.block": { fg: palette.accent },
  "markup.quote": { fg: palette.muted, italic: true },
  "markup.bold": { fg: palette.text, bold: true },
  "markup.italic": { fg: palette.text, italic: true },
})

function shortTime(milliseconds: number) {
  const seconds = Math.max(0, Math.floor(milliseconds / 1000))
  return seconds < 60 ? `${seconds}s` : `${Math.floor(seconds / 60)}m ${seconds % 60}s`
}

function shortPath(path: string, limit: number) {
  return path.length <= limit ? path : `…${path.slice(-(limit - 1))}`
}

function preview(value: unknown, limit = 180): string {
  if (typeof value === "string") return value.length > limit ? `${value.slice(0, limit - 1)}…` : value
  if (value === undefined || value === null) return ""
  try {
    const text = JSON.stringify(value)
    return text.length > limit ? `${text.slice(0, limit - 1)}…` : text
  } catch { return "" }
}

function toolGroupKey(entries: Entry[]) {
  return entries[0]?.callId ?? `tool-${entries[0]?.id ?? "unknown"}`
}

function groupTranscript(entries: Entry[]): Array<{ kind: "entry"; entry: Entry } | { kind: "tools"; entries: Entry[] }> {
  const grouped: Array<{ kind: "entry"; entry: Entry } | { kind: "tools"; entries: Entry[] }> = []
  for (let index = 0; index < entries.length;) {
    const entry = entries[index]!
    if (entry.role !== "tool") {
      grouped.push({ kind: "entry", entry })
      index++
      continue
    }
    const run = [entry]
    while (index + run.length < entries.length) {
      const next = entries[index + run.length]!
      if (next.role !== "tool" || next.toolName !== entry.toolName) break
      run.push(next)
    }
    grouped.push({ kind: "tools", entries: run })
    index += run.length
  }
  return grouped
}

function parseSlashWords(input: string): string[] {
  const words: string[] = []
  const pattern = /"((?:\\.|[^"\\])*)"|'((?:\\.|[^'\\])*)'|((?:\\.|[^\s])+)/g
  for (const match of input.matchAll(pattern)) {
    words.push((match[1] ?? match[2] ?? match[3] ?? "").replace(/\\([\\"' ])/g, "$1"))
  }
  return words
}

type LeadingPath = { path: string; prompt: string } | { ambiguous: true }

function decodePastedPath(value: string): string {
  const unquoted = value.replace(/\\([ \\"'])/g, "$1")
  if (!unquoted.startsWith("file://")) return unquoted
  try {
    const parsed = new URL(unquoted)
    if (parsed.protocol === "file:") return decodeURIComponent(parsed.pathname)
  } catch { /* leave invalid URI as entered for an actionable backend error */ }
  return unquoted
}

function isPathShape(value: string): boolean {
  return /^(?:\/(?:[^/]+\/)+[^/]+|\.{1,2}\/[^/]+(?:\/[^/]+)*|~\/[^/]+(?:\/[^/]+)*)$/.test(value)
}

const fileExtension = /\.(?:pdf|png|jpe?g|webp|bmp|tiff?|gif|txt|md|markdown|rst|csv|tsv|json|ya?ml|toml|xml|html?|css|js|jsx|ts|tsx|go|py|rb|rs|sh|sql|log|diff|patch|svg)$/i

export function leadingPathFromPrompt(input: string): LeadingPath | undefined {
  const trimmed = input.trim()
  if (!trimmed) return
  const leadingCommand = trimmed.split(/\s/, 1)[0]
  if (slashCommands.some((command) => command.name === leadingCommand)) return

  const quoted = /^(?:"((?:\\.|[^"\\])*)"|'((?:\\.|[^'\\])*)')(?:[ \t]*([\s\S]*))?$/.exec(trimmed)
  if (quoted) {
    const path = decodePastedPath(quoted[1] ?? quoted[2] ?? "")
    if (isPathShape(path)) return { path, prompt: (quoted[3] ?? "").trim() }
  }

  const start = /^(?:file:\/\/|\/|\.{1,2}\/|~\/)/.test(trimmed)
  if (!start) return

  // Prefer a known file extension boundary over splitting at the first whitespace:
  // `/path/meeting notes.pdf summarize it` has a clear end even when unquoted.
  const extensionBoundary = /^(.*?\.(?:pdf|png|jpe?g|webp|bmp|tiff?|gif|txt|md|markdown|rst|csv|tsv|json|ya?ml|toml|xml|html?|css|js|jsx|ts|tsx|go|py|rb|rs|sh|sql|log|diff|patch|svg))(?=$|[ \t\r\n])([\s\S]*)$/i.exec(trimmed)
  if (extensionBoundary) {
    const candidate = decodePastedPath(extensionBoundary[1]!)
    if (isPathShape(candidate)) return { path: candidate, prompt: extensionBoundary[2]!.trim() }
  }

  // A quoted or shell-escaped first token is unambiguous even if its filename has spaces.
  const token = /^(?:"((?:\\.|[^"\\])*)"|'((?:\\.|[^'\\])*)'|((?:\\.|[^\s])+))(?:[ \t]+([\s\S]*))?$/.exec(trimmed)
  if (!token) return { ambiguous: true }
  const rawPath = token[1] ?? token[2] ?? token[3] ?? ""
  const path = decodePastedPath(rawPath)
  const rest = (token[4] ?? "").trim()
  if (isPathShape(path)) {
    if (rest && !token[1] && !token[2]) return { ambiguous: true }
    return { path, prompt: rest }
  }

  // An unquoted path with spaces is ambiguous. Don't consume prose as part of a filename.
  if (fileExtension.test(path)) return { path, prompt: rest }
  if (trimmed.startsWith("/") && !trimmed.slice(1).includes("/")) return
  return { ambiguous: true }
}

function hasMarkdownSyntax(content: string): boolean {
  const blockSyntax = /(?:^|\n)(?:[ \t]{0,3}#{1,6}[ \t]+|[ \t]{0,3}(?:[-*+][ \t]+|\d+[.)][ \t]+|>[ \t]?|`{3,}|~{3,})|[ \t]{0,3}(?:\*{3,}|-{3,}|_{3,}|={2,})[ \t]*$|[ \t]{4,}\S|[ \t]*\|[^\n]+\|[ \t]*(?:\n|$)|[^\n]+\n[ \t]*(?:={3,}|-{3,})[ \t]*(?:\n|$))/m
  const inlineSyntax = /\[[^\]]+\](?:\([^)]*\)|\[[^\]]*\])|\*\*[^*]+\*\*|__[^_]+__|~~[^~]+~~|`[^`]+`|\*[^*\s][^*\n]*\*|_[^_\s][^_\n]*_|(?:^|\n)[ \t]{0,3}\[[^\]]+\]:[ \t]*\S/m
  return blockSyntax.test(content) || inlineSyntax.test(content)
}

export function parsePastedPaths(input: string, mimeType = ""): { paths: string[]; prompt: string } | undefined {
  const raw = input.replace(/\r/g, "").trim()
  if (!raw) return
  const uriList = mimeType.toLowerCase() === "text/uri-list"
  const lines = raw.split("\n").map((line) => line.trim()).filter((line) => line && !line.startsWith("#"))
  if (uriList) {
    const paths = lines.map(decodePastedPath).filter((path) => /^(?:\/|\.\.?\/|~\/)/.test(path))
    return paths.length ? { paths, prompt: "" } : undefined
  }
  if (lines.length === 1) {
    const tokens = parseSlashWords(lines[0]!)
    if (tokens.length > 1 && tokens.every((path) => isPathShape(decodePastedPath(path)) && (fileExtension.test(path) || path.startsWith("file://")))) {
      return { paths: tokens.map(decodePastedPath), prompt: "" }
    }
  }
  const parsedLines = lines.map((line) => leadingPathFromPrompt(line))
  if (parsedLines.length && parsedLines.every((item) => item && "path" in item && !item.prompt)) {
    return { paths: parsedLines.map((item) => (item as { path: string }).path), prompt: "" }
  }
  const leading = leadingPathFromPrompt(raw)
  if (leading && "path" in leading) return { paths: [leading.path], prompt: leading.prompt }
}

export function PkApp({ transport, workspace, initialSession }: { transport: PkTransport; workspace: string; initialSession?: string }) {
  const renderer = useRenderer()
  const textarea = useRef<TextareaRenderable>(null)
  const [entries, setEntries] = useState<Entry[]>([{ id: 0, role: "system", text: "Ready when you are. Ask about this workspace or give me a task." }])
  const [tools, setTools] = useState<ToolActivity[]>([])
  const [model, setModel] = useState(process.env.PK_MODEL || "gpt-6-luna")
  const [effort, setEffort] = useState(process.env.PK_EFFORT || "medium")
  const [sessionId, setSessionId] = useState(initialSession || "")
  const [usage, setUsage] = useState<{ cachedInput?: number; available: boolean } | null>(null)
  const [connected, setConnected] = useState(false)
  const [everConnected, setEverConnected] = useState(false)
  const [steeringEnabled, setSteeringEnabled] = useState(false)
  const [busy, setBusy] = useState(false)
  const [selector, setSelector] = useState<"model" | "effort" | "tasks" | "skills" | "plugins" | "mcp" | "tools" | null>(null)
  const [selectionIndex, setSelectionIndex] = useState(0)
  const [clock, setClock] = useState(Date.now())
  const [activityStartedAt, setActivityStartedAt] = useState<number | null>(null)
  const [phaseStartedAt, setPhaseStartedAt] = useState<number | null>(null)
  const [streamProgress, setStreamProgressState] = useState<StreamProgress | null>(null)
  const [message, setMessage] = useState("")
  const [draft, setDraft] = useState("")
  const [slashIndex, setSlashIndex] = useState(0)
  const [tasks, setTasks] = useState<Array<{ session_id: string; updated_at?: string; title?: string }>>([])
  const [skills, setSkills] = useState<SkillOption[]>([])
  const [plugins, setPlugins] = useState<PluginOption[]>([])
  const [mcpServers, setMcpServers] = useState<MCPServerOption[]>([])
  const [mcpTools, setMcpTools] = useState<MCPToolOption[]>([])
  const [modelTools, setModelTools] = useState<ModelToolOption[]>([])
  const [mcpSavedTools, setMcpSavedTools] = useState(false)
  const [modelToolsSaved, setModelToolsSaved] = useState(false)
  const [maintenance, setMaintenance] = useState<Maintenance | null>(null)
  const [reloadPending, setReloadPending] = useState(false)
  const [copyNotice, setCopyNotice] = useState("")
  const [activeTaskId, setActiveTaskId] = useState("")
  const [question, setQuestion] = useState<PendingQuestion | null>(null)
  const [questionIndex, setQuestionIndex] = useState(0)
  const [queuedFiles, setQueuedFiles] = useState<string[]>([])
  const queuedFilesRef = useRef<string[]>([])
  const [expandedToolGroups, setExpandedToolGroups] = useState<Set<string>>(() => new Set())
  const entryId = useRef(1)
  const waiting = useRef(false)
  const turnActive = useRef(false)
  const busyRef = useRef(false)
  const promptCommandId = useRef("")
  const preferenceErrors = useRef(new Map<string, () => void>())
  const pendingPromptFiles = useRef(new Map<string, string[]>())
  const pendingClipboardRequests = useRef(new Set<string>())
  const pendingClipboardWrites = useRef(new Map<string, string>())
  const pendingSteers = useRef(new Map<string, number>())
  const steeringNegotiated = useRef(false)
  const reloadCommandId = useRef("")
  const reloadReady = useRef(false)
  const copyNoticeTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const copiedSelection = useRef("")
  const activityPhase = useRef("idle")
  const activeToolIds = useRef(new Set<string>())
  const streamDrafts = useRef(new Map<string, StreamDraft>())
  const activeStreamAttempt = useRef<ActiveStreamAttempt | null>(null)
  const toolProgress = useRef(new Map<string, { name: string; bytes: number }>())
  const streamProgressRef = useRef<StreamProgress | null>(null)
  const pendingQuestionId = useRef<string | null>(null)

  const showCopyNotice = (text: string) => {
    setCopyNotice(text)
    if (copyNoticeTimer.current) clearTimeout(copyNoticeTimer.current)
    copyNoticeTimer.current = setTimeout(() => setCopyNotice(""), 1800)
  }

  const fallbackClipboardCopy = (text: string) => {
    if (renderer.isOsc52Supported() && renderer.copyToClipboardOSC52(text)) showCopyNotice("Copy sent to terminal")
    else showCopyNotice("Clipboard copy is unavailable in this terminal")
  }

  const copyToClipboard = (text: string) => {
    if (!text) return
    const id = transport.send("clipboard_write" as any, { text })
    if (id) {
      pendingClipboardWrites.current.set(id, text)
      setCopyNotice("Copying…")
    } else fallbackClipboardCopy(text)
  }

  useSelectionHandler((selection) => {
    if (selection.isDragging) return
    const selected = selection.getSelectedText()
    if (!selected.trim()) {
      copiedSelection.current = ""
      return
    }
    if (selected === copiedSelection.current) return
    copiedSelection.current = selected
    copyToClipboard(selected)
  })

  const enterPhase = (phase: string) => {
    const resolved = pendingQuestionId.current && phase !== "idle" && !phase.startsWith("question:")
      ? `question:${pendingQuestionId.current}`
      : phase
    if (activityPhase.current === resolved) return
    activityPhase.current = resolved
    setPhaseStartedAt(resolved === "idle" ? null : Date.now())
  }

  const updateFileQueue = (update: string[] | ((current: string[]) => string[])) => {
    const next = typeof update === "function" ? update(queuedFilesRef.current) : update
    queuedFilesRef.current = next
    setQueuedFiles(next)
  }

  const clearComposer = (focus = false) => {
    textarea.current?.clear()
    setDraft("")
    if (focus) textarea.current?.focus()
  }

  const addEntry = (role: Role, text: string) => {
    if (!text.trim()) return
    setEntries((previous) => [...previous, { id: entryId.current++, role, text }].slice(-300))
  }

  const updateEntry = (id: number, update: (entry: Entry) => Entry) => {
    setEntries((current) => current.map((entry) => entry.id === id ? update(entry) : entry))
  }

  const finishPendingSteers = (message: string) => {
    const ids = new Set(pendingSteers.current.values())
    if (ids.size) setEntries((current) => current.map((entry) => ids.has(entry.id) ? { ...entry, delivery: "rejected", deliveryMessage: message } : entry))
    pendingSteers.current.clear()
  }

  const setStreamProgress = (next: StreamProgress | null) => {
    streamProgressRef.current = next
    setStreamProgressState(next)
  }

  const clearStreamDraft = (outerId?: string, requestId?: string, attempt?: number, keepProgress = false) => {
    const matches = [...streamDrafts.current.entries()].filter(([, draft]) =>
      (!outerId || draft.outerId === outerId)
      && (!requestId || draft.requestId === requestId)
      && (attempt === undefined || draft.attempt === attempt))
    const removedIds = new Set(matches.map(([, draft]) => draft.entryId))
    if (removedIds.size) setEntries((entries) => entries.filter((entry) => !removedIds.has(entry.id)))
    for (const [key] of matches) streamDrafts.current.delete(key)

    const progress = streamProgressRef.current
    const ownsState = !outerId || activeStreamAttempt.current?.outerId === outerId || progress?.outerId === outerId
    if (ownsState) {
      if (!requestId || activeStreamAttempt.current?.requestId === requestId) activeStreamAttempt.current = null
      if (!keepProgress) setStreamProgress(null)
    }
  }

  const handleModelProgress = (event: ServerEvent) => {
    const data = event.payload ?? {}
    const outerId = String(event.id ?? data.prompt_id ?? "")
    const requestId = String(data.request_id ?? "")
    const attempt = Number.isFinite(data.attempt) ? Math.max(1, Number(data.attempt)) : 1
    const phase = String(data.phase ?? "")
    if (!outerId || !requestId || !promptCommandId.current || outerId !== promptCommandId.current) return

    const active = activeStreamAttempt.current
    if (active?.outerId === outerId && attempt < active.attempt) return
    if (active?.outerId === outerId && attempt === active.attempt && active.requestId !== requestId) return
    if (active && (active.outerId !== outerId || active.requestId !== requestId || attempt > active.attempt)) {
      if (active.outerId === outerId) clearStreamDraft(outerId)
      else clearStreamDraft(active.outerId)
      for (const key of toolProgress.current.keys()) {
        if (key.startsWith(`${active.requestId}:${active.attempt}:`)) toolProgress.current.delete(key)
      }
    }
    activeStreamAttempt.current = { outerId, requestId, attempt }

    const toolName = typeof data.tool_name === "string" ? data.tool_name.slice(0, 40) : "tool"
    let label: string
    let phaseKey: string
    switch (phase) {
      case "attempt_started":
        label = attempt > 1 ? `Retrying model · attempt ${attempt}` : "Sending request to model"
        phaseKey = `request:${requestId}:attempt:${attempt}`
        break
      case "response_started":
        label = "Waiting for model response"
        phaseKey = `response:${requestId}:${attempt}`
        break
      case "assistant_delta":
        label = "Receiving response"
        phaseKey = `receiving:${requestId}:${attempt}`
        break
      case "tool_call_started":
        toolProgress.current.set(`${requestId}:${attempt}:${String(data.item_id ?? toolName)}`, { name: toolName, bytes: 0 })
        label = `Preparing ${toolName} tool call`
        phaseKey = `preparing:${requestId}:${attempt}:${String(data.item_id ?? toolName)}`
        break
      case "tool_arguments_progress": {
        const itemId = String(data.item_id ?? toolName)
        const key = `${requestId}:${attempt}:${itemId}`
        const current = toolProgress.current.get(key) ?? { name: toolName, bytes: 0 }
        current.bytes += Number.isFinite(data.bytes) ? Math.max(0, Number(data.bytes)) : 0
        toolProgress.current.set(key, current)
        label = `Preparing ${current.name} tool call · ${current.bytes} bytes`
        phaseKey = `preparing:${requestId}:${attempt}:${itemId}`
        break
      }
      case "tool_call_ready":
        label = `Running ${toolName} tool`
        phaseKey = `tool-ready:${requestId}:${attempt}:${String(data.item_id ?? toolName)}`
        break
      case "response_completed":
        label = "Response received"
        phaseKey = `completed:${requestId}:${attempt}`
        break
      case "response_incomplete":
        label = "Response incomplete"
        phaseKey = `incomplete:${requestId}:${attempt}`
        break
      case "response_failed":
        label = "Retrying model response"
        phaseKey = `response-failed:${requestId}:${attempt}`
        break
      case "attempt_failed":
        label = data.status === "retrying" ? `Retrying model · attempt ${attempt + 1}` : "Model attempt failed"
        phaseKey = `attempt-failed:${requestId}:${attempt}`
        clearStreamDraft(outerId, requestId, attempt)
        break
      case "request_failed":
        label = "Model request failed"
        phaseKey = `request-failed:${requestId}:${attempt}`
        clearStreamDraft(outerId, requestId, attempt)
        toolProgress.current.clear()
        break
      default:
        return
    }

    setStreamProgress({ outerId, requestId, label })
    enterPhase(pendingQuestionId.current ? `question:${pendingQuestionId.current}` : phaseKey)

    if (phase === "response_completed" || phase === "response_incomplete") {
      clearStreamDraft(outerId, requestId, attempt, true)
      for (const key of toolProgress.current.keys()) if (key.startsWith(`${requestId}:${attempt}:`)) toolProgress.current.delete(key)
    }
    if (phase !== "assistant_delta" || typeof data.text_delta !== "string" || !data.text_delta) return
    const itemId = String(data.item_id ?? "assistant")
    const key = `${outerId}:${requestId}:${attempt}:${itemId}`
    const previous = streamDrafts.current.get(key)
    const streamedEntry: StreamDraft = {
      outerId, requestId, attempt, itemId,
      entryId: previous?.entryId ?? entryId.current++,
      text: `${previous?.text ?? ""}${data.text_delta}`.slice(-64 * 1024),
    }
    streamDrafts.current.set(key, streamedEntry)
    const entry: Entry = { id: streamedEntry.entryId, role: "assistant", text: streamedEntry.text, provisional: true }
    setEntries((entries) => entries.some((current) => current.id === entry.id)
      ? entries.map((current) => current.id === entry.id ? entry : current)
      : [...entries, entry].slice(-300))
  }

  const requestClipboardPaste = () => {
    const id = transport.send("clipboard_paste" as any)
    if (id) pendingClipboardRequests.current.add(id)
    else addEntry("system", "Clipboard paste is unavailable until pk connects.")
  }

  const trackTool = (data: Record<string, any>) => {
    const callId = String(data.call_id ?? data.id ?? `tool-${Date.now()}`)
    const name = String(data.name ?? data.tool ?? "tool")
    if (name.toLowerCase() === "askuser") return
    const rawStatus = data.state ?? data.status?.status ?? data.status
    const status = typeof rawStatus === "string" ? rawStatus : "running"
    const startedAt = Date.now() - (Number.isFinite(data.elapsed_ms) ? Math.max(0, Number(data.elapsed_ms)) : 0)
    const operation = Array.isArray(data.operations) ? data.operations.map((item: any) => {
      const output = preview(item?.output_excerpt)
      const error = preview(item?.error_excerpt)
      return [item?.type ?? "operation", item?.state, error || output, item?.exit_code === undefined ? "" : `exit ${item.exit_code}`].filter(Boolean).join(" · ")
    }).filter(Boolean).join("\n") : ""
    const detail = preview(operation, 900)
    const terminal = ["completed", "complete", "failed", "canceled", "cancelled", "succeeded", "interrupted"].includes(status.toLowerCase())
    if (terminal) activeToolIds.current.delete(callId)
    else activeToolIds.current.add(callId)
    enterPhase(activeToolIds.current.size ? "tools" : "model")
    const error = preview(data.status?.error ?? data.error)
    const displayState = error ? "failed" : terminal ? status : status === "awaiting" ? "working" : status
    setEntries((current) => {
      const previous = current.find((entry) => entry.callId === callId)
      const next: Entry = {
        id: previous?.id ?? entryId.current++, role: "tool", text: error || detail,
        callId, toolName: name, toolState: displayState,
        startedAt: previous?.startedAt ?? startedAt,
        elapsedMs: Number.isFinite(data.elapsed_ms) ? Number(data.elapsed_ms) : undefined,
        commandPreview: preview(data.command_preview || data.arguments_preview, 120),
        detail,
      }
      return (previous ? current.map((entry) => entry.callId === callId ? next : entry) : [...current, next]).slice(-300)
    })
    setTools((current) => terminal ? current.filter((item) => item.id !== callId) : [
      ...current.filter((item) => item.id !== callId),
      { id: callId, name, state: displayState, startedAt: current.find((item) => item.id === callId)?.startedAt ?? startedAt, detail },
    ])
  }

  useEffect(() => {
    const tick = setInterval(() => setClock(Date.now()), 1000)
    return () => clearInterval(tick)
  }, [])
  useEffect(() => () => { if (copyNoticeTimer.current) clearTimeout(copyNoticeTimer.current) }, [])
  useEffect(() => { setSlashIndex(0) }, [draft])
  useEffect(() => { busyRef.current = busy }, [busy])

  useEffect(() => {
    void transport.start({ workspace, model: "", effort: "", sessionId: initialSession, steering: true })
  }, [transport, workspace, initialSession])

  const handleEvent = useRef<(event: ServerEvent) => void>(() => {})
  handleEvent.current = (event) => {
    const data = event.payload ?? {}
    switch (event.type) {
      case "ready":
        setConnected(true)
        setEverConnected(true)
        if (Array.isArray(data.capabilities)) steeringNegotiated.current = data.capabilities.includes("steer")
        setSteeringEnabled(steeringNegotiated.current)
        if (data.model) setModel(data.model)
        if (data.effort) setEffort(data.effort)
        if (data.workspace) setMessage(String(data.workspace))
        setEntries((current) => current.filter((entry) => entry.text !== "Starting a new session…"))
        break
      case "session":
        if (data.session_id) setSessionId(String(data.session_id))
        break
      case "history": {
        const restored = Array.isArray(data.entries)
          ? data.entries.flatMap((item: any) => {
              const role = item?.role === "user" || item?.role === "assistant" ? item.role : null
              const text = typeof item?.text === "string" ? item.text : ""
              return role && text.trim() ? [{ id: entryId.current++, role, text } as Entry] : []
            })
          : []
        const notices: Entry[] = data.truncated ? [{ id: entryId.current++, role: "system", text: "Showing recent conversation history; earlier messages were omitted." }] : []
        setEntries([...notices, ...restored].slice(-300))
        if (data.session_id) setSessionId(String(data.session_id))
        break
      }
      case "question": {
        const kind = data.kind === "confirmation" ? "confirmation" : "question"
        const supplied = Array.isArray(data.choices) ? data.choices.filter((item: unknown) => typeof item === "string" && item.trim()).map(String) : []
        const choices = supplied.length ? supplied : kind === "confirmation" ? ["Yes", "No"] : []
        const text = String(data.text ?? "The agent needs an answer.")
        addEntry("system", `AskUser · ${text}`)
        setActivityStartedAt((current) => current ?? Date.now())
        pendingQuestionId.current = String(data.id ?? "pending")
        enterPhase(`question:${pendingQuestionId.current}`)
        setQuestion({ id: String(data.id ?? ""), text, choices, kind })
        setQuestionIndex(0)
        break
      }
      case "question_answered":
        if (question?.id === String(data.id ?? "")) {
          if (question.submittedAnswer) addEntry("user", `Answer · ${question.submittedAnswer}`)
          setQuestion(null)
          pendingQuestionId.current = null
          setStreamProgress(null)
          enterPhase(activeToolIds.current.size ? "tools" : "model")
        }
        break
      case "question_cancelled":
        setQuestion((current) => current?.id === String(data.id ?? "") ? null : current)
        pendingQuestionId.current = null
        setStreamProgress(null)
        enterPhase(activeToolIds.current.size ? "tools" : "model")
        break
      case "turn_started":
        setBusy(true)
        setActivityStartedAt((current) => current ?? Date.now())
        activeToolIds.current.clear()
        toolProgress.current.clear()
        enterPhase("model")
        setTools([])
        turnActive.current = true
        break
      case "model_progress":
        handleModelProgress(event)
        break
      case "input_queued": {
        const requestId = String(event.id ?? data.command_id ?? "")
        const entryId = pendingSteers.current.get(requestId)
        if (entryId !== undefined) updateEntry(entryId, (entry) => ({ ...entry, delivery: "queued", deliveryMessage: undefined }))
        break
      }
      case "input_accepted": {
        const requestId = String(event.id ?? data.command_id ?? "")
        const entryId = pendingSteers.current.get(requestId)
        if (entryId !== undefined) {
          updateEntry(entryId, (entry) => ({ ...entry, delivery: "accepted", deliveryMessage: undefined }))
          pendingSteers.current.delete(requestId)
        }
        break
      }
      case "input_rejected": {
        const requestId = String(event.id ?? data.command_id ?? "")
        const entryId = pendingSteers.current.get(requestId)
        const reason = String(data.message ?? "The active turn could not accept this message.")
        if (entryId !== undefined) {
          const messageEntry = entries.find((entry) => entry.id === entryId)
          updateEntry(entryId, (entry) => ({ ...entry, delivery: "rejected", deliveryMessage: reason }))
          pendingSteers.current.delete(requestId)
          if (!(textarea.current?.plainText ?? "").trim() && messageEntry) {
            textarea.current?.setText(messageEntry.text)
            textarea.current?.focus()
            setDraft(messageEntry.text)
          }
        }
        addEntry("system", `Steering message not accepted · ${reason}`)
        break
      }
      case "attachments_loaded": {
        const submitted = event.id ? pendingPromptFiles.current.get(event.id) : undefined
        if (event.id) pendingPromptFiles.current.delete(event.id)
        if (submitted?.length) {
          updateFileQueue((current) => current.filter((path) => !submitted.includes(path)))
          const files = Array.isArray(data.files) ? data.files : []
          const descriptions = files.map((file: any) => {
            const name = String(file?.path ?? "file").split(/[\\/]/).pop() || "file"
            const type = String(file?.kind ?? file?.content_type ?? "file")
            const pages = Number(file?.pages_extracted ?? file?.pages_total)
            return `${name} (${type}${Number.isFinite(pages) && pages > 0 ? `, ${pages} pages` : ""}${file?.truncated ? ", truncated" : ""})`
          })
          addEntry("system", `Loaded ${descriptions.join(" · ") || `${submitted.length} attachment${submitted.length === 1 ? "" : "s"}`}`)
        }
        break
      }
      case "clipboard_files": {
        if (event.id && !pendingClipboardRequests.current.has(event.id)) break
        if (event.id) pendingClipboardRequests.current.delete(event.id)
        for (const file of Array.isArray(data.files) ? data.files : []) {
          const path = typeof file?.path === "string" ? file.path : ""
          if (path) queueFile(path)
        }
        if (typeof data.text === "string" && data.text) {
          const pasted = parsePastedPaths(data.text)
          if (pasted) {
            for (const path of pasted.paths) queueFile(path)
            if (pasted.prompt) textarea.current?.insertText(pasted.prompt)
          } else textarea.current?.insertText(data.text)
        }
        if (data.message) addEntry("system", String(data.message))
        break
      }
      case "assistant":
        clearStreamDraft(String(event.id ?? ""))
        toolProgress.current.clear()
        enterPhase(activeToolIds.current.size ? "tools" : "model")
        if (data.text) addEntry("assistant", String(data.text))
        break
      case "tool": {
        if (streamProgressRef.current?.outerId === String(event.id ?? "")) {
          setStreamProgress(null)
          activeStreamAttempt.current = null
        }
        trackTool(data)
        break
      }
      case "tool_call": {
        if (streamProgressRef.current?.outerId === String(event.id ?? "")) {
          setStreamProgress(null)
          activeStreamAttempt.current = null
        }
        trackTool(data)
        break
      }
      case "turn_finished":
        finishPendingSteers("The turn ended before this message was accepted.")
        clearStreamDraft()
        toolProgress.current.clear()
        setBusy(false)
        setActivityStartedAt(null)
        enterPhase("idle")
        pendingQuestionId.current = null
        activeToolIds.current.clear()
        setTools([])
        waiting.current = false
        turnActive.current = false
        setQuestion(null)
        promptCommandId.current = ""
        if (data.session_id) setSessionId(String(data.session_id))
        break
      case "status":
        if (data.session_id) setSessionId(String(data.session_id))
        if (data.model) setModel(String(data.model))
        if (data.effort) setEffort(String(data.effort))
        if (data.usage) setUsage({ cachedInput: data.usage.cached_input_tokens, available: data.usage.cached_input_tokens_available === true })
        break
      case "usage":
        setUsage({ cachedInput: data.cached_input_tokens, available: data.cached_input_tokens_available === true })
        break
      case "tasks":
      case "sessions":
        setTasks((Array.isArray(data.sessions) ? data.sessions : []).map((task: any) => ({ ...task, kind: "session" })) as any)
        break
      case "task_list":
        {
          const available = (Array.isArray(data.tasks) ? data.tasks : []).map((task: any) => ({ ...task, kind: "task" }))
          setTasks(available as any)
          if (available.length) {
            setSelector("tasks")
            setSelectionIndex(0)
          } else addEntry("system", "No durable tasks yet. Use /task new PROMPT to start one.")
        }
        break
      case "skill_catalog": {
        const available = Array.isArray(data.skills) ? data.skills.filter((item: any) => item && typeof item.name === "string").map((item: any) => ({
          name: String(item.name), description: String(item.description ?? ""), path: String(item.path ?? ""), bundled: item.bundled === true, saved: item.saved === true,
        })) : []
        setSkills(available)
        setSelectionIndex(0)
        setSelector("skills")
        for (const warning of Array.isArray(data.warnings) ? data.warnings : []) addEntry("system", `Skills · ${String(warning)}`)
        if (!available.length) addEntry("system", "No skills are available in this workspace or your configured skill directories.")
        break
      }
      case "plugins":
      case "plugins_updated": {
        const available = Array.isArray(data.plugins) ? data.plugins.filter((item: any) => item && typeof item.id === "string").map((item: any) => ({
          id: String(item.id), version: typeof item.version === "string" ? item.version : undefined,
          manifest_path: typeof item.manifest_path === "string" ? item.manifest_path : undefined,
          enabled: item.enabled === true, tools: Array.isArray(item.tools) ? item.tools.map(String) : [],
          commands: Array.isArray(item.commands) ? item.commands.map(String) : [], error: typeof item.error === "string" ? item.error : undefined,
        })) : []
        setPlugins(available)
        if (selector !== "plugins") setSelectionIndex(0)
        setSelector("plugins")
        if (event.type === "plugins_updated" && data.next_session_only) addEntry("system", "Plugin configuration saved · applies to new sessions. Use /new to start one.")
        if (!available.length) addEntry("system", "No plugins are installed.")
        break
      }
      case "mcp_catalog":
      case "mcp_updated": {
        const servers = Array.isArray(data.servers) ? data.servers.filter((item: any) => item && typeof item.id === "string").map((item: any) => ({
          id: String(item.id), command: String(item.command ?? ""), arguments_count: Number(item.arguments_count ?? 0),
          environment_keys: Array.isArray(item.environment_keys) ? item.environment_keys.map(String) : [],
          working_directory: typeof item.working_directory === "string" ? item.working_directory : undefined,
        })) : []
        const tools = Array.isArray(data.tools) ? data.tools.filter((item: any) => item && typeof item.name === "string").map((item: any) => ({
          server_id: String(item.server_id ?? ""), server_tool_name: String(item.server_tool_name ?? ""),
          name: String(item.name), description: String(item.description ?? ""),
        })) : []
        setMcpServers(servers)
        setMcpTools(tools)
        setMcpSavedTools(data.saved_session_tools === true)
        if (selector !== "mcp") setSelectionIndex(0)
        setSelector("mcp")
        if (event.type === "mcp_updated" && data.next_session_only) addEntry("system", "MCP configuration saved · applies to new sessions. Use /new to start one.")
        if (!servers.length) addEntry("system", 'No MCP servers configured. Use /mcp add --id ID --command PATH to add one.')
        break
      }
      case "tool_catalog": {
        const available = Array.isArray(data.tools) ? data.tools.filter((item: any) => item && typeof item.name === "string").map((item: any) => ({
          name: String(item.name), description: String(item.description ?? ""), source: typeof item.source === "string" ? item.source : undefined,
        })) : []
        setModelTools(available)
        setModelToolsSaved(data.saved === true)
        setSelectionIndex(0)
        setSelector("tools")
        if (!available.length) addEntry("system", "No model tools are available in this session.")
        break
      }
      case "clipboard_written": {
        if (event.id && pendingClipboardWrites.current.delete(event.id)) showCopyNotice("Copied to clipboard")
        break
      }
      case "update_started":
      case "rollback_started": {
        const kind = event.type === "update_started" ? "update" : "rollback"
        const progress = String(data.text ?? (kind === "update" ? "Preparing build" : "Restoring previous release"))
        setMaintenance({ id: String(event.id ?? ""), kind, startedAt: Date.now(), progress })
        addEntry("system", `${kind === "update" ? "Update" : "Rollback"} started · ${progress}`)
        break
      }
      case "update_progress":
      case "rollback_progress": {
        const id = String(event.id ?? "")
        const progress = String(data.text ?? "Working…").slice(0, 240)
        if (maintenance && (!id || id === maintenance.id) && maintenance.progress !== progress) {
          setMaintenance({ ...maintenance, progress })
          addEntry("system", progress)
        }
        break
      }
      case "update_cancel_requested":
        if (maintenance?.kind === "update" && (!event.id || event.id === maintenance.id)) {
          setMaintenance({ ...maintenance, progress: "Stopping update" })
          addEntry("system", "Update cancellation requested…")
        }
        break
      case "update_finished":
      case "rollback_finished": {
        const id = String(event.id ?? "")
        if (maintenance && (!id || id === maintenance.id)) {
          const succeeded = data.success === true || Number(data.exit_code) === 0
          const detail = String(data.message ?? (succeeded ? "Complete." : `Failed with exit code ${data.exit_code ?? "unknown"}.`))
          const canceled = !succeeded && /cancel/i.test(detail)
          addEntry("system", `${maintenance.kind === "update" ? "Update" : "Rollback"} ${succeeded ? "complete" : canceled ? "canceled" : "failed"} · ${detail}${succeeded ? " Use /reload to run the saved session on the new release." : canceled ? " The active release was not changed." : " Review the build output, then try again."}`)
          setMaintenance(null)
        }
        break
      }
      case "reload_ready":
        reloadReady.current = true
        setReloadPending(true)
        addEntry("system", "Session saved · restarting pk…")
        transport.send("reload_exit" as any)
        break
      case "reload_rejected":
      case "reload_failed":
        reloadCommandId.current = ""
        setReloadPending(false)
        addEntry("system", String(data.message ?? "Reload was rejected; the session is still running."))
        break
      case "skill_document": {
        const name = String(data.skill?.name ?? "skill")
        const instruction = `Use the ${name} skill for this task.`
        const current = textarea.current?.plainText ?? draft
        const inserted = current ? `${current}${current.endsWith("\n") ? "" : "\n"}${instruction}` : instruction
        textarea.current?.setText(inserted)
        textarea.current?.focus()
        setDraft(inserted)
        setSelector(null)
        addEntry("system", `Inserted an instruction for the ${name} skill. Review it in the composer, then send when ready.`)
        break
      }
      case "task_created":
        addEntry("system", `Task started in the background · ${String(data.task_id ?? data.id ?? "task")}`)
        break
      case "task_attached": {
        const taskBusy = ["running", "queued", "awaiting", "canceling"].includes(String(data.status))
        setActiveTaskId(String(data.task_id ?? data.id ?? ""))
        if (data.session_id) setSessionId(String(data.session_id))
        if (data.workspace) setMessage(String(data.workspace))
        setBusy(taskBusy)
        setActivityStartedAt((current) => taskBusy ? current ?? Date.now() : null)
        enterPhase(taskBusy ? "model" : "idle")
        addEntry("system", `Following task ${String(data.task_id ?? data.id ?? "")}`)
        break
      }
      case "task_resumed":
        setActiveTaskId(String(data.task_id ?? data.id ?? ""))
        setBusy(true)
        setActivityStartedAt((current) => current ?? Date.now())
        enterPhase("model")
        addEntry("system", `Resumed task ${String(data.task_id ?? data.id ?? "")}`)
        break
      case "task_input_sent":
        break
      case "task_cancelled":
        addEntry("system", `Task ${String(data.task_id ?? "")} canceled.`)
        break
      case "task_output":
        if (data.text) addEntry(data.role === "user" ? "user" : "assistant", String(data.text))
        if (data.status || data.tool) addEntry("system", String(data.message ?? `${data.status ?? "running"} ${data.tool ?? ""}`))
        break
      case "task_finished":
        finishPendingSteers("The task ended before this message was accepted.")
        clearStreamDraft()
        toolProgress.current.clear()
        if (data.text) addEntry("assistant", String(data.text))
        addEntry("system", `Task ${String(data.status ?? "finished")}`)
        setActiveTaskId("")
        setActivityStartedAt(null)
        enterPhase("idle")
        if (data.session_id) setSessionId(String(data.session_id))
        setBusy(false)
        waiting.current = false
        setQuestion(null)
        break
      case "detached":
        addEntry("system", `Detached from session ${sessionId || ""}`)
        setSessionId("")
        setBusy(false)
        setActivityStartedAt(null)
        enterPhase("idle")
        setTools([])
        void transport.close().finally(() => renderer.destroy())
        break
      case "error":
        if (event.id && pendingClipboardWrites.current.has(event.id) && (data.command_type === "clipboard_write" || data.request_type === "clipboard_write")) {
          const text = pendingClipboardWrites.current.get(event.id)!
          pendingClipboardWrites.current.delete(event.id)
          fallbackClipboardCopy(text)
          break
        }
        addEntry("system", String(data.message ?? "The agent encountered an error."))
        if (event.id && reloadCommandId.current === event.id) { reloadCommandId.current = ""; setReloadPending(false) }
        if (maintenance && event.id === maintenance.id) setMaintenance(null)
        if ((data.command_type === "steer" || data.request_type === "steer") && event.id) {
          const entryId = pendingSteers.current.get(event.id)
          if (entryId !== undefined) {
            const messageEntry = entries.find((entry) => entry.id === entryId)
            const reason = String(data.message ?? "The active turn could not accept this message.")
            updateEntry(entryId, (entry) => ({ ...entry, delivery: "rejected", deliveryMessage: reason }))
            pendingSteers.current.delete(event.id)
            if (!(textarea.current?.plainText ?? "").trim() && messageEntry) {
              textarea.current?.setText(messageEntry.text)
              textarea.current?.focus()
              setDraft(messageEntry.text)
            }
          }
        }
        if (data.command_type === "answer_question" || data.request_type === "answer_question") {
          setQuestion((current) => current ? { ...current, answering: false, submittedAnswer: undefined } : current)
        }
        if (event.id && preferenceErrors.current.has(event.id)) {
          preferenceErrors.current.get(event.id)?.()
          preferenceErrors.current.delete(event.id)
        }
        const isPromptError = Boolean(event.id && promptCommandId.current && event.id === promptCommandId.current)
          || data.command_type === "prompt"
          || data.request_type === "prompt"
        if (isPromptError) {
          clearStreamDraft(event.id ? String(event.id) : undefined)
          if (event.id) pendingPromptFiles.current.delete(event.id)
          waiting.current = false
          turnActive.current = false
          promptCommandId.current = ""
          setBusy(false)
          setActivityStartedAt(null)
          enterPhase("idle")
          setTools([])
        } else if (!turnActive.current && !activeTaskId && !waiting.current) {
          setBusy(false)
          setActivityStartedAt(null)
          enterPhase("idle")
          setTools([])
        }
        break
      case "log": {
        const line = String(data.text ?? "")
        if (!/^\[pk\] (Running|Finished) /.test(line)) addEntry("system", line)
        break
      }
      case "rpc_closed": {
        if (reloadReady.current) {
          void transport.close().finally(() => {
            renderer.destroy()
            process.exitCode = 75
          })
          break
        }
        finishPendingSteers("The connection closed before this message was accepted.")
        clearStreamDraft()
        toolProgress.current.clear()
        setConnected(false)
        setSteeringEnabled(false)
        steeringNegotiated.current = false
        setBusy(false)
        setActivityStartedAt(null)
        enterPhase("idle")
        setEntries((entries) => entries.map((entry) => entry.role === "tool" && !["completed", "complete", "failed", "canceled", "cancelled", "succeeded", "interrupted"].includes((entry.toolState ?? "").toLowerCase())
          ? { ...entry, toolState: "interrupted", text: entry.text || "Agent connection closed before this tool finished." }
          : entry))
        setTools([])
        setQuestion(null)
        setMaintenance(null)
        setReloadPending(false)
        pendingQuestionId.current = null
        waiting.current = false
        turnActive.current = false
        promptCommandId.current = ""
        pendingPromptFiles.current.clear()
        setActiveTaskId("")
        addEntry("system", "Agent connection closed. Relaunch pk to reconnect.")
        break
      }
    }
  }

  // Transport hands events through a stable callback so the view remains the only state owner.
  useEffect(() => {
    transport.setEventHandler((event: ServerEvent) => handleEvent.current(event))
  }, [transport])

  const queueFile = (path: string): boolean => {
    if (!path) {
      addEntry("system", 'Usage: /file PATH · quote paths containing spaces, for example /file "docs/meeting notes.pdf"')
      return false
    }
    if (path.length > 4096) {
      addEntry("system", "File path is too long (maximum 4096 characters).")
      return false
    }
    const current = queuedFilesRef.current
    if (current.includes(path)) {
      addEntry("system", `Already queued · ${path}`)
      return true
    }
    if (current.length >= 8) {
      addEntry("system", "The attachment queue is full (8 files). Send a prompt or remove a file first.")
      return false
    }
    updateFileQueue([...current, path])
    addEntry("system", `Queued file ${current.length + 1}/8 · ${path}`)
    return true
  }

  usePaste((event) => {
    const pasted = new TextDecoder().decode(event.bytes)
    const parsed = parsePastedPaths(pasted, event.metadata?.mimeType ?? "")
    if (!parsed) return
    event.preventDefault()
    let accepted = true
    for (const path of parsed.paths) accepted = queueFile(path) && accepted
    if (parsed.prompt) {
      textarea.current?.insertText(parsed.prompt)
      setDraft(textarea.current?.plainText ?? `${draft}${parsed.prompt}`)
    } else if (accepted) {
      addEntry("system", `Added ${parsed.paths.length} pasted file${parsed.paths.length === 1 ? "" : "s"} to the next prompt.`)
    }
  })

  const sendPrompt = () => {
    const value = textarea.current?.plainText ?? draft
    const text = value.trim()
    if (question) {
      if (question.answering) return
      const answer = text || question.choices[questionIndex] || ""
      if (!answer) return
      transport.send("answer_question" as any, { id: question.id, answer })
      setQuestion({ ...question, answering: true, submittedAnswer: answer })
      clearComposer(true)
      return
    }
    if (!text || !connected) return
    const leadingPath = leadingPathFromPrompt(text)
    if (leadingPath && "ambiguous" in leadingPath) {
      addEntry("system", 'That looks like a file path with spaces. Quote the path, for example: "/path/to/meeting notes.pdf" describe this file.')
      return
    }
    let promptText = text
    let includedLeadingPath = false
    if (leadingPath && "path" in leadingPath) {
      if (!queueFile(leadingPath.path)) return
      includedLeadingPath = true
      promptText = leadingPath.prompt
      if (!promptText) promptText = "Inspect the attached file."
    }
    if (!includedLeadingPath && promptText.startsWith("/")) {
      const [commandName, subcommand] = parseSlashWords(promptText)
      if (commandName === "/task" && subcommand === "new" && queuedFilesRef.current.length) {
        addEntry("system", "Queued files are not sent to background tasks. Clear the queue with /files clear before creating one; files remain queued for a foreground prompt.")
        return
      }
      runSlashCommand(promptText)
      clearComposer(true)
      return
    }
    if (maintenance || reloadCommandId.current) {
      addEntry("system", maintenance ? `${maintenance.kind === "update" ? "Update" : "Rollback"} is still running. Your draft is kept.` : "Reload is saving this session. Your draft is kept.")
      return
    }
    if (activeTaskId && queuedFilesRef.current.length) {
      addEntry("system", "File attachments are not supported for this background task. The draft and file queue are kept; attachments work with foreground prompts.")
      return
    }
    if (!activeTaskId && (waiting.current || turnActive.current || busyRef.current)) {
      if (queuedFilesRef.current.length) {
        addEntry("system", "Steering messages cannot include queued file attachments yet. The draft and file queue are kept; send after this turn or clear files with /files clear.")
        return
      }
      if (!steeringEnabled) {
        addEntry("system", "This session does not support mid-turn steering. Your draft is kept; use Esc to stop the active turn, then send it.")
        return
      }
      const commandId = transport.send("steer" as any, { text: promptText })
      if (!commandId) {
        addEntry("system", "Could not queue this steering message because the agent connection is unavailable. Your draft is kept.")
        return
      }
      const id = entryId.current++
      pendingSteers.current.set(commandId, id)
      const steeringEntry: Entry = { id, role: "user", text: promptText, delivery: "queued" }
      setEntries((previous) => [...previous, steeringEntry].slice(-300))
      clearComposer(true)
      return
    }
    addEntry("user", promptText)
    clearComposer(true)
    if (activeTaskId) {
      transport.send("send_input" as any, { task_id: activeTaskId, text: promptText })
      return
    }
    waiting.current = true
    setBusy(true)
    setActivityStartedAt(Date.now())
    activeToolIds.current.clear()
    enterPhase("model")
    const files = [...queuedFilesRef.current]
    const commandId = transport.send("prompt", { text: promptText, ...(files.length ? { files } : {}) }) ?? ""
    promptCommandId.current = commandId
    if (commandId && files.length) pendingPromptFiles.current.set(commandId, files)
  }

  const setModelPreference = (nextModel: string, nextEffort: string) => {
    const previous = { model, effort }
    setModel(nextModel)
    setEffort(nextEffort)
    const id = transport.send("set_model", { model: nextModel, effort: nextEffort })
    if (id) preferenceErrors.current.set(id, () => { setModel(previous.model); setEffort(previous.effort) })
  }

  const runSlashCommand = (raw: string) => {
    const [head, ...args] = parseSlashWords(raw)
    const command = slashCommands.find((item) => item.name === head)
    if (!command) { addEntry("system", `Unknown command: ${head}. Type /help to see available commands.`); return }
    switch (command.action) {
      case "model":
        if (args[0]) {
          const selectedModel = args[0]
          const selectedEffort = args[1] || effort
          if (!efforts.includes(selectedEffort)) {
            addEntry("system", `Unsupported reasoning effort: ${selectedEffort}. Choose ${efforts.join(", ")}.`)
            break
          }
          setModelPreference(selectedModel, selectedEffort)
        } else { setSelector("model"); setSelectionIndex(Math.max(0, models.findIndex((item) => item.id === model))) }
        break
      case "effort":
        if (args[0] && efforts.includes(args[0])) setModelPreference(model, args[0])
        else if (!args[0]) { setSelector("effort"); setSelectionIndex(Math.max(0, efforts.indexOf(effort))) }
        else addEntry("system", `Unsupported reasoning effort: ${args[0]}. Choose ${efforts.join(", ")}.`)
        break
      case "tasks": transport.send("task_list"); break
      case "skills": transport.send("skills" as any); break
      case "plugins": transport.send("plugins_list" as any); break
      case "mcp": {
        const [operation, ...options] = args
        if (!operation || operation === "list") transport.send("mcp_list" as any)
        else if (operation === "remove" && options[0]) transport.send("mcp_remove" as any, { id: options[0] })
        else if (operation === "add") {
          const server: { id?: string; command?: string; args: string[]; env: Record<string, string>; working_directory?: string } = { args: [], env: {} }
          let invalid = ""
          for (let index = 0; index < options.length; index++) {
            const option = options[index]!
            const value = options[++index]
            if (value === undefined) { invalid = `Missing value after ${option}.`; break }
            if (option === "--id") server.id = value
            else if (option === "--command") server.command = value
            else if (option === "--arg") server.args.push(value)
            else if (option === "--cwd") server.working_directory = value
            else if (option === "--env") {
              const separator = value.indexOf("=")
              const key = separator > 0 ? value.slice(0, separator) : ""
              if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(key)) { invalid = "Environment entries must use KEY=VALUE with a valid key."; break }
              if (Object.hasOwn(server.env, key)) { invalid = `Environment key ${key} was supplied more than once.`; break }
              server.env[key] = value.slice(separator + 1)
            } else { invalid = `Unknown MCP option: ${option}.`; break }
          }
          if (!server.id || !server.command) invalid ||= 'Usage: /mcp add --id ID --command PATH [--arg ARG] [--env KEY=VALUE] [--cwd DIR]'
          if (invalid) addEntry("system", invalid)
          else transport.send("mcp_add" as any, { server })
        } else addEntry("system", 'Usage: /mcp · /mcp add --id ID --command PATH [--arg ARG] [--env KEY=VALUE] [--cwd DIR] · /mcp remove ID')
        break
      }
      case "tools": transport.send("tools" as any); break
      case "plugin": {
        const operation = (args[0] ?? "").toLowerCase()
        const target = args.slice(1).join(" ")
        if (operation === "enable" && target) transport.send("plugins_enable" as any, { manifest_path: target })
        else if (operation === "disable" && target) transport.send("plugins_disable" as any, { id: target })
        else addEntry("system", 'Usage: /plugin enable "MANIFEST_PATH" · /plugin disable ID · changes apply to new sessions.')
        break
      }
      case "update": {
        if (busy || turnActive.current || waiting.current || question || activeTaskId || maintenance || reloadCommandId.current) {
          addEntry("system", "Update is available only when the foreground turn and task follow are idle.")
          break
        }
        let sourcePath = ""
        if (args[0] === "--source") sourcePath = args.slice(1).join(" ")
        else if (args.length) sourcePath = args.join(" ")
        if (args[0] === "--source" && !sourcePath) {
          addEntry("system", "Usage: /update [--source PATH] · without a path, pk fetches the latest GitHub source release.")
          break
        }
        const commandId = transport.send("update", sourcePath ? { source_path: sourcePath } : undefined)
        if (!commandId) addEntry("system", "Could not start update because the agent connection is unavailable.")
        else setMaintenance({ id: commandId, kind: "update", startedAt: Date.now(), progress: "Starting build" })
        break
      }
      case "rollback": {
        if (busy || turnActive.current || waiting.current || question || activeTaskId || maintenance || reloadCommandId.current) {
          addEntry("system", "Rollback is available only when the foreground turn and task follow are idle.")
          break
        }
        const commandId = transport.send("rollback")
        if (!commandId) addEntry("system", "Could not start rollback because the agent connection is unavailable.")
        else setMaintenance({ id: commandId, kind: "rollback", startedAt: Date.now(), progress: "Restoring previous release" })
        break
      }
      case "reload": {
        if (busy || turnActive.current || waiting.current || question || activeTaskId || maintenance || reloadCommandId.current) {
          addEntry("system", "Reload is available only when the foreground turn, question, task follow, and update are idle.")
          break
        }
        if (!sessionId) {
          addEntry("system", "Start a conversation before reloading so pk can resume its session.")
          break
        }
        reloadCommandId.current = transport.send("reload") ?? ""
        if (!reloadCommandId.current) addEntry("system", "Could not prepare reload because the agent connection is unavailable.")
        else { setReloadPending(true); addEntry("system", "Saving this session before reload…") }
        break
      }
      case "new":
        if (busy) { addEntry("system", "Wait for the active turn to finish before starting a new session."); break }
        transport.send("new")
        setEntries([])
        setSessionId("")
        break
      case "attach":
        if (!args[0]) { addEntry("system", "Usage: /attach SESSION_ID"); break }
        transport.send("attach", { session_id: args[0] })
        setEntries([{ id: entryId.current++, role: "system", text: `Attaching to session ${args[0]}…` }])
        break
      case "detach":
        if (busy) addEntry("system", "A turn is still running. Use /cancel, then detach when it finishes.")
        else if (sessionId) transport.send("detach", { session_id: sessionId })
        else addEntry("system", "There is no active session to detach.")
        break
      case "cancel":
        if (maintenance?.kind === "update") {
          transport.send("update_cancel")
          setMaintenance({ ...maintenance, progress: "Stopping update" })
          addEntry("system", "Stopping the update before activation…")
        } else if (maintenance?.kind === "rollback") addEntry("system", "Rollback cannot be interrupted safely once activation has started; wait for it to finish.")
        else if (reloadCommandId.current) addEntry("system", "Reload is saving the session; wait for the supervisor handoff to finish.")
        else if (reloadCommandId.current) addEntry("system", "Reload is saving the session; wait for the supervisor handoff to finish.")
        else if (busy) transport.send("cancel")
        else addEntry("system", "No turn is running.")
        break
      case "status": transport.send("status"); addEntry("system", `Session ${sessionId || "not started"} · ${model} · ${effort} reasoning`); break
      case "login": transport.send("login"); break
      case "task": {
        const [subcommand, ...rest] = args
        const taskId = rest[0]
        let taskWorkspace: string | undefined
        let promptParts = rest
        if (subcommand === "new" && rest[0] === "--workspace") {
          taskWorkspace = rest[1]
          promptParts = rest.slice(2)
        }
        const prompt = promptParts.join(" ")
        if (subcommand === "new" && prompt) transport.send("task_create", { prompt, ...(taskWorkspace ? { workspace: taskWorkspace } : {}), model, effort })
        else if (subcommand === "new") addEntry("system", "Usage: /task new [--workspace PATH] PROMPT")
        else if (subcommand === "list" || !subcommand) transport.send("task_list")
        else if (subcommand === "attach" && taskId) transport.send("task_attach", { task_id: taskId })
        else if (subcommand === "resume" && taskId) transport.send("task_resume", { task_id: taskId })
        else if (subcommand === "cancel" && taskId) transport.send("task_cancel", { task_id: taskId })
        else addEntry("system", "Usage: /task new [--workspace PATH] PROMPT · /task list · /task attach ID · /task resume ID · /task cancel ID")
        break
      }
      case "file": {
        queueFile(args.join(" "))
        break
      }
      case "files": {
        const [operation, ...rest] = args
        if (operation === "clear") {
          updateFileQueue([])
          addEntry("system", queuedFiles.length ? `Cleared ${queuedFiles.length} queued file${queuedFiles.length === 1 ? "" : "s"}.` : "The file queue is already empty.")
        } else if (operation === "remove") {
          const target = rest.join(" ")
          const index = /^\d+$/.test(target) ? Number(target) - 1 : queuedFiles.indexOf(target)
          if (index < 0 || index >= queuedFiles.length) addEntry("system", "Usage: /files remove N · N is the 1-based file number shown below.")
          else {
            const removed = queuedFiles[index]!
            updateFileQueue((current) => current.filter((_, currentIndex) => currentIndex !== index))
            addEntry("system", `Removed file · ${removed}`)
          }
        } else if (operation) {
          addEntry("system", "Usage: /files · /files remove N · /files clear")
        } else if (queuedFiles.length) {
          addEntry("system", `Queued files:\n${queuedFiles.map((path, index) => `${index + 1}. ${path}`).join("\n")}\nUse /files remove N or /files clear.`)
        } else addEntry("system", "No files queued. Use /file PATH to attach an explicit file to your next prompt.")
        break
      }
      case "paste": requestClipboardPaste(); break
      case "help": addEntry("system", "Enter sends · Shift-Enter or Ctrl-J adds a line · Esc stops/closes panels · Ctrl-P opens commands · Ctrl/Cmd-V or /paste imports clipboard · selecting transcript text copies it when OSC 52 is supported · Ctrl-Y copies selected text · /file PATH · /files · /task new [--workspace PATH] PROMPT · /tasks · /skills · /plugins · /plugin enable MANIFEST · /plugin disable ID · /mcp · /mcp add --id ID --command PATH · /mcp remove ID · /tools · /update [--source PATH] · /rollback · /reload · /new · /attach ID · /detach · /status · /login · /help · /exit"); break
      case "exit": void transport.close().finally(() => renderer.destroy()); break
    }
  }

  const commitSelector = (index: number) => {
    if (selector === "model") {
      const selected = models[index]
      if (selected) {
        setModelPreference(selected.id, effort)
      }
    } else if (selector === "effort") {
      const selected = efforts[index]
      if (selected) {
        setModelPreference(model, selected)
      }
    }
    setSelector(null)
    textarea.current?.focus()
  }

  const activateSelectorOption = (index: number) => {
    if (selector === "tasks") {
      const task: any = tasks[index]
      if (task?.task_id) transport.send("task_attach", { task_id: task.task_id })
      else if (task?.session_id) runSlashCommand(`/attach ${task.session_id}`)
      setSelector(null)
      textarea.current?.focus()
    } else if (selector === "skills") {
      const skill = skills[index]
      if (skill) {
        const current = textarea.current?.plainText ?? draft
        const instruction = `Use the ${skill.name} skill for this task.`
        const inserted = current ? `${current}${current.endsWith("\n") ? "" : "\n"}${instruction}` : instruction
        textarea.current?.setText(inserted)
        textarea.current?.focus()
        setDraft(inserted)
        addEntry("system", `Inserted an instruction for the ${skill.name} skill. Review it in the composer, then send when ready.`)
      }
      setSelector(null)
      textarea.current?.focus()
    } else if (selector === "plugins") {
      const plugin = plugins[index]
      if (plugin) {
        if (plugin.enabled) transport.send("plugins_disable" as any, { id: plugin.id })
        else if (plugin.error) addEntry("system", `Cannot enable ${plugin.id}: ${plugin.error}`)
        else if (plugin.manifest_path) transport.send("plugins_enable" as any, { manifest_path: plugin.manifest_path })
        else addEntry("system", `Cannot enable ${plugin.id}: its manifest path is unavailable.`)
      }
    } else if (selector === "mcp") {
      const server = mcpServers[index]
      if (server) addEntry("system", `MCP ${server.id} · ${server.command} · ${server.arguments_count} args · environment keys: ${server.environment_keys.join(", ") || "none"} · ${server.working_directory || "default working directory"}. Use /mcp remove ${server.id} to remove it; changes apply to new sessions.`)
    } else if (selector === "tools") {
      const tool = modelTools[index]
      if (tool) addEntry("system", `${tool.name}${tool.source ? ` · ${tool.source}` : ""}${tool.description ? ` · ${tool.description}` : ""}`)
    } else commitSelector(index)
  }

  const submitQuestionAnswer = (answer: string) => {
    if (!question || question.answering || !answer) return
    transport.send("answer_question" as any, { id: question.id, answer })
    setQuestion({ ...question, answering: true, submittedAnswer: answer })
    clearComposer(true)
  }

  const toggleToolGroup = (key: string) => {
    setExpandedToolGroups((current) => {
      const next = new Set(current)
      if (next.has(key)) next.delete(key)
      else next.add(key)
      return next
    })
  }

  const toggleLastToolGroup = () => {
    const groups = groupTranscript(entries).filter((item): item is { kind: "tools"; entries: Entry[] } => item.kind === "tools")
    const last = groups[groups.length - 1]
    if (last) toggleToolGroup(toolGroupKey(last.entries))
  }

  const leftMouseDown = (event: { button: number; preventDefault: () => void; stopPropagation: () => void }, action: () => void) => {
    if (event.button !== 0) return
    event.preventDefault()
    event.stopPropagation()
    action()
  }

  useKeyboard((key) => {
    const isEscape = key.name === "escape" || key.name === "esc"
    const commandModifier = key.super === true || key.meta === true
    if ((commandModifier || key.ctrl) && key.name.toLowerCase() === "v") {
      key.preventDefault()
      requestClipboardPaste()
      return
    }
    if ((commandModifier && key.name.toLowerCase() === "c") || (key.ctrl && key.name.toLowerCase() === "y")) {
      const selection = renderer.getSelection()?.getSelectedText() ?? ""
      if (selection) {
        key.preventDefault()
        copyToClipboard(selection)
      } else if (key.ctrl && key.name.toLowerCase() === "y") {
        key.preventDefault()
        addEntry("system", selection ? "This terminal does not allow OSC 52 clipboard copy." : "Select transcript text first, then press Ctrl+Y to copy it.")
      }
      return
    }
    if (!question && !selector && key.ctrl && key.name === "o") {
      toggleLastToolGroup()
      return
    }
    if (question) {
      if (question.answering) return
      if (isEscape) {
        transport.send("cancel_question" as any, { id: question.id })
        setQuestion(null)
        addEntry("system", "Question canceled; stopping this turn…")
        return
      }
      if (key.name === "up" || key.name === "down") {
        if (question.choices.length && !(textarea.current?.plainText ?? draft).trim()) {
          key.preventDefault()
          setQuestionIndex((index) => key.name === "up" ? (index - 1 + question.choices.length) % question.choices.length : (index + 1) % question.choices.length)
          return
        }
      }
      if (key.name === "return" && !(textarea.current?.plainText ?? draft).trim() && question.choices.length) {
        key.preventDefault()
        const answer = question.choices[questionIndex]
        if (answer) submitQuestionAnswer(answer)
        return
      }
    }
    if (selector) {
      const count = selector === "model" ? models.length : selector === "effort" ? efforts.length : selector === "tasks" ? tasks.length : selector === "skills" ? skills.length : selector === "plugins" ? plugins.length : selector === "mcp" ? mcpServers.length : modelTools.length
      if (isEscape) { setSelector(null); textarea.current?.focus(); return }
      if (key.name === "up") { setSelectionIndex((index) => (index - 1 + count) % count); return }
      if (key.name === "down") { setSelectionIndex((index) => (index + 1) % count); return }
      if (key.name === "return") {
        activateSelectorOption(selectionIndex)
        key.preventDefault()
        return
      }
    }
    if (key.ctrl && key.name === "p") {
      textarea.current!.setText("/")
      setDraft("/")
      textarea.current?.focus()
      return
    }
    if (draft.startsWith("/")) {
      const typedCommand = draft.trim().split(/\s/)[0] || "/"
      const filtered = slashCommands.filter((item) => item.name.startsWith(typedCommand))
      if (key.name === "return" && filtered.length && !/\s/.test(draft.trim())) {
        key.preventDefault()
        const selected = filtered[slashIndex % filtered.length]
        if (selected && selected.action !== "task" && selected.action !== "attach") {
          runSlashCommand(selected.name)
          clearComposer(true)
        }
        else if (selected) { textarea.current!.setText(`${selected.name} `); setDraft(`${selected.name} `) }
        return
      }
      if (filtered.length && (key.name === "up" || key.name === "down")) {
        key.preventDefault()
        setSlashIndex((index) => key.name === "up" ? (index - 1 + filtered.length) % filtered.length : (index + 1) % filtered.length)
        return
      }
      if (filtered.length && key.name === "tab") {
        key.preventDefault()
        const selected = filtered[slashIndex % filtered.length]
        if (selected) { textarea.current!.setText(`${selected.name} `); setDraft(`${selected.name} `) }
        return
      }
    }
    if (isEscape && selector) {
      setSelector(null)
      textarea.current?.focus()
      return
    }
    if (isEscape && draft.startsWith("/")) {
      key.preventDefault()
      textarea.current!.clear()
      setDraft("")
      return
    }
    if (isEscape && maintenance?.kind === "update") {
      transport.send("update_cancel" as any)
      setMaintenance({ ...maintenance, progress: "Stopping update" })
      addEntry("system", "Stopping the update before activation…")
      return
    }
    if (isEscape && maintenance?.kind === "rollback") {
      addEntry("system", "Rollback is finishing safely; wait for it to complete.")
      return
    }
    if (isEscape && reloadPending) {
      addEntry("system", "Session handoff is in progress; pk will restart when it is safe.")
      return
    }
    if (isEscape && busyRef.current) {
      if (activeTaskId) transport.send("task_cancel", { task_id: activeTaskId })
      else transport.send("cancel")
      addEntry("system", "Stopping the current turn…")
      return
    }
    if (key.ctrl && key.name === "d") {
      if (maintenance || reloadPending) { addEntry("system", "Wait for the release operation to finish before closing pk."); return }
      if (activeTaskId && !busy) transport.send("detach", { task_id: activeTaskId })
      else if (sessionId && !busy) transport.send("detach", { session_id: sessionId })
      else if (busyRef.current) addEntry("system", "A turn is still running. Use Esc to stop it before detaching.")
      else void transport.close().finally(() => renderer.destroy())
      return
    }
    if (key.ctrl && key.name === "c" && !busy && !maintenance && !reloadPending) {
      void transport.close().finally(() => renderer.destroy())
    }
  })

  const selectedOptions = selector === "model" ? models.map((item) => ({ label: item.label, value: item.id, description: item.id, state: item.id === model ? "current" : "" }))
    : selector === "effort" ? efforts.map((item) => ({ label: `${item[0]!.toUpperCase()}${item.slice(1)} reasoning`, value: item, description: "", state: item === effort ? "current" : "" }))
      : selector === "tasks" ? tasks.map((task: any) => ({ label: task.title || task.prompt || task.session_id || task.task_id, value: task.session_id || task.task_id, description: `${task.kind ?? "task"} · ${task.status ?? task.updated_at ?? "saved"}`, state: "" }))
        : selector === "skills" ? skills.map((skill) => ({ label: skill.name, value: skill.name, description: `${skill.description || "No description"} · ${skill.path}`, state: skill.saved ? "saved" : skill.bundled ? "bundled" : "available" }))
        : selector === "plugins" ? plugins.map((plugin) => ({ label: plugin.id, value: plugin.id, description: plugin.error ? `Error · ${plugin.error}` : `${plugin.manifest_path ?? "manifest unavailable"}${plugin.tools?.length ? ` · ${plugin.tools.length} tools` : ""}${plugin.commands?.length ? ` · ${plugin.commands.length} commands` : ""}`, state: plugin.enabled ? "enabled" : "disabled" }))
          : selector === "mcp" ? mcpServers.map((server) => ({ label: server.id, value: server.id, description: `${server.command} · ${server.arguments_count} args · env ${server.environment_keys.join(", ") || "none"}${server.working_directory ? ` · cwd ${server.working_directory}` : ""}`, state: "configured" }))
            : modelTools.map((tool) => ({ label: tool.name, value: tool.name, description: tool.description, state: tool.source ?? "" }))
  const selectorPageSize = selector === "skills"
    ? Math.max(3, Math.min(7, Math.floor((renderer.height - 12) / 3)))
    : selector === "plugins" || selector === "mcp" || selector === "tools" ? Math.max(4, Math.min(10, Math.floor((renderer.height - 12) / 2))) : 8
  const selectorWindowStart = Math.max(0, Math.min(selectionIndex - Math.floor(selectorPageSize / 2), selectedOptions.length - selectorPageSize))
  const filteredCommands = slashCommands.filter((item) => item.name.startsWith(draft.trim().split(/\s/)[0] || "/"))
  const slashWindowStart = Math.max(0, Math.min(slashIndex - 5, filteredCommands.length - 6))
  const cwd = message || workspace
  const visibleFileCount = Math.max(1, Math.floor((renderer.width - 26) / 20))
  const activityLabel = question
    ? "Waiting for your answer"
    : maintenance
      ? `${maintenance.kind === "update" ? "Building update" : "Rolling back"} · ${maintenance.progress}`
      : reloadPending
        ? "Saving session for reload"
    : busy
      ? streamProgress?.label ?? (tools.length ? `Running ${tools.length} tool${tools.length === 1 ? "" : "s"}` : "Waiting for model")
      : connected ? "Ready" : everConnected ? "Connection closed" : "Starting"
  const phaseTime = phaseStartedAt === null || (!busy && !maintenance) ? "" : ` · ${shortTime(clock - phaseStartedAt)}`
  const activityTime = activityStartedAt !== null ? ` · ${shortTime(clock - activityStartedAt)} total` : maintenance ? ` · ${shortTime(clock - maintenance.startedAt)} total` : ""
  const spinner = ["◒", "◐", "◓", "◑"][Math.floor(clock / 180) % 4]!
  const activityActive = busy || Boolean(maintenance) || reloadPending
  const cacheLabel = usage?.available && usage.cachedInput !== undefined ? `cache ${usage.cachedInput.toLocaleString()}` : "cache —"

  return (
    <box style={{ flexDirection: "column", width: "100%", height: "100%", minHeight: 0, flexGrow: 1, backgroundColor: palette.bg, paddingLeft: 2, paddingRight: 2 }}>
      <box style={{ flexDirection: "row", height: 1 }}>
        <text fg={palette.text} content="pk  /  terminal agent" />
      </box>
      {!sessionId && entries.length <= 1 && <box style={{ flexDirection: "column", marginTop: 1, marginBottom: 1, flexShrink: 0 }}>
        <ascii-font text="PK" font="block" color={palette.accent} />
      </box>}
      <box style={{ flexDirection: "row", height: 1, flexShrink: 0 }}>
        <text fg={palette.muted} content={`${shortPath(cwd, 42)}  ·  ${sessionId ? `session ${sessionId.slice(0, 8)}` : "new session"}`} />
      </box>
      <scrollbox id="transcript" stickyScroll stickyStart="bottom" style={{ flexGrow: 1, minHeight: 0, height: 0, paddingTop: 0, paddingBottom: 0 }}>
        {groupTranscript(entries).map((item) => item.kind === "entry"
          ? <TranscriptEntry key={`entry-${item.entry.id}`} entry={item.entry} clock={clock} />
          : <ToolTranscriptGroup key={`tools-${toolGroupKey(item.entries)}`} entries={item.entries} clock={clock} expanded={expandedToolGroups.has(toolGroupKey(item.entries))} onToggle={() => toggleToolGroup(toolGroupKey(item.entries))} />)}
      </scrollbox>
      <box style={{ border: ["top"], borderColor: palette.line, paddingTop: 0, flexShrink: 0 }}>
        {queuedFiles.length > 0 && <box style={{ flexDirection: "row", gap: 1, height: 1, flexShrink: 0, paddingLeft: 1 }}>
          <text fg={palette.muted} content={`Files ${queuedFiles.length}/8`} />
          {queuedFiles.slice(0, visibleFileCount).map((path, index) => {
            const name = path.split(/[\\/]/).pop() || path
            const label = name.length > 14 ? `${name.slice(0, 13)}…` : name
            return <box key={`${path}-${index}`} id={`file-chip-${index}`} onMouseDown={(event) => leftMouseDown(event, () => {
              updateFileQueue((current) => current.filter((_, currentIndex) => currentIndex !== index))
            })} style={{ flexDirection: "row", gap: 1, backgroundColor: palette.raised, paddingLeft: 1, paddingRight: 1, height: 1 }}>
              <text fg={palette.text} content={label} />
              <text fg={palette.accent} content="×" />
            </box>
          })}
          {queuedFiles.length > visibleFileCount && <text fg={palette.dim} content={`+${queuedFiles.length - visibleFileCount} · /files`} />}
        </box>}
        <box style={{ flexDirection: "row", height: 1 }}>
          <text fg={copyNotice ? palette.accent : palette.dim} content={`${copyNotice ? `${copyNotice}  ·  ` : ""}${activityActive ? `${spinner} ` : ""}${activityLabel}${phaseTime}${activityTime} · ${cacheLabel}`} />
        </box>
        <box style={{ border: true, borderColor: palette.line, backgroundColor: palette.panel, paddingLeft: 1, paddingRight: 1, minHeight: 3, maxHeight: 5, flexShrink: 0 }}>
          <textarea id="composer" ref={textarea} focused={!selector} placeholder={question ? "Type an answer, or choose an option above…" : "Ask pk to inspect, explain, or change this workspace…"} onContentChange={() => setDraft(textarea.current?.plainText ?? "")} onSubmit={sendPrompt} keyBindings={[{ name: "return", action: "submit" }, { name: "return", shift: true, action: "newline" }, { name: "kpenter", action: "submit" }, { name: "kpenter", shift: true, action: "newline" }, { name: "j", ctrl: true, action: "newline" }]} />
        </box>
        {draft.startsWith("/") && filteredCommands.length > 0 && <box style={{ border: true, borderColor: palette.line, backgroundColor: palette.raised, paddingLeft: 1, paddingRight: 1, marginTop: 1, flexDirection: "column" }}>
          {filteredCommands.slice(slashWindowStart, slashWindowStart + 6).map((item, localIndex) => <box key={item.name} onMouseOver={() => setSlashIndex(slashWindowStart + localIndex)} onMouseDown={(event) => leftMouseDown(event, () => {
            setSlashIndex(slashWindowStart + localIndex)
            if (item.action === "task" || item.action === "attach") {
              textarea.current!.setText(`${item.name} `)
              setDraft(`${item.name} `)
              textarea.current?.focus()
            } else {
              runSlashCommand(item.name)
              clearComposer(true)
            }
          })} style={{ flexDirection: "row", gap: 2, backgroundColor: slashWindowStart + localIndex === slashIndex % filteredCommands.length ? palette.panel : palette.raised, height: 1 }}>
            <text fg={palette.accent} content={item.name} />
            <text fg={palette.muted} content={item.description} />
          </box>)}
        </box>}
        <box style={{ flexDirection: "row", justifyContent: "space-between", height: 1 }}>
      <text fg={palette.dim} content={`${question ? "Enter answer" : activeTaskId || busy && steeringEnabled ? "Enter steer" : busy ? "Esc stop" : "Enter send"}  ·  ^J newline  ·  ^P menu  ·  ⌘V paste files  ·  ${entries.some((entry) => entry.role === "tool") ? "^O tool details  ·  " : ""}^D detach`} />
          <text fg={palette.muted} content={`${model}  ·  ${effort}`} />
        </box>
      </box>
      {selector && <box style={{ position: "absolute", left: selector === "skills" || selector === "plugins" || selector === "mcp" || selector === "tools" ? "8%" : "25%", right: selector === "skills" || selector === "plugins" || selector === "mcp" || selector === "tools" ? "8%" : "25%", top: selector === "skills" || selector === "plugins" || selector === "mcp" || selector === "tools" ? "10%" : "25%", border: true, borderColor: palette.line, backgroundColor: palette.raised, padding: 2, flexDirection: "column" }}>
        <text fg={palette.text} content={selector === "model" ? "Select model" : selector === "effort" ? "Reasoning effort" : selector === "tasks" ? "Saved sessions" : selector === "skills" ? "Available skills" : selector === "plugins" ? "Installed plugins" : selector === "mcp" ? "MCP servers · safe configuration summary" : "Model-visible tools"} />
        {selector === "mcp" && <text fg={palette.dim} content={`${mcpTools.length} MCP tool${mcpTools.length === 1 ? "" : "s"} in ${mcpSavedTools ? "saved session snapshot" : "current catalog"} · no servers are started by listing`} />}
        {selector === "tools" && <text fg={palette.dim} content={modelToolsSaved ? "Saved snapshot for this session" : "Current available tool registry"} />}
        <box style={{ height: 1 }} />
        {selectedOptions.length === 0 && <text fg={palette.muted} content={selector === "skills" ? "No skills available" : selector === "plugins" ? "No plugins installed" : selector === "mcp" ? "No MCP servers configured" : selector === "tools" ? "No model tools available" : "Nothing to show"} />}
        {selectedOptions.slice(selectorWindowStart, selectorWindowStart + selectorPageSize).map((option, localIndex) => {
          const index = selectorWindowStart + localIndex
          return <box key={option.value} onMouseOver={() => setSelectionIndex(index)} onMouseDown={(event) => leftMouseDown(event, () => activateSelectorOption(index))} style={{ flexDirection: "column", backgroundColor: index === selectionIndex ? palette.panel : palette.raised, paddingLeft: 1, paddingRight: 1 }}>
            <box style={{ flexDirection: "row", gap: 1, height: 1 }}>
              <text fg={index === selectionIndex ? palette.accent : palette.muted} content={index === selectionIndex ? "›" : " "} />
              <text fg={index === selectionIndex ? palette.text : palette.muted} content={option.label} />
              <text fg={(selector === "plugins" && option.state === "enabled") || (selector === "mcp" && option.state === "configured") ? palette.accent : palette.dim} content={option.state} />
            </box>
            {option.description && <text fg={palette.dim} content={option.description} />}
          </box>
        })}
        <box style={{ height: 1 }} />
        <text fg={palette.dim} content={selector === "skills" ? "↑↓ move · Enter insert instruction · Esc close" : selector === "plugins" ? "↑↓ move · Enter toggle · /plugin enable|disable · new session required" : selector === "mcp" ? "↑↓ move · Enter details · /mcp add|remove · new session required · Esc close" : selector === "tools" ? "↑↓ move · Enter details · registry snapshot · Esc close" : "↑↓ move  ·  Enter choose  ·  Esc close"} />
      </box>}
      {question && <box style={{ position: "absolute", left: "15%", right: "15%", top: "20%", border: true, borderColor: palette.accent, backgroundColor: palette.raised, padding: 2, flexDirection: "column" }}>
        <text fg={palette.accent} content={question.kind === "confirmation" ? "Confirmation needed" : "A question for you"} />
        <text fg={palette.text} content={question.text} />
        {question.choices.map((choice, index) => <box key={`${question.id}-${index}`} onMouseOver={() => !question.answering && setQuestionIndex(index)} onMouseDown={(event) => leftMouseDown(event, () => submitQuestionAnswer(choice))} style={{ flexDirection: "row", gap: 1, backgroundColor: index === questionIndex ? palette.panel : palette.raised, paddingLeft: 1, height: 1 }}>
          <text fg={question.answering ? palette.dim : index === questionIndex ? palette.accent : palette.muted} content={index === questionIndex && !question.answering ? "›" : " "} />
          <text fg={question.answering ? palette.dim : index === questionIndex ? palette.text : palette.muted} content={choice} />
        </box>)}
        <text fg={palette.dim} content={question.answering ? "Sending answer…" : question.choices.length ? "↑↓ choose · Enter answer · type a custom answer · Esc cancel" : "Type an answer · Enter submit · Esc cancel"} />
      </box>}
    </box>
  )
}

const ToolTranscriptGroup = memo(function ToolTranscriptGroupView({ entries, clock, expanded, onToggle }: { entries: Entry[]; clock: number; expanded: boolean; onToggle: () => void }) {
  const renderer = useRenderer()
  const first = entries[0]!
  const failed = entries.some((entry) => entry.toolState === "failed")
  const interrupted = entries.some((entry) => entry.toolState === "interrupted")
  const running = entries.some((entry) => !["completed", "complete", "failed", "canceled", "cancelled", "succeeded", "interrupted"].includes((entry.toolState ?? "").toLowerCase()))
  const elapsed = entries.reduce((total, entry) => total + (entry.elapsedMs ?? (entry.startedAt ? Math.max(0, clock - entry.startedAt) : 0)), 0)
  const summary = entries.length > 1
    ? `${first.toolName ?? "tool"} · Ran ${entries.length} ${first.toolName === "Bash" ? "commands" : "calls"}`
    : `${first.toolName ?? "tool"} · ${first.commandPreview || "operation"}`
  const color = failed ? palette.red : interrupted ? palette.amber : running ? palette.accent : palette.green
  const available = Math.max(24, Math.min(112, renderer.width - 24))
  const compactSummary = summary.length > available ? `${summary.slice(0, available - 1)}…` : summary
  return <box focusable onMouseDown={(event) => {
    if (event.button !== 0) return
    event.preventDefault()
    event.stopPropagation()
    onToggle()
  }} onKeyDown={(key) => {
    if (key.name === "return" || key.name === "space") {
      key.preventDefault()
      onToggle()
    }
  }} style={{ flexDirection: "column", width: "100%", marginLeft: 2, marginBottom: 1, paddingLeft: 1, border: ["left"], borderColor: failed ? palette.red : palette.accent }}>
    <box style={{ flexDirection: "row", gap: 1, height: 1 }}>
      <text fg={color} content={running ? "◌" : failed ? "!" : interrupted ? "↯" : "✓"} />
      <text fg={palette.text} content={compactSummary} />
      <text fg={palette.dim} content={shortTime(elapsed)} />
      <text fg={palette.dim} content={expanded ? "▾" : "›"} />
    </box>
    {expanded && entries.map((entry) => <box key={entry.callId} style={{ flexDirection: "column", paddingLeft: 2, paddingBottom: 1 }}>
      {entry.commandPreview && <text fg={palette.muted} content={`$ ${entry.commandPreview}`} />}
      {entry.detail && <text fg={entry.toolState === "failed" ? palette.red : palette.dim} content={entry.detail} />}
      {entry.text && entry.text !== entry.detail && <text fg={palette.red} content={entry.text} />}
    </box>)}
  </box>
}, (previous, next) => {
  if (previous.expanded !== next.expanded || previous.entries.length !== next.entries.length) return false
  for (let index = 0; index < previous.entries.length; index++) if (previous.entries[index] !== next.entries[index]) return false
  const elapsedIsLive = previous.entries.some((entry) => entry.startedAt !== undefined && entry.elapsedMs === undefined && !["completed", "complete", "failed", "canceled", "cancelled", "succeeded", "interrupted"].includes((entry.toolState ?? "").toLowerCase()))
  return !elapsedIsLive || previous.clock === next.clock
})

const TranscriptEntry = memo(function TranscriptEntryView({ entry, clock }: { entry: Entry; clock: number }) {
  if (entry.role === "system") return <box style={{ paddingLeft: 2, paddingBottom: 1 }}><text fg={palette.dim} content={entry.text} /></box>
  if (entry.role === "tool") return <box style={{ flexDirection: "column", marginLeft: 2, marginBottom: 1, paddingLeft: 1, border: ["left"], borderColor: entry.toolState === "failed" ? palette.red : palette.accent }}>
    <box style={{ flexDirection: "row", gap: 1, height: 1 }}>
      <text fg={entry.toolState === "failed" ? palette.red : palette.accent} content={entry.toolState === "running" || entry.toolState === "working" ? "◌" : "›"} />
      <text fg={palette.text} content={`${entry.toolName ?? "tool"} · ${entry.toolState ?? "working"}`} />
      <text fg={palette.dim} content={entry.elapsedMs !== undefined ? shortTime(entry.elapsedMs) : entry.startedAt ? shortTime(clock - entry.startedAt) : ""} />
    </box>
    {entry.detail ? <text fg={palette.muted} content={entry.detail} /> : entry.text ? <text fg={palette.red} content={entry.text} /> : null}
  </box>
  const isUser = entry.role === "user"
  const markdown = hasMarkdownSyntax(entry.text)
  const delivery = entry.delivery === "queued" ? "queued · waiting for a boundary" : entry.delivery === "accepted" ? "accepted by active turn" : entry.delivery === "rejected" ? "not accepted" : ""
  return <box style={{ flexDirection: "column", width: "100%", paddingLeft: isUser ? 0 : 2, paddingBottom: 1 }}>
    <text fg={isUser ? palette.blue : palette.accent} content={isUser ? `you${delivery ? ` · ${delivery}` : ""}` : "pk"} />
    {isUser || entry.provisional || !markdown
      ? <text fg={palette.text} content={entry.text} />
      : <markdown content={entry.text} syntaxStyle={markdownStyle} fg={palette.text} style={{ width: "100%", flexGrow: 1, minHeight: 1, flexShrink: 0 }} />}
    {entry.deliveryMessage && <text fg={entry.delivery === "rejected" ? palette.red : palette.dim} content={entry.deliveryMessage} />}
  </box>
}, (previous, next) => previous.entry === next.entry && (previous.entry.role !== "tool" || previous.clock === next.clock))
