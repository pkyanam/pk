import { useKeyboard, usePaste, useRenderer, useSelectionHandler } from "@opentui/react"
import { SyntaxStyle, type TextareaRenderable } from "@opentui/core"
import { memo, useEffect, useRef, useState } from "react"
import type { PkTransport } from "./transport"
import type { ServerEvent } from "./protocol"
import { SessionManager, type ManagedSession } from "./session-manager"
import { MCPManager } from "./mcp-manager"

type Role = "user" | "assistant" | "system" | "tool"
type HistoryAttachment = { name: string; kind: string; contentType?: string; truncated?: boolean; pagesExtracted?: number; pagesTotal?: number }
type Entry = { id: number; role: Role; text: string; speaker?: string; callId?: string; toolName?: string; toolState?: string; historySummary?: string; historical?: boolean; historyAttachments?: HistoryAttachment[]; startedAt?: number; elapsedMs?: number; commandPreview?: string; detail?: string; progressText?: string; provisional?: boolean; delivery?: "queued" | "accepted" | "rejected"; deliveryMessage?: string }
const MAX_TRANSCRIPT_ENTRIES = 300
const OMITTED_TRANSCRIPT_ENTRY: Entry = { id: -1, role: "system", text: "Earlier activity omitted from this view · transcript is bounded for responsiveness." }
function appendTranscriptEntries(current: Entry[], incoming: Entry | Entry[]): Entry[] {
  const additions = Array.isArray(incoming) ? incoming : [incoming]
  const hadOmission = current.some((entry) => entry.id === OMITTED_TRANSCRIPT_ENTRY.id)
  const combined = [...current.filter((entry) => entry.id !== OMITTED_TRANSCRIPT_ENTRY.id), ...additions]
  if (!hadOmission && combined.length <= MAX_TRANSCRIPT_ENTRIES) return combined
  return [OMITTED_TRANSCRIPT_ENTRY, ...combined.slice(-(MAX_TRANSCRIPT_ENTRIES - 1))]
}
type ToolActivity = { id: string; name: string; state: string; startedAt: number; detail?: string }
type StreamDraft = { outerId: string; requestId: string; attempt: number; itemId: string; entryId: number; text: string }
type ActiveStreamAttempt = { outerId: string; requestId: string; attempt: number }
type StreamProgress = { outerId: string; requestId: string; label: string }
type Model = { id: string; label: string }
type PendingQuestion = { id: string; text: string; choices: string[]; kind: "question" | "confirmation"; answering?: boolean; submittedAnswer?: string }
type SlashCommand = { name: string; description: string; action: "model" | "effort" | "tasks" | "sessions" | "skills" | "plugins" | "plugin" | "mcp" | "tools" | "provider" | "image" | "plugin_commands" | "history" | "usage" | "update" | "rollback" | "reload" | "new" | "attach" | "detach" | "cancel" | "status" | "login" | "task" | "file" | "files" | "paste" | "help" | "exit" }
type Maintenance = { id: string; kind: "update" | "rollback"; startedAt: number; progress: string }
type SessionUsage = { sessionId: string; responseCount: number; inputTokens?: number; outputTokens?: number; cachedInputTokens?: number; uncachedInputTokens?: number; coverage: { input: number; output: number; cachedInput: number; uncachedInput: number } }
type PluginCommandRun = { id: string; name: string; startedAt: number; cancelRequested?: boolean }
type SkillOption = { name: string; description: string; path: string; bundled?: boolean; saved?: boolean; source?: string; url?: string; installs?: number; id?: string }
type SkillSearchOption = { name: string; id: string; source: string; installs: number; url: string }
type SkillCandidate = { name: string; description: string; source: string; path: string; url: string }
type SkillInstalled = { name: string; description?: string; source?: string; path?: string }
type PluginOption = { id: string; version?: string; manifest_path?: string; enabled: boolean; tools?: string[]; commands?: string[]; error?: string }
type PluginCandidate = { id: string; version?: string; manifest_path: string; tools: string[]; commands: string[]; build_required: boolean }
type ExtensionCommand = { name: string; extension_id: string; command_name: string; description: string; enabled?: boolean; error?: string }
type MCPServerOption = { id: string; command: string; arguments_count: number; environment_keys: string[]; working_directory?: string; transport?: string; url?: string; auth_mode?: string; auth_status?: string; credential_env?: string[] }
type MCPToolOption = { server_id: string; server_tool_name: string; name: string; description: string; input_schema?: Record<string, unknown> }
type ModelToolOption = { name: string; description: string; source?: string }
type ProviderOption = { id: string; protocol: string; base_url: string; api_key_configured: boolean; api_key_env?: string; default_model?: string; default_effort?: string; supports_reasoning_effort: boolean; is_default: boolean }
type ProviderModelOption = { id: string; object?: string; owned_by?: string }
type SavedHistoryEntry = { role: "user" | "assistant" | "tool"; text: string; sequence: number; toolName?: string; toolState?: string; toolCallID?: string; attachments?: HistoryAttachment[] }

function parseHistoryAttachments(value: unknown): HistoryAttachment[] {
  if (!Array.isArray(value)) return []
  return value.slice(0, 8).flatMap((item: any) => {
    if (typeof item?.name !== "string" || typeof item?.kind !== "string") return []
    const name = item.name.split(/[\\/]/).filter(Boolean).at(-1)?.replace(/[\u0000-\u001f\u007f]/g, "").slice(0, 100) ?? ""
    if (!name) return []
    const pagesExtracted = Number(item.pages_extracted)
    const pagesTotal = Number(item.pages_total)
    return [{
      name,
      kind: item.kind.slice(0, 32),
      ...(typeof item.content_type === "string" ? { contentType: item.content_type.slice(0, 80) } : {}),
      ...(item.truncated === true ? { truncated: true } : {}),
      ...(Number.isSafeInteger(pagesExtracted) && pagesExtracted >= 0 ? { pagesExtracted } : {}),
      ...(Number.isSafeInteger(pagesTotal) && pagesTotal >= 0 ? { pagesTotal } : {}),
    }]
  })
}

function attachmentLabel(attachment: HistoryAttachment): string {
  const kind = attachment.kind.toLowerCase()
  const type = kind === "image" ? "Image" : kind === "pdf" || kind === "pdf_text" ? "PDF" : kind === "text" ? "Text" : kind === "file" ? "File" : attachment.contentType ?? kind
  const pages = attachment.pagesTotal !== undefined ? ` · ${attachment.pagesExtracted ?? 0}/${attachment.pagesTotal} pages` : ""
  return `${attachment.name} · ${type}${pages}${attachment.truncated ? " · shortened" : ""}`
}

function savedHistoryPreview(entry: SavedHistoryEntry): string {
  const files = entry.attachments?.map(attachmentLabel) ?? []
  return [entry.text, ...(files.length ? [`Attachments · ${files.join(" · ")}`] : [])].filter(Boolean).join("\n")
}

function parseSavedHistoryEntries(items: unknown): SavedHistoryEntry[] {
  if (!Array.isArray(items)) return []
  return items.flatMap((item: any) => {
    const role = item?.role === "user" || item?.role === "assistant" || item?.role === "tool" ? item.role : null
    const text = typeof item?.text === "string" ? item.text : ""
    const attachments = parseHistoryAttachments(item?.attachments)
    const sequence = Number(item?.sequence)
    if (!role || (!text.trim() && !(role === "user" && attachments.length)) || !Number.isFinite(sequence)) return []
    return [{
      role, text, sequence,
      ...(attachments.length ? { attachments } : {}),
      ...(typeof item?.name === "string" ? { toolName: item.name } : {}),
      ...(typeof item?.state === "string" ? { toolState: item.state } : {}),
      ...(typeof item?.tool_call_id === "string" ? { toolCallID: item.tool_call_id } : {}),
    }]
  })
}

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
  { name: "/sessions", description: "Search, reopen, archive, or restore saved conversations", action: "sessions" },
  { name: "/skills", description: "Search skills.sh, review/install skills, and manage installed skills", action: "skills" },
  { name: "/plugins", description: "Browse installed plugins or discover and install from a source", action: "plugins" },
  { name: "/commands", description: "Browse namespaced plugin commands", action: "plugin_commands" },
  { name: "/plugin", description: "Enable or disable a plugin manifest", action: "plugin" },
  { name: "/mcp", description: "List, add, or remove MCP servers · configuration only changes new sessions", action: "mcp" },
  { name: "/tools", description: "Inspect the tools available to the active model session", action: "tools" },
  { name: "/history", description: "Browse saved conversation entries from earlier in this session", action: "history" },
  { name: "/usage", description: "Inspect recorded token totals for this session", action: "usage" },
  { name: "/provider", description: "List providers, inspect models, or select one for a new session", action: "provider" },
  { name: "/image", description: "Opt in to ImageGen for new sessions · disabled by default", action: "image" },
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

function shortChildID(id: string) {
  return id.length <= 10 ? id : id.slice(0, 8)
}

function compactSkillSource(source: string, path: string) {
  try {
    const url = new URL(source)
    if (url.hostname === "github.com") {
      const parts = url.pathname.split("/").filter(Boolean)
      const treeIndex = parts.indexOf("tree")
      if (parts.length >= 2 && treeIndex >= 2) {
        const repository = parts.slice(0, 2).join("/")
        const ref = parts[treeIndex + 1]
        const skillPath = path || parts.slice(treeIndex + 2).join("/")
        return `${repository}${skillPath ? ` · ${skillPath}` : ""}${ref ? ` · ${ref}` : ""}`
      }
      if (parts.length >= 2) return parts.slice(0, 2).join("/")
    }
  } catch {
    // Non-URL source identifiers are shown as provided.
  }
  return path ? `${source} · ${path}` : source
}

function safeProviderURL(value: string) {
  try {
    const url = new URL(value)
    url.username = ""
    url.password = ""
    url.search = ""
    url.hash = ""
    return url.toString().replace(/\/$/, "")
  } catch {
    return value.replace(/\?.*$/, "").replace(/(https?:\/\/)[^/@]+@/i, "$1")
  }
}

function preview(value: unknown, limit = 180): string {
  if (typeof value === "string") return value.length > limit ? `${value.slice(0, limit - 1)}…` : value
  if (value === undefined || value === null) return ""
  try {
    const text = JSON.stringify(value)
    return text.length > limit ? `${text.slice(0, limit - 1)}…` : text
  } catch { return "" }
}

function selectedCopyText(selection: any): string {
  if (!selection) return ""
  const isWithin = (renderable: any, id: string) => {
    let node = renderable
    while (node) {
      if (node.id === id) return true
      node = node.parent
    }
    return false
  }
  const selected = selection.selectedRenderables ?? []
  const transcript = selected.filter((item: any) => isWithin(item, "transcript"))
  const composer = selected.filter((item: any) => isWithin(item, "composer"))
  const scoped = transcript.length ? transcript : composer
  const lines = new Map<number, Array<{ x: number; text: string }>>()
  for (const renderable of scoped) {
    const text = renderable.getSelectedText()
    if (!text) continue
    text.split("\n").forEach((line: string, index: number) => {
      const y = renderable.y + index
      const segments = lines.get(y) ?? []
      segments.push({ x: renderable.x, text: line })
      lines.set(y, segments)
    })
  }
  return [...lines.entries()].sort(([a], [b]) => a - b)
    .map(([, segments]) => segments.sort((a, b) => a.x - b.x).map((item) => item.text).join(""))
    .join("\n")
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
  const pluginSourceTextarea = useRef<TextareaRenderable>(null)
  const [entries, setEntries] = useState<Entry[]>([{ id: 0, role: "system", text: "Ready when you are. Ask about this workspace or give me a task." }])
  const [tools, setTools] = useState<ToolActivity[]>([])
  const [model, setModel] = useState(process.env.PK_MODEL || "gpt-6-luna")
  const [effort, setEffort] = useState(process.env.PK_EFFORT || "medium")
  const [sessionId, setSessionId] = useState(initialSession || "")
  const [newSessionPending, setNewSessionPending] = useState(false)
  const [usage, setUsage] = useState<{ cachedInput?: number; available: boolean } | null>(null)
  const [connected, setConnected] = useState(false)
  const [everConnected, setEverConnected] = useState(false)
  const [steeringEnabled, setSteeringEnabled] = useState(false)
  const [busy, setBusy] = useState(false)
  const [selector, setSelector] = useState<"model" | "effort" | "tasks" | "skills" | "plugins" | "plugin_candidates" | "mcp" | "tools" | "providers" | "provider_models" | "image" | "extension_commands" | "history" | "usage" | null>(null)
  const [sessionUsage, setSessionUsage] = useState<SessionUsage | null>(null)
  const [sessionUsageLoading, setSessionUsageLoading] = useState(false)
  const [sessionUsageError, setSessionUsageError] = useState("")
  const [sessionManagerOpen, setSessionManagerOpen] = useState(false)
  const [sessionManagerEvent, setSessionManagerEvent] = useState<ServerEvent | undefined>()
  const [mcpManagerOpen, setMcpManagerOpen] = useState(false)
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
  const [skillInstallations, setSkillInstallations] = useState<SkillInstalled[]>([])
  const [skillSearchResults, setSkillSearchResults] = useState<SkillSearchOption[]>([])
  const [skillCandidates, setSkillCandidates] = useState<SkillCandidate[]>([])
  const [skillReview, setSkillReview] = useState<SkillCandidate | null>(null)
  const [skillsView, setSkillsView] = useState<"available" | "installed" | "results" | "candidates" | "review">("available")
  const [skillOperation, setSkillOperation] = useState("")
  const [skillNotice, setSkillNotice] = useState("")
  const skillOperationRequest = useRef("")
  const pendingSkillCatalog = useRef("")
  const skillsPanelRequested = useRef(false)
  const [plugins, setPlugins] = useState<PluginOption[]>([])
  const [pluginSourceModalOpen, setPluginSourceModalOpen] = useState(false)
  const [pluginSourceDraft, setPluginSourceDraft] = useState("")
  const [pluginCandidates, setPluginCandidates] = useState<PluginCandidate[]>([])
  const [pluginCandidateReview, setPluginCandidateReview] = useState<PluginCandidate | null>(null)
  const [pluginCandidateSource, setPluginCandidateSource] = useState("")
  const [pluginCandidateRevision, setPluginCandidateRevision] = useState("")
  const [pluginSourceOperation, setPluginSourceOperation] = useState("")
  const pluginSourceRequest = useRef("")
  const [extensionCommands, setExtensionCommands] = useState<ExtensionCommand[]>([])
  const [mcpServers, setMcpServers] = useState<MCPServerOption[]>([])
  const [mcpTools, setMcpTools] = useState<MCPToolOption[]>([])
  const [modelTools, setModelTools] = useState<ModelToolOption[]>([])
  const [providers, setProviders] = useState<ProviderOption[]>([])
  const [providerModels, setProviderModels] = useState<ProviderModelOption[]>([])
  const [providerModelsLoading, setProviderModelsLoading] = useState(false)
  const [providerID, setProviderID] = useState("native")
  const [imagegenEnabled, setImagegenEnabled] = useState(false)
  const [imagegenDriver, setImagegenDriver] = useState("")
  const [imageConfigPending, setImageConfigPending] = useState(false)
  const [releaseUpdateAvailable, setReleaseUpdateAvailable] = useState(false)
  const sessionHasPrompt = useRef(false)
  const childSequences = useRef(new Map<string, number>())
  const childAssistantEntries = useRef(new Map<string, number>())
  const [mcpSavedTools, setMcpSavedTools] = useState(false)
  const [modelToolsSaved, setModelToolsSaved] = useState(false)
  const [modelToolsInitialized, setModelToolsInitialized] = useState(false)
  const [modelToolsPreview, setModelToolsPreview] = useState(false)
  const [modelToolsNotice, setModelToolsNotice] = useState("")
  const [modelToolsLoading, setModelToolsLoading] = useState(false)
  const [historyEntries, setHistoryEntries] = useState<SavedHistoryEntry[]>([])
  const [historyHasEarlier, setHistoryHasEarlier] = useState(false)
  const [historyBeforeSequence, setHistoryBeforeSequence] = useState(0)
  const [historyLoading, setHistoryLoading] = useState(false)
  const [historyDetailIndex, setHistoryDetailIndex] = useState<number | null>(null)
  const [historyRequestMode, setHistoryRequestMode] = useState<"latest" | "older">("latest")
  const [maintenance, setMaintenance] = useState<Maintenance | null>(null)
  const [pluginCommandRun, setPluginCommandRun] = useState<PluginCommandRun | null>(null)
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
  const newSessionCommandID = useRef("")
  const preferenceErrors = useRef(new Map<string, () => void>())
  const pendingPromptFiles = useRef(new Map<string, string[]>())
  const pendingClipboardRequests = useRef(new Set<string>())
  const pendingClipboardWrites = useRef(new Map<string, string>())
  const pendingToolsRequest = useRef("")
  const pendingHistoryRequest = useRef("")
  const pendingProviderModelsRequest = useRef("")
  const pendingImageConfigRequest = useRef("")
  const sessionIdentity = useRef(initialSession || "")
  const pendingUsageRequest = useRef<{ id: string; sessionId: string } | null>(null)
  const pendingUsageCancels = useRef(new Set<string>())
  const historySessionID = useRef("")
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

  const cancelSessionUsageRequest = () => {
    const pending = pendingUsageRequest.current
    if (pending) {
      const cancelId = transport.send("session_usage_cancel", { request_id: pending.id })
      if (cancelId) pendingUsageCancels.current.add(cancelId)
    }
    pendingUsageRequest.current = null
    setSessionUsageLoading(false)
  }

  const updateSessionIdentity = (nextSessionId: string) => {
    if (sessionIdentity.current !== nextSessionId) {
      sessionIdentity.current = nextSessionId
      cancelSessionUsageRequest()
      setSessionUsage(null)
      setSessionUsageError(nextSessionId ? "Session changed; reopen /usage to refresh totals." : "")
    }
    setSessionId(nextSessionId)
  }

  const openSessionUsage = () => {
    cancelSessionUsageRequest()
    const requestedSession = sessionIdentity.current
    setSelector("usage")
    setSelectionIndex(0)
    setSessionUsage(null)
    setSessionUsageError("")
    if (!requestedSession) {
      pendingUsageRequest.current = null
      setSessionUsageLoading(false)
      return
    }
    const id = transport.send("session_usage", { session_id: requestedSession })
    if (!id) {
      pendingUsageRequest.current = null
      setSessionUsageLoading(false)
      setSessionUsageError("Usage data is unavailable while pk is disconnected.")
      return
    }
    pendingUsageRequest.current = { id, sessionId: requestedSession }
    setSessionUsageLoading(true)
  }

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
    // OpenTUI's global selection can include renderables below the scrollbox
    // viewport. Prefer transcript text whenever the drag touched the transcript;
    // composer selections still work when they are the only selected region.
    const selected = selectedCopyText(selection)
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
    setEntries((previous) => appendTranscriptEntries(previous, { id: entryId.current++, role, text }))
  }

  const requestHistoryPage = (sessionID: string, beforeSequence: number, mode: "latest" | "older") => {
    if (!sessionID) { addEntry("system", "No saved conversation is available yet. Send a prompt first, then use /history."); return }
    if (pendingHistoryRequest.current) return
    const requestID = transport.send("history_before" as any, { session_id: sessionID, before_sequence: beforeSequence })
    if (!requestID) { addEntry("system", "Could not load saved history because pk is disconnected."); return }
    pendingHistoryRequest.current = requestID
    setHistoryRequestMode(mode)
    setHistoryLoading(true)
    setHistoryDetailIndex(null)
    setSelector("history")
  }

  const cancelHistoryRead = () => {
    if (!pendingHistoryRequest.current) return
    transport.send("history_cancel" as any)
    pendingHistoryRequest.current = ""
    setHistoryLoading(false)
  }

  const requestEarlierHistory = (sessionID = historySessionID.current || sessionId, beforeSequence = historyBeforeSequence) => {
    if (!historyHasEarlier || beforeSequence <= 0) { addEntry("system", "There are no earlier saved conversation entries."); return }
    requestHistoryPage(sessionID, beforeSequence, "older")
  }

  const openLatestHistory = () => {
    const currentSession = historySessionID.current || sessionId
    if (!currentSession) { addEntry("system", "No saved conversation is available yet. Send a prompt first, then use /history."); return }
    requestHistoryPage(currentSession, 0, "latest")
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
      : appendTranscriptEntries(entries, entry))
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
      return previous ? current.map((entry) => entry.callId === callId ? next : entry) : appendTranscriptEntries(current, next)
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
  useEffect(() => {
    if (!connected) return
    const poll = setInterval(() => transport.send("release_status" as any), 60_000)
    return () => clearInterval(poll)
  }, [connected, transport])
  useEffect(() => () => { if (copyNoticeTimer.current) clearTimeout(copyNoticeTimer.current) }, [])
  useEffect(() => { setSlashIndex(0) }, [draft])
  useEffect(() => { busyRef.current = busy }, [busy])

  useEffect(() => {
    void transport.start({ workspace, model: "", effort: "", sessionId: initialSession, providerId: process.env.PK_PROVIDER || undefined, steering: true })
  }, [transport, workspace, initialSession])

  const handleEvent = useRef<(event: ServerEvent) => void>(() => {})
  handleEvent.current = (event) => {
    setSessionManagerEvent(event)
    const data = event.payload ?? {}
    switch (event.type) {
      case "ready":
        setConnected(true)
        setEverConnected(true)
        setNewSessionPending(false)
        if (newSessionCommandID.current) setSkillNotice("")
        newSessionCommandID.current = ""
        if (Array.isArray(data.capabilities)) steeringNegotiated.current = data.capabilities.includes("steer")
        setSteeringEnabled(steeringNegotiated.current)
        if (data.model) setModel(data.model)
        if (data.effort) setEffort(data.effort)
        if (typeof data.provider_id === "string") setProviderID(data.provider_id || "native")
        if (typeof data.imagegen_enabled === "boolean") setImagegenEnabled(data.imagegen_enabled)
        if (typeof data.imagegen_driver === "string") setImagegenDriver(data.imagegen_driver)
        if (data.release_status && typeof data.release_status.reload_available === "boolean") setReleaseUpdateAvailable(data.release_status.reload_available)
        if (typeof data.session_id === "string" && data.session_id) {
          updateSessionIdentity(String(data.session_id))
          historySessionID.current = data.session_id
        }
        if (data.workspace) setMessage(String(data.workspace))
        setEntries((current) => current.filter((entry) => entry.text !== "Starting a new session…"))
        break
      case "session":
        if (data.session_id) {
          updateSessionIdentity(String(data.session_id))
          historySessionID.current = String(data.session_id)
        }
        if (typeof data.provider_id === "string") setProviderID(data.provider_id || "native")
        break
      case "history": {
        historySessionID.current = String(data.session_id ?? "")
        const saved = parseSavedHistoryEntries(data.entries)
        setHistoryEntries(saved)
        setHistoryHasEarlier(data.has_earlier === true)
        setHistoryBeforeSequence(Number(data.before_sequence ?? saved[0]?.sequence ?? 0))
        const restored = saved.map((item) => ({
          id: entryId.current++, role: item.role, text: item.role === "tool" ? "" : item.text,
          ...(item.role === "user" && item.attachments ? { historyAttachments: item.attachments } : {}),
          ...(item.role === "tool" ? { toolName: item.toolName ?? "tool", toolState: item.toolState ?? "completed", historySummary: `${item.toolName ?? "tool"} · ${["running", "working"].includes((item.toolState ?? "").toLowerCase()) ? "was running" : item.toolState ?? "completed"}`, historical: true, callId: item.toolCallID ?? `history-${item.sequence}`, detail: item.text } : {}),
          historySequence: item.sequence,
        } as Entry))
        const notices: Entry[] = data.has_earlier ? [{ id: entryId.current++, role: "system", text: "Showing recent conversation history; use /history older to browse earlier saved entries." }] : data.truncated ? [{ id: entryId.current++, role: "system", text: "Some saved conversation text was shortened to fit the replay limit." }] : []
        setEntries([...notices, ...restored].slice(-300))
        sessionHasPrompt.current = restored.some((entry) => entry.role === "user")
        if (data.session_id) updateSessionIdentity(String(data.session_id))
        break
      }
      case "history_page_started":
        if (event.id && event.id === pendingHistoryRequest.current) setHistoryLoading(true)
        break
      case "history_page": {
        if (!event.id || event.id !== pendingHistoryRequest.current) break
        pendingHistoryRequest.current = ""
        setHistoryLoading(false)
        if (data.session_id) {
          historySessionID.current = String(data.session_id)
          updateSessionIdentity(String(data.session_id))
        }
        const page = parseSavedHistoryEntries(data.entries)
        setHistoryEntries(page)
        setHistoryHasEarlier(data.has_earlier === true)
        setHistoryBeforeSequence(Number(data.before_sequence ?? page[0]?.sequence ?? 0))
        setHistoryDetailIndex(null)
        setSelectionIndex(0)
        setSelector("history")
        if (!page.length) addEntry("system", historyRequestMode === "latest" ? "This session has no saved conversation entries yet." : "No earlier saved conversation entries were returned.")
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
        sessionHasPrompt.current = true
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
      case "tool_progress": {
        const outerId = String(event.id ?? "")
        const callId = typeof data.call_id === "string" ? data.call_id : ""
        if (!outerId || outerId !== promptCommandId.current || !callId || typeof data.text !== "string") break
        const progressText = data.text.replace(/[\u0000-\u001f\u007f\s]+/g, " ").trim().slice(0, 180)
        if (!progressText) break
        setEntries((current) => {
          const existing = current.find((entry) => entry.role === "tool" && entry.callId === callId)
          if (!existing || existing.historical || ["completed", "complete", "failed", "canceled", "cancelled", "succeeded", "interrupted"].includes((existing.toolState ?? "").toLowerCase())) return current
          return current.map((entry) => entry === existing ? { ...entry, progressText } : entry)
        })
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
        if (data.session_id) updateSessionIdentity(String(data.session_id))
        break
      case "status":
        if (data.session_id) updateSessionIdentity(String(data.session_id))
        if (data.model) setModel(String(data.model))
        if (data.effort) setEffort(String(data.effort))
        if (typeof data.provider_id === "string") setProviderID(data.provider_id || "native")
        if (data.usage) setUsage({ cachedInput: data.usage.cached_input_tokens, available: data.usage.cached_input_tokens_available === true })
        break
      case "usage":
        setUsage({ cachedInput: data.cached_input_tokens, available: data.cached_input_tokens_available === true })
        break
      case "release_status":
        setReleaseUpdateAvailable(data.reload_available === true)
        break
      case "session_usage": {
        const pending = pendingUsageRequest.current
        if (!pending || event.id !== pending.id) break
        pendingUsageRequest.current = null
        setSessionUsageLoading(false)
        if (pending.sessionId !== sessionIdentity.current || data.session_id !== pending.sessionId) {
          setSessionUsage(null)
          setSessionUsageError("Session changed; reopen /usage to refresh totals.")
          break
        }
        const nullableCount = (value: unknown) => typeof value === "number" && Number.isFinite(value) && value >= 0 ? value : undefined
        const coverage = data.coverage && typeof data.coverage === "object" ? data.coverage : {}
        const responseCount = nullableCount(data.response_count) ?? 0
        setSessionUsage({
          sessionId: String(data.session_id),
          responseCount,
          inputTokens: nullableCount(data.input_tokens),
          outputTokens: nullableCount(data.output_tokens),
          cachedInputTokens: nullableCount(data.cached_input_tokens),
          uncachedInputTokens: nullableCount(data.uncached_input_tokens),
          coverage: {
            input: nullableCount(coverage.input_responses) ?? 0,
            output: nullableCount(coverage.output_responses) ?? 0,
            cachedInput: nullableCount(coverage.cached_input_responses) ?? 0,
            uncachedInput: nullableCount(coverage.uncached_input_responses) ?? 0,
          },
        })
        setSessionUsageError("")
        break
      }
      case "session_usage_cancel_requested":
        if (event.id) pendingUsageCancels.current.delete(event.id)
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
        if (!skillsPanelRequested.current || event.id !== pendingSkillCatalog.current) break
        skillsPanelRequested.current = false
        pendingSkillCatalog.current = ""
        const available = Array.isArray(data.skills) ? data.skills.filter((item: any) => item && typeof item.name === "string").map((item: any) => ({
          name: String(item.name), description: String(item.description ?? ""), path: String(item.path ?? ""), bundled: item.bundled === true, saved: item.saved === true,
        })) : []
        setSkills(available)
        setSkillsView("available")
        setSelectionIndex(0)
        setSelector("skills")
        for (const warning of Array.isArray(data.warnings) ? data.warnings : []) addEntry("system", `Skills · ${String(warning)}`)
        if (!available.length) addEntry("system", "No skills are available in this workspace or your configured skill directories.")
        break
      }
      case "skill_installations":
        if (!skillsPanelRequested.current || event.id !== pendingSkillCatalog.current) break
        skillsPanelRequested.current = false
        setSkillInstallations(Array.isArray(data.skills) ? data.skills.filter((item: any) => item && typeof item.name === "string").map((item: any) => ({ name: String(item.name), description: String(item.description ?? ""), source: String(item.source ?? ""), path: String(item.path ?? "") })) : [])
        setSkillOperation("")
        pendingSkillCatalog.current = ""
        setSkillsView("installed")
        setSelector("skills")
        setSelectionIndex(0)
        break
      case "skill_search_started":
      case "skill_source_list_started":
      case "skill_install_started":
        if (event.id === skillOperationRequest.current) setSkillOperation(event.type === "skill_search_started" ? "Searching skills.sh…" : event.type === "skill_source_list_started" ? "Loading source skills…" : "Installing selected skill…")
        break
      case "skill_search_results":
        if (event.id !== skillOperationRequest.current) break
        setSkillOperation("")
        setSkillSearchResults(Array.isArray(data.results) ? data.results.filter((item: any) => item && typeof item.name === "string").map((item: any) => ({ name: String(item.name), id: String(item.id ?? ""), source: String(item.source ?? ""), installs: Number(item.installs ?? 0), url: String(item.url ?? "") })) : [])
        setSkillsView("results")
        setSkillReview(null)
        setSelectionIndex(0)
        setSelector("skills")
        break
      case "skill_source_candidates":
        if (event.id !== skillOperationRequest.current) break
        setSkillOperation("")
        setSkillCandidates(Array.isArray(data.candidates) ? data.candidates.filter((item: any) => item && typeof item.name === "string").map((item: any) => ({ name: String(item.name), description: String(item.description ?? ""), source: String(item.source ?? data.source ?? ""), path: String(item.path ?? ""), url: String(item.url ?? "") })) : [])
        setSkillsView("candidates")
        setSkillReview(null)
        setSelectionIndex(0)
        setSelector("skills")
        break
      case "skill_installed": {
        if (event.id !== skillOperationRequest.current) break
        setSkillOperation("")
        const name = String(data.skill?.name ?? skillReview?.name ?? "skill")
        setSkillNotice(`Installed ${name} · available in new sessions. Use /new to load its instructions.`)
        addEntry("system", `Installed ${name} · available in new sessions. Use /new to load its instructions.`)
        setSkillsView("installed")
        setSkillReview(null)
        setSelector("skills")
        skillsPanelRequested.current = true
        pendingSkillCatalog.current = transport.send("skill_installed_list") ?? ""
        break
      }
      case "skill_removed":
        if (event.id !== skillOperationRequest.current) break
        setSkillOperation("")
        setSkillNotice(`Removed ${String(data.name ?? "skill")} · existing sessions keep their saved snapshot.`)
        addEntry("system", `Removed ${String(data.name ?? "skill")} · existing sessions keep their saved snapshot.`)
        setSkillsView("installed")
        setSelector("skills")
        skillsPanelRequested.current = true
        pendingSkillCatalog.current = transport.send("skill_installed_list") ?? ""
        break
      case "skill_cancel_requested":
        if (event.id === skillOperationRequest.current) setSkillOperation("Canceling skill operation…")
        break
      case "plugin_commands": {
        const available = Array.isArray(data.commands) ? data.commands.filter((item: any) => item && typeof item.name === "string").slice(0, 256).map((item: any) => ({
          name: String(item.name), extension_id: String(item.extension_id ?? ""), command_name: String(item.command_name ?? ""),
          description: String(item.description ?? "").slice(0, 1024), enabled: item.enabled !== false, error: typeof item.error === "string" ? item.error : undefined,
        })) : []
        setExtensionCommands(available)
        setSelectionIndex(0)
        setSelector("extension_commands")
        for (const issue of Array.isArray(data.issues) ? data.issues : []) addEntry("system", `Plugin commands · ${String(issue).slice(0, 240)}`)
        if (!available.length) addEntry("system", "No namespaced plugin commands are available. Enable a plugin with /plugin, then /new.")
        break
      }
      case "plugin_command_started": {
        const name = String(data.name ?? pluginCommandRun?.name ?? "plugin command")
        setPluginCommandRun((current) => current ? { ...current, name } : { id: String(event.id ?? ""), name, startedAt: Date.now() })
        addEntry("system", `Running ${name}…`)
        break
      }
      case "plugin_command_cancel_requested":
        setPluginCommandRun((current) => current ? { ...current, cancelRequested: true } : current)
        addEntry("system", "Stopping plugin command…")
        break
      case "plugin_command_result": {
        const name = String(data.name ?? "Plugin command")
        const text = typeof data.text === "string" ? data.text.slice(0, 64 * 1024) : ""
        addEntry("system", `${name}${text ? `\n${text}` : "\nCommand completed without output."}`)
        setPluginCommandRun(null)
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
        else setSelectionIndex((index) => Math.min(index, available.length))
        setSelector("plugins")
        if (event.type === "plugins_updated" && data.next_session_only) {
          const installed = data.installed === true
          addEntry("system", installed ? "Plugin installed and enabled · available in new sessions. Use /new to load it." : "Plugin configuration saved · applies to new sessions. Use /new to start one.")
          if (installed) {
            pluginSourceRequest.current = ""
            setPluginSourceOperation("")
            setPluginCandidateReview(null)
            setSelector("plugins")
          }
        }
        if (!available.length) addEntry("system", "No plugins are installed.")
        break
      }
      case "plugin_discover_started":
      case "plugin_install_started": {
        if (pluginSourceRequest.current && event.id !== pluginSourceRequest.current) break
        setPluginSourceOperation(event.type === "plugin_discover_started" ? "Inspecting plugin source…" : "Installing reviewed plugin…")
        break
      }
      case "plugin_candidates": {
        if (event.id !== pluginSourceRequest.current) break
        pluginSourceRequest.current = ""
        setPluginSourceOperation("")
        const candidates = Array.isArray(data.candidates) ? data.candidates.filter((item: any) => item && typeof item.id === "string" && typeof item.manifest_path === "string").slice(0, 100).map((item: any) => ({
          id: String(item.id), version: typeof item.version === "string" ? item.version : undefined,
          manifest_path: String(item.manifest_path), tools: Array.isArray(item.tools) ? item.tools.map(String).slice(0, 100) : [],
          commands: Array.isArray(item.commands) ? item.commands.map(String).slice(0, 100) : [], build_required: item.build_required === true,
        })) : []
        setPluginCandidates(candidates)
        setPluginCandidateSource(String(data.source ?? pluginCandidateSource))
        setPluginCandidateRevision(String(data.revision ?? ""))
        setPluginCandidateReview(null)
        setSelectionIndex(0)
        setSelector("plugin_candidates")
        for (const item of Array.isArray(data.unsupported) ? data.unsupported.slice(0, 10) : []) addEntry("system", `Plugin source · ${String(item).slice(0, 240)}`)
        if (!candidates.length) addEntry("system", "No installable plugins were found in that source.")
        break
      }
      case "plugin_source_cancel_requested":
        setPluginSourceOperation("Canceling plugin source operation…")
        break
      case "mcp_catalog":
      case "mcp_updated": {
        const servers = Array.isArray(data.servers) ? data.servers.filter((item: any) => item && typeof item.id === "string").map((item: any) => ({
          id: String(item.id), command: String(item.command ?? ""), arguments_count: Number(item.arguments_count ?? 0),
          environment_keys: Array.isArray(item.environment_keys) ? item.environment_keys.map(String) : [],
          working_directory: typeof item.working_directory === "string" ? item.working_directory : undefined,
          transport: typeof item.transport === "string" ? item.transport : undefined,
          url: typeof item.url === "string" ? safeProviderURL(item.url) : undefined,
          auth_mode: typeof item.auth_mode === "string" ? item.auth_mode : undefined,
          auth_status: typeof item.auth_status === "string" ? item.auth_status : undefined,
          credential_env: Array.isArray(item.credential_env) ? item.credential_env.map(String) : [],
        })) : []
        const tools = Array.isArray(data.tools) ? data.tools.filter((item: any) => item && typeof item.name === "string").map((item: any) => ({
          server_id: String(item.server_id ?? ""), server_tool_name: String(item.server_tool_name ?? ""),
          name: String(item.name), description: String(item.description ?? ""),
        })) : []
        setMcpServers(servers)
        setMcpTools(tools)
        setMcpSavedTools(data.saved_session_tools === true)
        if (!mcpManagerOpen) {
          if (selector !== "mcp") setSelectionIndex(0)
          setSelector("mcp")
          if (event.type === "mcp_updated" && data.next_session_only) addEntry("system", "MCP configuration saved · applies to new sessions. Use /new to start one.")
          if (!servers.length) addEntry("system", 'No MCP servers configured. Use /mcp to open the setup panel.')
        }
        break
      }
      case "mcp_auth_status": {
        const id = String(data.id ?? "MCP server")
        const status = String(data.status ?? "status updated")
        addEntry("system", `${id} · ${status === "authenticated" ? "connected" : status === "authorizing" ? "waiting for browser consent" : status === "needs_login" ? "local sign-in cleared; sign in again to reconnect" : status}`)
        if (status !== "authorizing" && !mcpManagerOpen) transport.send("mcp_list" as any)
        break
      }
      case "providers":
      case "providers_updated": {
        const available = Array.isArray(data.providers) ? data.providers.filter((item: any) => item && typeof item.id === "string").map((item: any) => ({
          id: String(item.id), protocol: String(item.protocol ?? "unknown"), base_url: String(item.base_url ?? ""),
          api_key_configured: item.api_key_configured === true,
          api_key_env: typeof item.api_key_env === "string" ? item.api_key_env : undefined,
          default_model: typeof item.default_model === "string" ? item.default_model : undefined,
          default_effort: typeof item.default_effort === "string" ? item.default_effort : undefined,
          supports_reasoning_effort: item.supports_reasoning_effort === true, is_default: item.is_default === true,
        })) : []
        setProviders(available)
        setSelectionIndex(providerID === "native" ? 0 : Math.max(0, available.findIndex((item) => item.id === providerID) + 1))
        setSelector("providers")
        if (event.type === "providers_updated" && data.next_session_only) addEntry("system", "Provider configuration saved · applies to new sessions. Use /new to start one.")
        if (!available.length) addEntry("system", 'No custom providers configured. Use `pk provider add` in a shell, then /provider list.')
        break
      }
      case "provider_models_started": {
        if (!event.id || event.id !== pendingProviderModelsRequest.current) break
        setProviderModels([])
        setProviderModelsLoading(true)
        setSelector("provider_models")
        break
      }
      case "provider_models": {
        if (!event.id || event.id !== pendingProviderModelsRequest.current) break
        pendingProviderModelsRequest.current = ""
        setProviderModelsLoading(false)
        const available = Array.isArray(data.models) ? data.models.filter((item: any) => item && typeof item.id === "string").map((item: any) => ({
          id: String(item.id), object: typeof item.object === "string" ? item.object : undefined, owned_by: typeof item.owned_by === "string" ? item.owned_by : undefined,
        })) : []
        setProviderModels(available)
        setSelectionIndex(0)
        setSelector("provider_models")
        if (!available.length) addEntry("system", `No models were returned for provider ${String(data.provider_id ?? "")} .`)
        break
      }
      case "provider_selected": {
        setProviderID(String(data.provider_id ?? "native") || "native")
        if (data.model) setModel(String(data.model))
        if (data.effort) setEffort(String(data.effort))
        addEntry("system", `Provider selected · ${String(data.provider_id || "Native Codex")} · applies to the next conversation.`)
        break
      }
      case "image_status":
      case "image_configured": {
        if (event.id !== pendingImageConfigRequest.current) break
        pendingImageConfigRequest.current = ""
        setImageConfigPending(false)
        setImagegenEnabled(data.enabled === true)
        setImagegenDriver(typeof data.driver === "string" ? data.driver : "")
        setSelectionIndex(0)
        setSelector("image")
        if (event.type === "image_configured" && data.next_session_only) {
          addEntry("system", `ImageGen ${data.enabled === true ? "enabled" : "disabled"}. Start a new session with /new to apply this change.`)
        }
        break
      }
      case "subagent": {
        const childID = String(data.child_id ?? "child")
        const sequence = Number(data.sequence ?? 0)
        const seen = childSequences.current.get(childID) ?? 0
        if (sequence && sequence <= seen) break
        if (sequence) childSequences.current.set(childID, sequence)
        if (childSequences.current.size > 300) childSequences.current.delete(childSequences.current.keys().next().value!)
        const subtype = String(data.type ?? "subagent")
        if (subtype === "assistant") {
          const phase = String(data.phase ?? "")
          const text = typeof data.text === "string" ? data.text.trim() : ""
          if (text && ["commentary", "final_answer", "final"].includes(phase)) {
            const existingID = childAssistantEntries.current.get(childID)
            if (existingID !== undefined) updateEntry(existingID, (entry) => ({ ...entry, text: `${entry.text}\n${text}`.slice(-16 * 1024) }))
            else {
              const id = entryId.current++
              childAssistantEntries.current.set(childID, id)
              if (childAssistantEntries.current.size > 128) childAssistantEntries.current.delete(childAssistantEntries.current.keys().next().value!)
              setEntries((current) => appendTranscriptEntries(current, { id, role: "assistant" as const, speaker: `agent ${shortChildID(childID)}`, detail: `Child ID ${childID}`, text: text.slice(-16 * 1024) }))
            }
          }
        } else if (subtype === "tool_call") {
          const name = String(data.name ?? "tool")
          const state = String(data.state ?? "running")
          const terminal = ["completed", "complete", "failed", "canceled", "cancelled", "succeeded", "interrupted"].includes(state.toLowerCase())
          const callId = `child:${childID}:${String(data.call_id ?? name)}`
          setEntries((current) => {
            const prior = current.find((entry) => entry.callId === callId)
            const next: Entry = { id: prior?.id ?? entryId.current++, role: "tool", callId, toolName: `agent ${shortChildID(childID)} · ${name}`, toolState: state, commandPreview: state, text: "", detail: `Child ID ${childID} · ${name} ${state}` }
            return prior ? current.map((entry) => entry.callId === callId ? next : entry) : appendTranscriptEntries(current, next)
          })
        } else {
          const state = String(data.state ?? "running")
          const childEntryId = `subagent:${childID}`
          const text = typeof data.text === "string" && data.text ? data.text.slice(0, 240) : ""
          setEntries((current) => {
            const prior = current.find((entry) => entry.callId === childEntryId)
            const next: Entry = { id: prior?.id ?? entryId.current++, role: "tool", callId: childEntryId, toolName: `agent ${shortChildID(childID)}`, toolState: state, commandPreview: state, text, detail: `Child ID ${childID}${text ? ` · ${text}` : ` · ${state === "running" ? "Working in delegated task" : `Task ${state}`}`}` }
            return prior ? current.map((entry) => entry.callId === childEntryId ? next : entry) : appendTranscriptEntries(current, next)
          })
        }
        break
      }
      case "tool_catalog": {
        if (!event.id || event.id !== pendingToolsRequest.current) break
        pendingToolsRequest.current = ""
        setModelToolsLoading(false)
        const available = Array.isArray(data.tools) ? data.tools.filter((item: any) => item && typeof item.name === "string").map((item: any) => ({
          name: String(item.name), description: String(item.description ?? ""), source: typeof item.source === "string" ? item.source : undefined,
        })) : []
        setModelTools(available)
        setModelToolsSaved(data.saved === true)
        const preview = data.preview === true
        const initialized = typeof data.initialized === "boolean" ? data.initialized : (sessionHasPrompt.current || data.saved === true || available.length > 0)
        setModelToolsInitialized(initialized)
        setModelToolsPreview(preview)
        setModelToolsNotice(typeof data.notice === "string" ? data.notice : "")
        setSelectionIndex(0)
        setSelector("tools")
        if (!available.length && initialized && !preview) addEntry("system", "No model tools are available in this session.")
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
        if (data.session_id) updateSessionIdentity(String(data.session_id))
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
        if (data.session_id) updateSessionIdentity(String(data.session_id))
        setBusy(false)
        waiting.current = false
        setQuestion(null)
        break
      case "detached":
        addEntry("system", `Detached from session ${sessionId || ""}`)
        updateSessionIdentity("")
        setBusy(false)
        setActivityStartedAt(null)
        enterPhase("idle")
        setTools([])
        void transport.close().finally(() => renderer.destroy())
        break
      case "error":
        if (event.id && pendingUsageCancels.current.delete(event.id)) break
        {
          const pending = pendingUsageRequest.current
          const usageError = pending && event.id === pending.id || data.request_type === "session_usage" || data.command_type === "session_usage"
          if (usageError) {
            if (!pending || event.id !== pending.id) break
            pendingUsageRequest.current = null
            setSessionUsageLoading(false)
            setSessionUsageError(String(data.message ?? "Could not load session usage."))
            break
          }
        }
        if (data.request_type === "image_status" || data.command_type === "image_status" || data.request_type === "image_configure" || data.command_type === "image_configure") {
          if (!event.id || event.id !== pendingImageConfigRequest.current) break
          pendingImageConfigRequest.current = ""
          setImageConfigPending(false)
          addEntry("system", `ImageGen setup · ${String(data.message ?? "Configuration request failed.").slice(0, 300)}`)
          break
        }
        if (event.id && event.id === newSessionCommandID.current) {
          newSessionCommandID.current = ""
          setNewSessionPending(false)
        }
        if (event.id && event.id === pluginSourceRequest.current) {
          pluginSourceRequest.current = ""
          setPluginSourceOperation("")
          addEntry("system", `Plugin source · ${String(data.message ?? "The plugin operation failed.").slice(0, 400)}`)
          break
        }
        if (event.id && event.id === skillOperationRequest.current) {
          skillOperationRequest.current = ""
          setSkillOperation("")
          addEntry("system", `Skills · ${String(data.message ?? "The skill operation failed.")}`)
          break
        }
        if (event.id && event.id === pendingSkillCatalog.current) {
          pendingSkillCatalog.current = ""
          skillsPanelRequested.current = false
          addEntry("system", `Skills · ${String(data.message ?? "Could not load skills.")}`)
          break
        }
        if (pluginCommandRun && (!event.id || event.id === pluginCommandRun.id || data.request_type === "plugin_command_execute")) setPluginCommandRun(null)
        if (data.command_type === "provider_models" || data.request_type === "provider_models") {
          if (!event.id || event.id !== pendingProviderModelsRequest.current) break
          pendingProviderModelsRequest.current = ""
          setProviderModelsLoading(false)
        }
        if (data.command_type === "tools" || data.request_type === "tools") {
          if (!event.id || event.id === pendingToolsRequest.current) pendingToolsRequest.current = ""
          setModelToolsLoading(false)
          setSelector(null)
          textarea.current?.focus()
          addEntry("system", String(data.message ?? "Could not load the model tool catalog."))
          break
        }
        if ((data.command_type === "history_before" || data.request_type === "history_before") && (!event.id || event.id === pendingHistoryRequest.current)) {
          pendingHistoryRequest.current = ""
          setHistoryLoading(false)
          setSelector(null)
          textarea.current?.focus()
          addEntry("system", String(data.message ?? "Could not load earlier conversation history."))
          break
        }
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
        setPluginCommandRun(null)
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

  useEffect(() => {
    if (!pluginSourceModalOpen) return
    pluginSourceTextarea.current?.setText(pluginSourceDraft)
    pluginSourceTextarea.current?.focus()
  }, [pluginSourceModalOpen])

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
      setEntries((previous) => appendTranscriptEntries(previous, steeringEntry))
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

  const requestSkillOperation = (type: "skill_search" | "skill_source_list" | "skill_install" | "skill_remove", payload: Record<string, unknown>, label: string) => {
    if (busy || maintenance || pluginCommandRun || question || activeTaskId) {
      addEntry("system", "Skill catalog changes and network searches are available while the foreground session is idle.")
      return
    }
    const id = transport.send(type, payload)
    if (!id) { addEntry("system", "Skills · agent connection is unavailable."); return }
    skillOperationRequest.current = id
    setSkillOperation(label)
    setSkillNotice("")
    setSelector("skills")
    setSelectionIndex(0)
    if (type === "skill_search") { setSkillsView("results"); setSkillSearchResults([]); setSkillReview(null) }
    if (type === "skill_source_list") { setSkillsView("candidates"); setSkillCandidates([]); setSkillReview(null) }
  }

  const openSkillsView = (view: "installed" | "available") => {
    setSkillsView(view)
    setSelector("skills")
    setSelectionIndex(0)
    if (view === "available") {
      skillsPanelRequested.current = true
      const id = transport.send("skills") ?? ""
      pendingSkillCatalog.current = id
    } else {
      skillsPanelRequested.current = true
      const id = transport.send("skill_installed_list") ?? ""
      pendingSkillCatalog.current = id
    }
  }

  const beginPluginDiscovery = (sourceInput: string) => {
    const source = sourceInput.trim()
    if (!source) { addEntry("system", "Enter a local repository path, GitHub owner/repo, or HTTPS GitHub URL."); return }
    if (busy || turnActive.current || waiting.current || question || activeTaskId || maintenance || pluginCommandRun) {
      addEntry("system", "Plugin discovery is available while the session is idle.")
      return
    }
    setPluginSourceModalOpen(false)
    setPluginCandidateSource(source)
    setPluginCandidateReview(null)
    setPluginSourceOperation("Inspecting plugin source…")
    setSelector("plugin_candidates")
    pluginSourceRequest.current = transport.send("plugins_discover" as any, { source }) ?? ""
    if (!pluginSourceRequest.current) { setPluginSourceOperation(""); addEntry("system", "Could not inspect plugin source because the RPC connection is unavailable.") }
  }

  const requestImageStatus = () => {
    setSelector("image")
    setImageConfigPending(true)
    pendingImageConfigRequest.current = transport.send("image_status") ?? ""
    if (!pendingImageConfigRequest.current) {
      setImageConfigPending(false)
      addEntry("system", "ImageGen status is unavailable because the agent connection is closed.")
    }
  }

  const configureImagegen = (enabled: boolean) => {
    if (imageConfigPending) return
    if (busy || turnActive.current || waiting.current || question || activeTaskId || maintenance || reloadPending) {
      addEntry("system", "ImageGen configuration is available while the session is idle.")
      return
    }
    pendingImageConfigRequest.current = transport.send("image_configure", {
      enabled,
      ...(enabled ? { driver: "gpt-6-astra" } : {}),
    }) ?? ""
    if (pendingImageConfigRequest.current) { setSelector("image"); setImageConfigPending(true) }
    else addEntry("system", "Could not update ImageGen because the agent connection is unavailable.")
  }

  const openPluginSourceEntry = () => {
    if (busy || turnActive.current || waiting.current || question || activeTaskId || maintenance || pluginCommandRun) {
      addEntry("system", "Plugin discovery is available while the session is idle.")
      return
    }
    setPluginSourceDraft("")
    setPluginSourceModalOpen(true)
  }

  const runSlashCommand = (raw: string) => {
    const [head, ...args] = parseSlashWords(raw)
    const command = slashCommands.find((item) => item.name === head)
    if (!command) {
      const extension = extensionCommands.find((item) => item.name === head)
      if (extension) {
        if (!extension.enabled || extension.error) { addEntry("system", `${head} is unavailable. Check /plugins and enable the plugin for a new session.`); return }
        const rawTail = raw.trim().replace(/^\S+(?:\s+)?/, "")
        if (new TextEncoder().encode(rawTail).length > 16 * 1024) { addEntry("system", "Plugin command arguments exceed 16 KiB."); return }
        const request = transport.send("plugin_command_execute" as any, { name: extension.name, arguments: rawTail })
        if (!request) addEntry("system", "Plugin command could not start because the RPC connection is unavailable.")
        else setPluginCommandRun({ id: request, name: extension.name, startedAt: Date.now() })
        return
      }
      addEntry("system", `Unknown command: ${head}. Type /help to see available commands.`)
      return
    }
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
      case "sessions": setSessionManagerOpen(true); break
      case "skills": {
        const [operation, ...rest] = args
        if (!operation || operation === "installed") openSkillsView("installed")
        else if (operation === "available") openSkillsView("available")
        else if (operation === "search" && rest.join(" ").trim()) requestSkillOperation("skill_search", { query: rest.join(" ").trim() }, "Searching skills.sh…")
        else if (operation === "browse" && rest[0]) requestSkillOperation("skill_source_list", { source: rest[0] }, "Loading source skills…")
        else if (operation === "remove" && rest[0]) requestSkillOperation("skill_remove", { name: rest.join(" ") }, `Removing ${rest.join(" ")}…`)
        else addEntry("system", "Usage: /skills [installed|available] · /skills search QUERY · /skills browse SOURCE · /skills remove NAME")
        break
      }
      case "plugins": {
        const [operation, ...rest] = args
        if (operation === "discover" && rest.length) beginPluginDiscovery(rest.join(" "))
        else if (!operation || operation === "list") transport.send("plugins_list" as any)
        else addEntry("system", 'Usage: /plugins · /plugins discover OWNER/REPO|URL|LOCAL_PATH · select a candidate to review, then press i to install.')
        break
      }
      case "plugin_commands": transport.send("plugin_commands_list" as any); break
      case "mcp": {
        const [operation, ...options] = args
        if (!operation || operation === "list") setMcpManagerOpen(true)
        else if (operation === "remove" && options[0]) transport.send("mcp_remove" as any, { id: options[0] })
        else if (operation === "login" && options[0]) transport.send("mcp_login" as any, { id: options[0] })
        else if (operation === "logout" && options[0]) transport.send("mcp_logout" as any, { id: options[0] })
        else if (operation === "add") {
          const server: { id?: string; command?: string; url?: string; args: string[]; env: Record<string, string>; working_directory?: string; auth?: Record<string, string> } = { args: [], env: {} }
          let invalid = ""
          for (let index = 0; index < options.length; index++) {
            const option = options[index]!
            const value = options[++index]
            if (value === undefined) { invalid = `Missing value after ${option}.`; break }
            if (option === "--id") server.id = value
            else if (option === "--command") server.command = value
            else if (option === "--url") server.url = value
            else if (option === "--auth") {
              if (value === "oauth") server.auth = { mode: "oauth" }
              else if (value === "bearer-env") server.auth = { mode: "bearer_env" }
              else if (value === "header-env") server.auth = { mode: "header_env" }
              else { invalid = "Auth must be oauth, bearer-env, or header-env."; break }
            }
            else if (option === "--bearer-env") { server.auth ??= { mode: "bearer_env" }; server.auth.bearer_env = value }
            else if (option === "--header") { server.auth ??= { mode: "header_env" }; server.auth.header_name = value }
            else if (option === "--header-env") { server.auth ??= { mode: "header_env" }; server.auth.header_value_env = value }
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
          const remote = Boolean(server.url)
          if (!server.id || (remote ? server.command : !server.command)) invalid ||= 'Use /mcp add --id ID --command PATH [--arg ARG] [--env KEY=VALUE] [--cwd DIR] or /mcp add --id ID --url URL [--auth oauth|bearer-env|header-env] [--bearer-env ENV|--header NAME --header-env ENV].'
          if (server.url && server.command) invalid ||= "Choose either a remote --url or a local --command."
          if (server.auth?.mode === "bearer_env" && !server.auth.bearer_env) invalid ||= "Bearer auth requires --bearer-env ENV."
          if (server.auth?.mode === "header_env" && (!server.auth.header_name || !server.auth.header_value_env)) invalid ||= "Header auth requires --header NAME and --header-env ENV."
          if (server.auth?.mode === "bearer_env" && server.auth.bearer_env && !/^[A-Za-z_][A-Za-z0-9_]*$/.test(server.auth.bearer_env)) invalid ||= "Bearer credential reference must be an environment variable name."
          if (server.auth?.mode === "header_env" && server.auth.header_value_env && !/^[A-Za-z_][A-Za-z0-9_]*$/.test(server.auth.header_value_env)) invalid ||= "Header credential reference must be an environment variable name."
          if (invalid) addEntry("system", invalid)
          else transport.send("mcp_add" as any, { server })
        } else addEntry("system", 'Usage: /mcp · /mcp add --id ID --command PATH [--arg ARG] [--env KEY=VALUE] [--cwd DIR] · /mcp add --id ID --url URL [--auth oauth|bearer-env|header-env] · /mcp login|logout ID · /mcp remove ID')
        break
      }
      case "tools": {
        setSelector("tools")
        setModelToolsLoading(true)
        pendingToolsRequest.current = transport.send("tools" as any) ?? ""
        if (!pendingToolsRequest.current) setModelToolsLoading(false)
        break
      }
      case "history": {
        const [operation] = args
        if (!operation) openLatestHistory()
        else if (operation === "older") requestEarlierHistory()
        else addEntry("system", "Usage: /history · open latest saved entries · /history older · load an earlier page")
        break
      }
      case "provider": {
        const [operation, ...rest] = args
        const target = rest.join(" ")
        if (!operation || operation === "list") transport.send("providers_list" as any)
        else if (operation === "models" && target) {
          pendingProviderModelsRequest.current = transport.send("provider_models" as any, { provider_id: target }) ?? ""
          setProviderModelsLoading(Boolean(pendingProviderModelsRequest.current))
          if (pendingProviderModelsRequest.current) {
            setProviderModels([])
            setSelector("provider_models")
          }
        }
        else if (operation === "use" && target) {
          if (busy || turnActive.current || sessionHasPrompt.current) addEntry("system", "Provider is fixed after the first prompt. Use /new before switching providers.")
          else transport.send("provider_select" as any, { provider_id: target === "native" ? "" : target })
        } else if (operation === "default" && target) {
          transport.send("provider_default" as any, { provider_id: target === "native" ? "" : target })
        } else if (operation === "remove" && target) {
          transport.send("provider_remove" as any, { id: target })
        } else if (operation === "add") {
          const provider: { id?: string; protocol?: string; base_url?: string; api_key_env?: string; default_model?: string; default_effort?: string; supports_reasoning_effort?: boolean } = {}
          let invalid = ""
          for (let index = 0; index < rest.length; index++) {
            const option = rest[index]!
            if (option === "--reasoning-effort") { provider.supports_reasoning_effort = true; continue }
            const value = rest[++index]
            if (value === undefined) { invalid = `Missing value after ${option}.`; break }
            if (option === "--id") provider.id = value
            else if (option === "--protocol") provider.protocol = value
            else if (option === "--base-url") provider.base_url = value
            else if (option === "--api-key-env") provider.api_key_env = value
            else if (option === "--model") provider.default_model = value
            else if (option === "--effort") provider.default_effort = value
            else { invalid = `Unknown provider option: ${option}.`; break }
          }
          if (!provider.id || !provider.protocol || !provider.base_url) invalid ||= 'Usage: /provider add --id ID --protocol responses|chat_completions --base-url URL [--api-key-env ENV] [--model ID] [--effort low|medium|high|xhigh|max] [--reasoning-effort]'
          if (provider.protocol && !["responses", "chat_completions"].includes(provider.protocol)) invalid ||= "Protocol must be responses or chat_completions."
          if (provider.default_effort && !efforts.includes(provider.default_effort)) invalid ||= `Unsupported effort ${provider.default_effort}. Choose ${efforts.join(", ")}.`
          if (provider.api_key_env && !/^[A-Za-z_][A-Za-z0-9_]*$/.test(provider.api_key_env)) invalid ||= "API key reference must be an environment variable name, such as OPENAI_API_KEY."
          if (invalid) addEntry("system", invalid)
          else transport.send("provider_add" as any, provider as Record<string, unknown>)
        } else addEntry("system", 'Usage: /provider [list] · /provider models ID · /provider use ID|native · /provider default ID|native · /provider add --id ID --protocol responses|chat_completions --base-url URL [--api-key-env ENV] [--model ID] · /provider remove ID')
        break
      }
      case "image": {
        const operation = (args[0] ?? "").toLowerCase()
        if (operation === "enable" || operation === "on") configureImagegen(true)
        else if (operation === "disable" || operation === "off") configureImagegen(false)
        else if (!operation || operation === "status") requestImageStatus()
        else addEntry("system", "Usage: /image · /image enable · /image disable. ImageGen uses the Astra driver and applies to new sessions.")
        break
      }
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
        reloadCommandId.current = transport.send("reload") ?? ""
        if (!reloadCommandId.current) addEntry("system", "Could not prepare reload because the agent connection is unavailable.")
        else { setReloadPending(true); addEntry("system", "Saving this session before reload…") }
        break
      }
      case "new":
        if (busy) { addEntry("system", "Wait for the active turn to finish before starting a new session."); break }
        setSkillNotice("")
        cancelHistoryRead()
        newSessionCommandID.current = transport.send("new") ?? ""
        setNewSessionPending(Boolean(newSessionCommandID.current))
        setEntries([])
        sessionHasPrompt.current = false
        updateSessionIdentity("")
        historySessionID.current = ""
        pendingHistoryRequest.current = ""
        setHistoryEntries([])
        setHistoryBeforeSequence(0)
        setHistoryHasEarlier(false)
        setHistoryLoading(false)
        break
      case "attach":
        if (!args[0]) { addEntry("system", "Usage: /attach SESSION_ID"); break }
        updateSessionIdentity("")
        cancelHistoryRead()
        historySessionID.current = ""
        pendingHistoryRequest.current = ""
        setHistoryEntries([])
        setHistoryBeforeSequence(0)
        setHistoryHasEarlier(false)
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
      case "usage": openSessionUsage(); break
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
      case "help": addEntry("system", "Enter sends · Shift-Enter or Ctrl-J adds a line · Esc stops/closes panels · Ctrl-P opens commands · Ctrl/Cmd-V or /paste imports clipboard · selecting transcript text copies it when OSC 52 is supported · Ctrl-Y copies selected text · /file PATH · /files · /task new [--workspace PATH] PROMPT · /tasks · /skills · /plugins · /commands · /plugin enable MANIFEST · /plugin disable ID · /mcp · /mcp add --id ID --command PATH · /mcp remove ID · /provider list|models ID|use ID|default ID|add|remove · /tools · /usage · /history older · /update [--source PATH] · /rollback · /reload · /new · /attach ID · /detach · /status · /login · /help · /exit"); break
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

  const installReviewedSkill = () => {
    if (skillsView !== "review" || !skillReview) return
    requestSkillOperation("skill_install", { source: skillReview.source, path: skillReview.path }, `Installing ${skillReview.name}…`)
  }

  const removeInstalledSkill = (index: number) => {
    const skill = skillInstallations[index]
    if (!skill) return
    requestSkillOperation("skill_remove", { name: skill.name }, `Removing ${skill.name}…`)
  }

  const installReviewedPlugin = () => {
    const candidate = pluginCandidateReview
    if (!candidate || pluginSourceOperation || candidate.build_required) return
    if (busy || turnActive.current || waiting.current || question || activeTaskId || maintenance || pluginCommandRun) {
      addEntry("system", "Plugin installation is available while the session is idle.")
      return
    }
    pluginSourceRequest.current = transport.send("plugins_install" as any, {
      source: pluginCandidateSource,
      manifest_path: candidate.manifest_path,
      revision: pluginCandidateRevision,
    }) ?? ""
    if (pluginSourceRequest.current) setPluginSourceOperation("Installing reviewed plugin…")
    else addEntry("system", "Could not install the plugin because the RPC connection is unavailable.")
  }

  const activateSelectorOption = (index: number) => {
    if (selector === "history") {
      if (index >= historyEntries.length) {
        requestEarlierHistory()
      } else {
        setHistoryDetailIndex(index)
        setSelectionIndex(index)
      }
    } else if (selector === "tasks") {
      const task: any = tasks[index]
      if (task?.task_id) transport.send("task_attach", { task_id: task.task_id })
      else if (task?.session_id) runSlashCommand(`/attach ${task.session_id}`)
      setSelector(null)
      textarea.current?.focus()
    } else if (selector === "skills") {
      if (skillsView === "results") {
        const result = skillSearchResults[index]
        if (result) requestSkillOperation("skill_source_list", { source: result.url }, `Loading ${result.name}…`)
      } else if (skillsView === "candidates") {
        const candidate = skillCandidates[index]
        if (candidate) { setSkillReview(candidate); setSkillsView("review") }
      } else if (skillsView === "installed") {
        const skill = skillInstallations[index]
        if (skill) {
          const current = textarea.current?.plainText ?? draft
          const instruction = `Use the ${skill.name} skill for this task.`
          const inserted = current ? `${current}${current.endsWith("\n") ? "" : "\n"}${instruction}` : instruction
          textarea.current?.setText(inserted)
          textarea.current?.focus()
          setDraft(inserted)
          addEntry("system", `Inserted an instruction for ${skill.name}; review it before sending.`)
          setSelector(null)
        }
      } else {
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
      }
    } else if (selector === "extension_commands") {
      const item = extensionCommands[index]
      if (item) {
        const current = textarea.current?.plainText ?? draft
        const inserted = `${current}${current && !current.endsWith("\n") ? "\n" : ""}${item.name} `
        textarea.current?.setText(inserted)
        setDraft(inserted)
        setSelector(null)
        textarea.current?.focus()
      }
    } else if (selector === "plugin_candidates") {
      const candidate = pluginCandidates[index]
      if (candidate) setPluginCandidateReview(candidate)
    } else if (selector === "plugins") {
      if (index === 0) { openPluginSourceEntry(); return }
      const plugin = plugins[index - 1]
      if (plugin) {
        if (plugin.enabled) transport.send("plugins_disable" as any, { id: plugin.id })
        else if (plugin.error) addEntry("system", `Cannot enable ${plugin.id}: ${plugin.error}`)
        else if (plugin.manifest_path) transport.send("plugins_enable" as any, { manifest_path: plugin.manifest_path })
        else addEntry("system", `Cannot enable ${plugin.id}: its manifest path is unavailable.`)
      }
    } else if (selector === "mcp") {
      const server = mcpServers[index]
      if (server) addEntry("system", server.url
        ? `MCP ${server.id} · remote ${server.url} · auth ${server.auth_mode ?? "anonymous"} (${server.auth_status ?? "configured"})${server.credential_env?.length ? ` · credential env ${server.credential_env.join(", ")}` : ""}. OAuth: /mcp login ${server.id} or /mcp logout ${server.id}. Remove with /mcp remove ${server.id}; configuration changes apply to new sessions.`
        : `MCP ${server.id} · ${server.command} · ${server.arguments_count} args · environment keys: ${server.environment_keys.join(", ") || "none"} · ${server.working_directory || "default working directory"}. Use /mcp remove ${server.id} to remove it; changes apply to new sessions.`)
    } else if (selector === "image") {
      configureImagegen(!imagegenEnabled)
    } else if (selector === "providers") {
      const provider = index === 0 ? null : providers[index - 1]
      const id = provider?.id ?? "native"
      if (busy || turnActive.current || sessionHasPrompt.current) {
        addEntry("system", "Provider is fixed after the first prompt. Use /new before switching providers.")
      } else {
        transport.send("provider_select" as any, { provider_id: id === "native" ? "" : id })
        setSelector(null)
        textarea.current?.focus()
      }
    } else if (selector === "provider_models") {
      const item = providerModels[index]
      if (item) addEntry("system", `Model · ${item.id}${item.owned_by ? ` · ${item.owned_by}` : ""}${item.object ? ` · ${item.object}` : ""}`)
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
    if (sessionManagerOpen || mcpManagerOpen) return
    if (pluginSourceModalOpen) {
      if (key.name === "escape" || key.name === "esc") {
        setPluginSourceModalOpen(false)
        textarea.current?.focus()
      }
      return
    }
    const liveDraft = textarea.current?.plainText ?? draft
    const isEscape = key.name === "escape" || key.name === "esc"
    const commandModifier = key.super === true || key.meta === true
    if ((commandModifier || key.ctrl) && key.name.toLowerCase() === "v") {
      key.preventDefault()
      requestClipboardPaste()
      return
    }
    if ((commandModifier && key.name.toLowerCase() === "c") || (key.ctrl && key.name.toLowerCase() === "y")) {
      const selection = selectedCopyText(renderer.getSelection())
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
    if (isEscape && pluginCommandRun) {
      transport.send("plugin_command_cancel" as any)
      setPluginCommandRun({ ...pluginCommandRun, cancelRequested: true })
      addEntry("system", "Stopping plugin command…")
      return
    }
    if (selector) {
      const count = selector === "usage" ? 0 : selector === "model" ? models.length : selector === "effort" ? efforts.length : selector === "tasks" ? tasks.length : selector === "skills" ? skills.length : selector === "plugins" ? plugins.length + 1 : selector === "plugin_candidates" ? pluginCandidates.length : selector === "mcp" ? mcpServers.length : selector === "providers" ? providers.length + 1 : selector === "provider_models" ? providerModels.length : selector === "image" ? 1 : selector === "extension_commands" ? extensionCommands.length : selector === "history" ? historyEntries.length + (historyHasEarlier ? 1 : 0) : modelTools.length
      if (selector === "plugin_candidates" && pluginSourceOperation && isEscape) {
        transport.send("plugin_source_cancel" as any)
        pluginSourceRequest.current = ""
        setPluginSourceOperation("")
        setSelector(null)
        textarea.current?.focus()
        return
      }
      if (selector === "plugin_candidates" && pluginCandidateReview && isEscape) {
        setPluginCandidateReview(null)
        return
      }
      if (selector === "plugin_candidates" && pluginCandidateReview && (key.name.toLowerCase() === "i" || key.name === "return")) {
        key.preventDefault()
        installReviewedPlugin()
        return
      }
      if (selector === "skills" && skillOperation && isEscape) {
        transport.send("skill_cancel")
        skillOperationRequest.current = ""
        setSkillOperation("")
        setSelector(null)
        textarea.current?.focus()
        return
      }
      if (selector === "skills" && skillsView === "candidates" && isEscape) {
        setSkillsView("results")
        return
      }
      if (selector === "skills" && skillsView === "review" && isEscape) {
        setSkillReview(null)
        setSkillsView("candidates")
        return
      }
      if (selector === "skills" && skillsView === "review" && (key.name.toLowerCase() === "i" || key.name === "return")) {
        key.preventDefault()
        installReviewedSkill()
        return
      }
      if (selector === "skills" && skillsView === "installed" && key.name.toLowerCase() === "x") {
        key.preventDefault()
        removeInstalledSkill(selectionIndex)
        return
      }
      if (isEscape) {
        if (selector === "usage") {
          cancelSessionUsageRequest()
        }
        if (selector === "skills") {
          skillsPanelRequested.current = false
          pendingSkillCatalog.current = ""
        }
        if (selector === "tools" && pendingToolsRequest.current) {
          pendingToolsRequest.current = ""
          setModelToolsLoading(false)
        }
        if (selector === "provider_models" && pendingProviderModelsRequest.current) {
          pendingProviderModelsRequest.current = ""
          setProviderModelsLoading(false)
        }
        if (selector === "image" && pendingImageConfigRequest.current) {
          pendingImageConfigRequest.current = ""
          setImageConfigPending(false)
        }
        if (selector === "history" && pendingHistoryRequest.current) {
          cancelHistoryRead()
        }
        setSelector(null); textarea.current?.focus(); return
      }
      const optionCount = selector === "skills" ? skillOptionCount : count
      if (selector === "usage") {
        if (key.name === "return") key.preventDefault()
        return
      }
      if (key.name === "up" && optionCount > 0) { setSelectionIndex((index) => (index - 1 + optionCount) % optionCount); return }
      if (key.name === "down" && optionCount > 0) { setSelectionIndex((index) => (index + 1) % optionCount); return }
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
    if (liveDraft.startsWith("/")) {
      const typedCommand = liveDraft.trim().split(/\s/)[0] || "/"
      const filtered = availableSlashCommands.filter((item) => item.name.startsWith(typedCommand))
      if (key.name === "return" && filtered.length && !/\s/.test(liveDraft.trim())) {
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

  const selectedOptions = selector === "usage" ? []
    : selector === "model" ? models.map((item) => ({ label: item.label, value: item.id, description: item.id, state: item.id === model ? "current" : "" }))
    : selector === "history" ? [
      ...historyEntries.map((item) => ({ label: `${item.role === "user" ? "You" : item.role === "tool" ? `Tool · ${item.toolName ?? "tool"}${item.toolState ? ` · ${item.toolState}` : ""}` : "pk"} · #${item.sequence}`, value: `entry-${item.sequence}`, description: savedHistoryPreview(item).slice(0, 240), state: "saved" })),
      ...(historyHasEarlier ? [{ label: "Load earlier entries…", value: "history-older", description: `Browse entries before #${historyBeforeSequence}`, state: historyLoading ? "loading" : "more" }] : []),
    ]
    : selector === "providers" ? [{ label: "Native Codex", value: "native", description: "Built-in Codex provider", state: providerID === "native" ? "selected" : "" }, ...providers.map((item) => ({ label: item.id, value: item.id, description: `${item.protocol} · ${safeProviderURL(item.base_url)} · key ${item.api_key_configured ? (item.api_key_env ? `env ${item.api_key_env}` : "configured") : "missing"}${item.default_model ? ` · ${item.default_model}` : ""}`, state: item.is_default ? "default" : item.id === providerID ? "selected" : "" }))]
    : selector === "image" ? [{ label: imagegenEnabled ? "Disable ImageGen" : "Enable ImageGen", value: imagegenEnabled ? "disable" : "enable", description: imageConfigPending ? "Saving configuration…" : imagegenEnabled ? `Enabled · ${imagegenDriver || "gpt-6-astra"}` : "Off by default · uses your ChatGPT login", state: imagegenEnabled ? "enabled" : "disabled" }]
      : selector === "extension_commands" ? extensionCommands.map((item) => ({ label: item.name, value: item.name, description: item.description || `Plugin command · ${item.extension_id}`, state: "available" }))
      : selector === "provider_models" ? providerModels.map((item) => ({ label: item.id, value: item.id, description: [item.object, item.owned_by].filter(Boolean).join(" · "), state: "model" }))
      : selector === "effort" ? efforts.map((item) => ({ label: `${item[0]!.toUpperCase()}${item.slice(1)} reasoning`, value: item, description: "", state: item === effort ? "current" : "" }))
      : selector === "tasks" ? tasks.map((task: any) => ({ label: task.title || task.prompt || task.session_id || task.task_id, value: task.session_id || task.task_id, description: `${task.kind ?? "task"} · ${task.status ?? task.updated_at ?? "saved"}`, state: "" }))
        : selector === "skills" ? skillsView === "results" ? skillSearchResults.map((skill) => ({ label: skill.name, value: skill.id, description: `${skill.source} · ${skill.installs.toLocaleString()} installs · Enter to inspect source`, state: "search result" }))
          : skillsView === "candidates" ? skillCandidates.map((skill) => ({ label: skill.name, value: skill.path, description: `${skill.description || "No description"} · ${skill.source}`, state: "review" }))
            : skillsView === "installed" ? skillInstallations.map((skill) => ({ label: skill.name, value: skill.name, description: `${skill.description || "Managed skill"}${skill.source ? ` · ${skill.source}` : ""}`, state: "installed" }))
              : skillsView === "review" ? []
            : skills.map((skill) => ({ label: skill.name, value: skill.name, description: `${preview(skill.description || "No description", 140)} · ${skill.path}`, state: skill.saved ? "saved" : skill.bundled ? "bundled" : "available" }))
        : selector === "plugin_candidates" ? pluginCandidates.map((plugin) => ({ label: plugin.id, value: plugin.manifest_path, description: `${plugin.version ? `v${plugin.version} · ` : ""}${plugin.tools.length} tools · ${plugin.commands.length} commands${plugin.build_required ? " · build required" : ""}`, state: plugin.build_required ? "review only" : "candidate" }))
          : selector === "plugins" ? [{ label: "Add plugin…", value: "__add_plugin__", description: "Discover from a local folder, GitHub repository, or URL", state: "add" }, ...plugins.map((plugin) => ({ label: plugin.id, value: plugin.id, description: plugin.error ? `Error · ${plugin.error}` : `${plugin.manifest_path ?? "manifest unavailable"}${plugin.tools?.length ? ` · ${plugin.tools.length} tools` : ""}${plugin.commands?.length ? ` · ${plugin.commands.length} commands` : ""}`, state: plugin.enabled ? "enabled" : "disabled" }))]
          : selector === "mcp" ? mcpServers.map((server) => ({ label: server.id, value: server.id, description: server.url
            ? `${server.url} · ${server.auth_mode ?? "anonymous"} · ${server.auth_status ?? "configured"}${server.credential_env?.length ? ` · env ${server.credential_env.join(", ")}` : ""}`
            : `${server.command} · ${server.arguments_count} args · env ${server.environment_keys.join(", ") || "none"}${server.working_directory ? ` · cwd ${server.working_directory}` : ""}`, state: server.auth_status ?? "configured" }))
            : modelTools.map((tool) => ({ label: tool.name, value: tool.name, description: tool.description, state: tool.source ?? "" }))
  const selectorPageSize = selector === "history" ? Math.max(3, Math.min(6, Math.floor((renderer.height - 20) / 2))) : selector === "skills"
    ? Math.max(3, Math.min(7, Math.floor((renderer.height - 12) / 3)))
    : selector === "plugins" || selector === "plugin_candidates" || selector === "mcp" || selector === "tools" || selector === "providers" || selector === "provider_models" || selector === "extension_commands" ? Math.max(4, Math.min(10, Math.floor((renderer.height - 12) / 2))) : 8
  const selectorWindowStart = Math.max(0, Math.min(selectionIndex - Math.floor(selectorPageSize / 2), selectedOptions.length - selectorPageSize))
  const skillOptionCount = selector === "skills" ? skillsView === "results" ? skillSearchResults.length : skillsView === "candidates" ? skillCandidates.length : skillsView === "installed" ? skillInstallations.length : skillsView === "review" ? 0 : skills.length : 0
  const availableSlashCommands: SlashCommand[] = [...slashCommands, ...extensionCommands.filter((item) => item.enabled && !item.error).map((item) => ({ name: item.name, description: item.description || `Plugin command · ${item.extension_id}`, action: "plugin_commands" as const }))]
  const filteredCommands = availableSlashCommands.filter((item) => item.name.startsWith(draft.trim().split(/\s/)[0] || "/"))
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
  const activityTime = activityStartedAt !== null ? ` · ${shortTime(clock - activityStartedAt)} total` : maintenance ? ` · ${shortTime(clock - maintenance.startedAt)} total` : pluginCommandRun ? ` · ${shortTime(clock - pluginCommandRun.startedAt)} total` : ""
  const spinner = ["◒", "◐", "◓", "◑"][Math.floor(clock / 180) % 4]!
  const activityActive = busy || Boolean(maintenance) || Boolean(pluginCommandRun) || reloadPending
  const cacheLabel = usage?.available && usage.cachedInput !== undefined ? `cache ${usage.cachedInput.toLocaleString()}` : "cache —"
  const transcriptOmitted = entries.some((entry) => entry.id === OMITTED_TRANSCRIPT_ENTRY.id)
  const welcomeVisible = (entries.length === 0 && connected && !newSessionPending) || (entries.length === 1 && entries[0]?.id === 0)
  const skillsEmptyLabel = skillOperation || (skillsView === "installed" ? "No managed skills installed · use /skills search QUERY" : skillsView === "results" ? "No skills matched that search." : skillsView === "candidates" ? "No skill manifests found in this source." : "No skills available")

  return (
    <box style={{ flexDirection: "column", width: "100%", height: "100%", minHeight: 0, flexGrow: 1, backgroundColor: palette.bg, paddingLeft: 2, paddingRight: 2 }}>
      <box style={{ flexDirection: "row", height: 1 }}>
        <text selectable={false} fg={palette.text} content="pk" />
      </box>
      <box style={{ flexDirection: "row", height: 1, flexShrink: 0 }}>
        <text selectable={false} fg={palette.muted} content={`${shortPath(cwd, 42)}  ·  ${sessionId ? `session ${sessionId.slice(0, 8)}` : "new session"}`} />
      </box>
      <scrollbox id="transcript" stickyScroll stickyStart="bottom" style={{ flexGrow: 1, minHeight: 0, height: 0, paddingTop: 0, paddingBottom: 0 }}>
        {welcomeVisible
          ? <WelcomeEntry onAction={(command) => { runSlashCommand(command); clearComposer(true) }} />
          : groupTranscript(entries.filter((entry) => entry.id !== 0)).map((item) => item.kind === "entry"
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
          <text selectable={false} fg={releaseUpdateAvailable ? palette.amber : copyNotice ? palette.accent : palette.dim} content={`${copyNotice ? `${copyNotice}  ·  ` : ""}${activityActive ? `${spinner} ` : ""}${activityLabel}${phaseTime}${activityTime} · ${cacheLabel}${releaseUpdateAvailable ? " · Update ready · /reload" : ""}${transcriptOmitted ? " · earlier activity omitted" : historyHasEarlier ? " · /history older" : ""}`} />
        </box>
        <box style={{ border: true, borderColor: palette.line, backgroundColor: palette.panel, paddingLeft: 1, paddingRight: 1, minHeight: 3, maxHeight: 5, flexShrink: 0 }}>
          <textarea id="composer" ref={textarea} focused={!selector && !sessionManagerOpen && !mcpManagerOpen && !pluginSourceModalOpen} placeholder={question ? "Type an answer, or choose an option above…" : "Ask pk to inspect, explain, or change this workspace…"} onContentChange={() => setDraft(textarea.current?.plainText ?? "")} onSubmit={sendPrompt} keyBindings={[{ name: "return", action: "submit" }, { name: "return", shift: true, action: "newline" }, { name: "kpenter", action: "submit" }, { name: "kpenter", shift: true, action: "newline" }, { name: "j", ctrl: true, action: "newline" }]} />
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
      <text selectable={false} fg={palette.dim} content={`${question ? "Enter answer" : activeTaskId || busy && steeringEnabled ? "Enter steer" : busy ? "Esc stop" : "Enter send"}  ·  ^J newline  ·  ^P menu  ·  ⌘V paste files  ·  ${entries.some((entry) => entry.role === "tool") ? "^O tool details  ·  " : ""}^D detach`} />
          <text selectable={false} fg={palette.muted} content={`${model}  ·  ${effort}`} />
        </box>
      </box>
      {selector && !mcpManagerOpen && <box style={{ position: "absolute", left: selector === "skills" || selector === "plugins" || selector === "plugin_candidates" || selector === "mcp" || selector === "tools" || selector === "providers" || selector === "provider_models" || selector === "extension_commands" || selector === "history" || selector === "usage" ? "8%" : "25%", right: selector === "skills" || selector === "plugins" || selector === "plugin_candidates" || selector === "mcp" || selector === "tools" || selector === "providers" || selector === "provider_models" || selector === "extension_commands" || selector === "history" || selector === "usage" ? "8%" : "25%", top: selector === "skills" || selector === "plugins" || selector === "plugin_candidates" || selector === "mcp" || selector === "tools" || selector === "providers" || selector === "provider_models" || selector === "extension_commands" || selector === "history" || selector === "usage" ? "10%" : "25%", border: true, borderColor: palette.line, backgroundColor: palette.raised, padding: 2, flexDirection: "column" }}>
        <text fg={palette.text} content={selector === "usage" ? "Session token usage · provider-reported" : selector === "model" ? "Select model" : selector === "effort" ? "Reasoning effort" : selector === "tasks" ? "Saved sessions" : selector === "skills" ? skillsView === "installed" ? "Installed skills" : skillsView === "results" ? "skills.sh search results" : skillsView === "candidates" ? "Review a skill source" : skillsView === "review" ? `Review ${skillReview?.name ?? "skill"}` : "Available skills" : selector === "plugins" ? "Plugins · Add or manage" : selector === "plugin_candidates" ? pluginCandidateReview ? `Review ${pluginCandidateReview.id}` : "Review plugin source · no plugin starts while browsing" : selector === "image" ? "Image generation · opt-in" : selector === "mcp" ? "MCP servers · safe configuration summary" : selector === "providers" ? "Providers · credentials redacted" : selector === "provider_models" ? "Discovered provider models · informational" : selector === "extension_commands" ? "Namespaced plugin commands · no worker starts while browsing" : selector === "history" ? `Saved conversation · ${historyRequestMode} page` : "Model-visible tools"} />
        {selector === "usage" && <box style={{ flexDirection: "column", gap: 1, paddingTop: 1 }}>
          {sessionUsageLoading ? <text fg={palette.accent} content="Loading recorded usage…" />
            : sessionUsageError ? <text fg={palette.amber} content={sessionUsageError} />
              : sessionUsage ? <>
                <text fg={palette.muted} content={`Session ${sessionUsage.sessionId.slice(0, 8)} · ${sessionUsage.responseCount} recorded response${sessionUsage.responseCount === 1 ? "" : "s"}`} />
                {([[
                  "Input tokens", sessionUsage.inputTokens, sessionUsage.coverage.input,
                ], ["Output tokens", sessionUsage.outputTokens, sessionUsage.coverage.output], ["Cached input", sessionUsage.cachedInputTokens, sessionUsage.coverage.cachedInput], ["Uncached input", sessionUsage.uncachedInputTokens, sessionUsage.coverage.uncachedInput]] as Array<[string, number | undefined, number]>).map(([label, value, covered]) => {
                  const amount = value === undefined ? "Unavailable" : value.toLocaleString()
                  const coverageLabel = sessionUsage.responseCount === 0 ? "no responses" : `${covered}/${sessionUsage.responseCount} responses`
                  return <box key={label} style={{ flexDirection: "row", justifyContent: "space-between" }}>
                    <text fg={palette.text} content={label} />
                    <text fg={value === undefined ? palette.dim : palette.accent} content={`${amount} · ${coverageLabel}`} />
                  </box>
                })}
                <text fg={palette.dim} content="Some totals may cover only responses where the provider reported them. No cost estimate." />
              </>
              : !sessionIdentity.current ? <text fg={palette.muted} content="No session yet. Send a prompt first to create session usage." />
                : <text fg={palette.muted} content="No usage report loaded." />}
          <box onMouseDown={(event) => leftMouseDown(event, () => { cancelSessionUsageRequest(); setSelector(null); textarea.current?.focus() })} style={{ alignSelf: "flex-start", backgroundColor: palette.panel, paddingLeft: 1, paddingRight: 1, height: 1 }}><text fg={palette.accent} content="Close · Esc" /></box>
        </box>}
        {selector === "skills" && <text fg={skillNotice ? palette.accent : palette.dim} content={skillNotice || skillOperation || (skillsView === "review" ? "Review the source before installing · affects new sessions" : skillsView === "installed" ? "Enter inserts its instruction · x removes selected · /skills search QUERY" : skillsView === "results" ? "↑↓ select source · Enter browse skills in source · /skills search QUERY" : skillsView === "candidates" ? "↑↓ select · Enter review details before install · Esc returns to search results" : "Catalog snapshot · /skills search QUERY · /skills installed · /skills available")} />}
        {selector === "plugin_candidates" && <text fg={pluginSourceOperation ? palette.accent : palette.dim} content={pluginSourceOperation || (pluginCandidateReview ? `${pluginCandidateReview.tools.length} tools · ${pluginCandidateReview.commands.length} commands · source reviewed before install` : `Source · ${pluginCandidateSource}${pluginCandidateRevision ? ` · revision ${pluginCandidateRevision.slice(0, 12)}` : ""} · review a candidate before installation`)} />}
        {selector === "history" && <text fg={palette.dim} content={historyLoading ? "Loading saved conversation…" : `Saved user, assistant, and tool entries · before #${historyBeforeSequence}`} />}
        {selector === "mcp" && <text fg={palette.dim} content={`${mcpTools.length} MCP tool${mcpTools.length === 1 ? "" : "s"} in ${mcpSavedTools ? "saved session snapshot" : "current catalog"} · no servers are started by listing`} />}
        {selector === "providers" && <text fg={palette.dim} content="Enter selects for this session · /provider use ID persists only through pk provider use ID" />}
        {selector === "image" && <text fg={imageConfigPending ? palette.accent : palette.dim} content={imageConfigPending ? "Saving ImageGen preference…" : "Uses a separate Astra image worker with your ChatGPT login. Your chat model stays unchanged. Start a new session with /new to apply."} />}
        {selector === "provider_models" && <text fg={palette.dim} content={providerModelsLoading ? "Contacting provider model catalog…" : "Model discovery is read-only · use /provider use ID before the first prompt"} />}
        {selector === "tools" && <text fg={palette.dim} content={modelToolsLoading ? "Loading model-visible tools…" : modelToolsPreview ? modelToolsNotice || "Preview of the core tools available before the first prompt" : modelToolsSaved ? "Saved snapshot for this session" : modelToolsInitialized ? "Current available tool registry" : "The session tool catalog is not initialized until its first prompt."} />}
        <box style={{ height: 1 }} />
        {selector === "skills" && skillsView === "review" && skillReview && <box style={{ flexDirection: "column", border: ["top"], borderColor: palette.line, paddingTop: 1, gap: 1 }}>
          <text fg={palette.text} content={skillReview.description || "No description provided by the source."} />
          <text fg={palette.dim} content={`Source · ${compactSkillSource(skillReview.source, skillReview.path)}`} />
          <box onMouseDown={(event) => leftMouseDown(event, installReviewedSkill)} style={{ backgroundColor: palette.panel, paddingLeft: 1, paddingRight: 1, height: 1 }}>
            <text fg={palette.accent} content="Install this skill · new sessions only" />
          </box>
        </box>}
        {selectedOptions.length === 0 && selector !== "usage" && !(selector === "skills" && skillsView === "review") && !(selector === "plugin_candidates" && pluginCandidateReview) && <text fg={palette.muted} content={selector === "skills" ? skillsEmptyLabel : selector === "plugins" ? "No plugins installed" : selector === "plugin_candidates" ? pluginSourceOperation || "No candidates to review" : selector === "mcp" ? "No MCP servers configured" : selector === "tools" ? modelToolsPreview ? "No preview tools available" : modelToolsInitialized ? "No model tools available" : "Not initialized yet" : selector === "providers" ? "No providers configured" : selector === "provider_models" ? providerModelsLoading ? "Loading models…" : "No models returned" : selector === "history" ? "No earlier saved entries" : "Nothing to show"} />}
        {selector === "plugin_candidates" && pluginCandidateReview && <box style={{ flexDirection: "column", border: ["top"], borderColor: palette.line, paddingTop: 1, gap: 1 }}>
          <text fg={palette.text} content={`${pluginCandidateReview.version ? `Version ${pluginCandidateReview.version} · ` : ""}${pluginCandidateReview.tools.length} tools · ${pluginCandidateReview.commands.length} commands`} />
          {pluginCandidateReview.tools.length > 0 && <text fg={palette.dim} content={`Tools · ${pluginCandidateReview.tools.slice(0, 6).join(", ")}${pluginCandidateReview.tools.length > 6 ? `, +${pluginCandidateReview.tools.length - 6}` : ""}`} />}
          {pluginCandidateReview.commands.length > 0 && <text fg={palette.dim} content={`Commands · ${pluginCandidateReview.commands.slice(0, 6).join(", ")}${pluginCandidateReview.commands.length > 6 ? `, +${pluginCandidateReview.commands.length - 6}` : ""}`} />}
          {pluginCandidateReview.build_required && <text fg={palette.amber} content="This candidate requires a build and cannot be installed from this picker yet." />}
          <box onMouseDown={(event) => leftMouseDown(event, installReviewedPlugin)} style={{ backgroundColor: palette.panel, paddingLeft: 1, paddingRight: 1, height: 1 }}>
            <text fg={pluginCandidateReview.build_required || pluginSourceOperation ? palette.dim : palette.accent} content={pluginCandidateReview.build_required ? "Install unavailable · build required" : "Install and enable · affects new sessions"} />
          </box>
        </box>}
        {selectedOptions.slice(selectorWindowStart, selectorWindowStart + selectorPageSize).map((option, localIndex) => {
          const index = selectorWindowStart + localIndex
          return <box key={option.value} onMouseOver={() => setSelectionIndex(index)} onMouseDown={(event) => leftMouseDown(event, () => activateSelectorOption(index))} style={{ flexDirection: "column", backgroundColor: index === selectionIndex ? palette.panel : palette.raised, paddingLeft: 1, paddingRight: 1 }}>
            <box style={{ flexDirection: "row", gap: 1, height: 1 }}>
              <text fg={index === selectionIndex ? palette.accent : palette.muted} content={index === selectionIndex ? "›" : " "} />
              <text fg={index === selectionIndex ? palette.text : palette.muted} content={option.label} />
              <text fg={(selector === "plugins" && (option.state === "enabled" || option.state === "add")) || (selector === "mcp" && option.state === "configured") || ((selector === "providers") && (option.state === "default" || option.state === "selected")) ? palette.accent : palette.dim} content={option.state ? `· ${option.state}` : ""} />
            </box>
            {option.description && <text fg={palette.dim} content={option.description} />}
          </box>
        })}
        {selector === "history" && historyDetailIndex !== null && historyEntries[historyDetailIndex] && <box style={{ border: ["top"], borderColor: palette.line, paddingTop: 1, maxHeight: 8, flexShrink: 0 }}><text fg={palette.text} content={savedHistoryPreview(historyEntries[historyDetailIndex]!).slice(0, 1200)} /></box>}
        <box style={{ height: 1 }} />
        {selector === "skills" && skillsView === "installed" && selectedOptions[selectionIndex] && <box onMouseDown={(event) => leftMouseDown(event, () => removeInstalledSkill(selectionIndex))} style={{ backgroundColor: palette.panel, paddingLeft: 1, height: 1 }}><text fg={palette.amber} content={`Remove ${selectedOptions[selectionIndex]!.label} · x`} /></box>}
        <text fg={palette.dim} content={selector === "usage" ? "Read-only provider token totals · Esc close" : selector === "history" ? "↑↓ browse · Enter preview or load earlier · /history older · Esc close" : selector === "skills" ? skillsView === "review" ? "i or click to install · Esc back to results" : skillsView === "installed" ? "↑↓ choose · Enter use · x or click remove · Esc close" : skillsView === "available" ? "↑↓ move · Enter insert instruction · Esc close" : "↑↓ move · Enter inspect · Esc close" : selector === "plugins" ? "↑↓ move · Enter open or toggle · Add plugin… accepts a repo URL · new session required" : selector === "plugin_candidates" ? pluginCandidateReview ? "i or click to install · Esc back to candidates" : "↑↓ choose · Enter review · Esc close" : selector === "image" ? "Enter or click to toggle · disabled by default · /new activates changes · Esc close" : selector === "mcp" ? "↑↓ move · Enter details · /mcp add|remove · new session required · Esc close" : selector === "tools" ? "↑↓ move · Enter details · registry snapshot · Esc close" : selector === "providers" ? "↑↓ move · Enter select · /provider models ID · Esc close" : selector === "provider_models" ? "↑↓ browse · Esc close" : selector === "extension_commands" ? "↑↓ move · Enter insert into composer · Esc close" : "↑↓ move  ·  Enter choose  ·  Esc close"} />
      </box>}
      {pluginSourceModalOpen && <box style={{ position: "absolute", left: "18%", right: "18%", top: "30%", border: true, borderColor: palette.accent, backgroundColor: palette.raised, padding: 2, flexDirection: "column" }}>
        <text fg={palette.text} content="Add plugin from source" />
        <text fg={palette.muted} content="Enter a local repository folder, owner/repo, or GitHub URL. Nothing installs until you review a candidate." />
        <box style={{ border: true, borderColor: palette.line, backgroundColor: palette.panel, paddingLeft: 1, paddingRight: 1, minHeight: 3, maxHeight: 4, flexShrink: 0 }}>
          <textarea id="plugin-source" ref={pluginSourceTextarea} focused={pluginSourceModalOpen} placeholder="owner/repo or https://github.com/owner/repo" onContentChange={() => setPluginSourceDraft(pluginSourceTextarea.current?.plainText ?? "")} onSubmit={() => beginPluginDiscovery(pluginSourceTextarea.current?.plainText ?? pluginSourceDraft)} keyBindings={[{ name: "return", action: "submit" }, { name: "return", shift: true, action: "newline" }, { name: "kpenter", action: "submit" }, { name: "kpenter", shift: true, action: "newline" }]} />
        </box>
        <box style={{ flexDirection: "row", gap: 2, paddingTop: 1 }}>
          <box onMouseDown={(event) => leftMouseDown(event, () => beginPluginDiscovery(pluginSourceTextarea.current?.plainText ?? pluginSourceDraft))} style={{ backgroundColor: palette.panel, paddingLeft: 1, paddingRight: 1, height: 1 }}><text fg={palette.accent} content="Discover candidates · Enter" /></box>
          <box onMouseDown={(event) => leftMouseDown(event, () => { setPluginSourceModalOpen(false); textarea.current?.focus() })} style={{ backgroundColor: palette.panel, paddingLeft: 1, paddingRight: 1, height: 1 }}><text fg={palette.muted} content="Cancel · Esc" /></box>
        </box>
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
      <SessionManager
        open={sessionManagerOpen}
        onClose={() => setSessionManagerOpen(false)}
        send={(type, payload) => transport.send(type as any, payload)}
        event={sessionManagerEvent}
        onLoad={(session: ManagedSession) => {
          if (busy || turnActive.current || question || maintenance) {
            addEntry("system", "Finish the active turn before opening a saved session.")
            return
          }
          setSessionManagerOpen(false)
          updateSessionIdentity("")
          historySessionID.current = ""
          pendingHistoryRequest.current = ""
          setHistoryEntries([])
          setHistoryHasEarlier(false)
          setHistoryBeforeSequence(0)
          transport.send("attach", { session_id: session.id })
          setEntries([{ id: entryId.current++, role: "system", text: `Attaching to session ${session.id.slice(0, 8)}…` }])
        }}
      />
      <MCPManager
        open={mcpManagerOpen}
        onClose={() => setMcpManagerOpen(false)}
        send={(type, payload) => transport.send(type as any, payload)}
        event={sessionManagerEvent}
      />
    </box>
  )
}

const ToolTranscriptGroup = memo(function ToolTranscriptGroupView({ entries, clock, expanded, onToggle }: { entries: Entry[]; clock: number; expanded: boolean; onToggle: () => void }) {
  const renderer = useRenderer()
  const first = entries[0]!
  const failed = entries.some((entry) => entry.toolState === "failed")
  const interrupted = entries.some((entry) => entry.toolState === "interrupted")
  const running = entries.some((entry) => !entry.historical && !["completed", "complete", "failed", "canceled", "cancelled", "succeeded", "interrupted"].includes((entry.toolState ?? "").toLowerCase()))
  const historicallyUnfinished = entries.some((entry) => entry.historical && ["running", "working"].includes((entry.toolState ?? "").toLowerCase()))
  const hasElapsed = entries.some((entry) => entry.elapsedMs !== undefined || entry.startedAt !== undefined)
  const elapsed = entries.reduce((total, entry) => total + (entry.elapsedMs ?? (entry.startedAt ? Math.max(0, clock - entry.startedAt) : 0)), 0)
  const progressText = [...entries].reverse().find((entry) => entry.progressText)?.progressText
  const summary = entries.length > 1
    ? `${first.toolName ?? "tool"} · Ran ${entries.length} ${first.toolName === "Bash" ? "commands" : "calls"}`
    : first.historySummary ?? `${first.toolName ?? "tool"} · ${progressText ? "working" : first.commandPreview || "operation"}`
  const summaryWithProgress = progressText
    ? entries.length > 1 ? `${summary} · ${progressText}` : `${first.toolName ?? "tool"} · ${progressText}`
    : summary
  const color = failed ? palette.red : interrupted || historicallyUnfinished ? palette.amber : running ? palette.accent : palette.green
  const available = Math.max(24, Math.min(112, renderer.width - 24))
  const compactSummary = summaryWithProgress.length > available ? `${summaryWithProgress.slice(0, available - 1)}…` : summaryWithProgress
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
      <text fg={color} content={running ? "◌" : failed ? "!" : interrupted ? "↯" : historicallyUnfinished ? "·" : "✓"} />
      <text fg={palette.text} content={compactSummary} />
      <text fg={palette.dim} content={hasElapsed ? shortTime(elapsed) : ""} />
      <text fg={palette.dim} content={expanded ? "▾" : "›"} />
    </box>
    {expanded && entries.map((entry) => <box key={entry.callId} style={{ flexDirection: "column", paddingLeft: 2, paddingBottom: 1 }}>
      {entry.commandPreview && <text fg={palette.muted} content={`$ ${entry.commandPreview}`} />}
      {entry.progressText && <text fg={palette.accent} content={`Progress · ${entry.progressText}`} />}
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

function WelcomeEntry({ onAction }: { onAction: (command: string) => void }) {
  const actions = ["/skills", "/plugins", "/mcp", "/provider"]
  const wordmark = ["       _    ", " _ __ | | __", "| '_ \\| |/ /", "| |_) |   < ", "| .__/|_|\\_\\", "|_|         "]
  return <box style={{ flexDirection: "column", paddingLeft: 2, paddingTop: 1, paddingBottom: 1 }}>
    {wordmark.map((line, index) => <text key={index} fg={palette.accent} content={line} />)}
    <text fg={palette.muted} content="A quiet workspace for your next task." />
    <box style={{ flexDirection: "row", gap: 2, paddingTop: 1 }}>
      {actions.map((command) => <box key={command} onMouseDown={(event) => {
        if (event.button !== 0) return
        event.preventDefault()
        event.stopPropagation()
        onAction(command)
      }} style={{ backgroundColor: palette.raised, paddingLeft: 1, paddingRight: 1, height: 1 }}>
        <text fg={palette.accent} content={command} />
      </box>)}
    </box>
  </box>
}

const TranscriptEntry = memo(function TranscriptEntryView({ entry, clock }: { entry: Entry; clock: number }) {
  const renderer = useRenderer()
  if (entry.role === "system") return <box style={{ paddingLeft: 2, paddingBottom: 1 }}><text fg={palette.dim} content={entry.text} /></box>
  if (entry.role === "tool") return <box style={{ flexDirection: "column", marginLeft: 2, marginBottom: 1, paddingLeft: 1, border: ["left"], borderColor: entry.toolState === "failed" ? palette.red : palette.accent }}>
    <box style={{ flexDirection: "row", gap: 1, height: 1 }}>
      <text fg={entry.toolState === "failed" ? palette.red : palette.accent} content={!entry.historical && (entry.toolState === "running" || entry.toolState === "working") ? "◌" : "›"} />
      <text fg={palette.text} content={`${entry.toolName ?? "tool"} · ${entry.toolState ?? "working"}`} />
      <text fg={palette.dim} content={entry.elapsedMs !== undefined ? shortTime(entry.elapsedMs) : entry.startedAt ? shortTime(clock - entry.startedAt) : ""} />
    </box>
    {entry.detail ? <text fg={palette.muted} content={entry.detail} /> : entry.text ? <text fg={palette.red} content={entry.text} /> : null}
  </box>
  const isUser = entry.role === "user"
  const markdown = hasMarkdownSyntax(entry.text)
  const delivery = entry.delivery === "queued" ? "queued · waiting for a boundary" : entry.delivery === "accepted" ? "accepted by active turn" : entry.delivery === "rejected" ? "not accepted" : ""
  return <box style={{ flexDirection: "column", width: "100%", paddingLeft: isUser ? 0 : 2, paddingBottom: 1 }}>
    <text fg={isUser ? palette.blue : palette.accent} content={isUser ? `you${delivery ? ` · ${delivery}` : ""}` : entry.speaker ?? "pk"} />
    {entry.text && (isUser || entry.provisional || !markdown
      ? <text fg={palette.text} content={entry.text} />
      : <markdown content={entry.text} syntaxStyle={markdownStyle} fg={palette.text} style={{ width: "100%", flexGrow: 1, minHeight: 1, flexShrink: 0 }} />)}
    {isUser && entry.historyAttachments?.length ? <box style={{ flexDirection: "row", gap: 1, flexWrap: "wrap", paddingTop: entry.text ? 1 : 0 }}>
      {entry.historyAttachments.slice(0, 8).map((attachment, index) => {
        const maxName = renderer.width < 90 ? 18 : 32
        const name = attachment.name.length > maxName ? `${attachment.name.slice(0, maxName - 1)}…` : attachment.name
        const kindValue = attachment.kind.toLowerCase()
        const kind = kindValue === "image" ? "Image" : kindValue === "pdf" || kindValue === "pdf_text" ? "PDF" : kindValue === "text" ? "Text" : "File"
        const pages = attachment.pagesTotal !== undefined ? ` · ${attachment.pagesExtracted ?? 0}/${attachment.pagesTotal} pages` : ""
        return <box key={`${attachment.name}-${index}`} style={{ backgroundColor: palette.raised, paddingLeft: 1, paddingRight: 1, height: 1 }}>
          <text fg={palette.muted} content={`${name} · ${kind}${pages}${attachment.truncated ? " · shortened" : ""}`} />
        </box>
      })}
    </box> : null}
    {entry.speaker && entry.detail && <text fg={palette.dim} content={entry.detail} />}
    {entry.deliveryMessage && <text fg={entry.delivery === "rejected" ? palette.red : palette.dim} content={entry.deliveryMessage} />}
  </box>
}, (previous, next) => {
  if (previous.entry !== next.entry) return false
  if (previous.entry.role !== "tool") return true
  const status = (previous.entry.toolState ?? "").toLowerCase()
  const terminal = ["completed", "complete", "failed", "canceled", "cancelled", "succeeded", "interrupted"].includes(status)
  // Static transcript rows should not re-render on the activity clock. A tool
  // row needs ticks only while its elapsed duration is live.
  const elapsedIsLive = previous.entry.elapsedMs === undefined && previous.entry.startedAt !== undefined && !terminal
  return !elapsedIsLive || previous.clock === next.clock
})
