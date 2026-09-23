import { useKeyboard, useRenderer } from "@opentui/react"
import { SyntaxStyle, type TextareaRenderable } from "@opentui/core"
import { useEffect, useRef, useState } from "react"
import type { PkTransport } from "./transport"
import type { ServerEvent } from "./protocol"

type Role = "user" | "assistant" | "system" | "tool"
type Entry = { id: number; role: Role; text: string; callId?: string; toolName?: string; toolState?: string; startedAt?: number; elapsedMs?: number; detail?: string }
type ToolActivity = { id: string; name: string; state: string; startedAt: number; detail?: string }
type Model = { id: string; label: string }
type SlashCommand = { name: string; description: string; action: "model" | "effort" | "tasks" | "new" | "attach" | "detach" | "cancel" | "status" | "login" | "task" | "help" | "exit" }

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
  { name: "/task", description: "Create, attach, or control a durable task", action: "task" },
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
  heading: { fg: palette.text, bold: true },
  link: { fg: palette.blue, underline: true },
  code: { fg: palette.accent },
  quote: { fg: palette.muted },
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

function parseSlashWords(input: string): string[] {
  const words: string[] = []
  const pattern = /"((?:\\.|[^"\\])*)"|'((?:\\.|[^'\\])*)'|(\S+)/g
  for (const match of input.matchAll(pattern)) {
    words.push((match[1] ?? match[2] ?? match[3] ?? "").replace(/\\([\\"'])/g, "$1"))
  }
  return words
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
  const [busy, setBusy] = useState(false)
  const [selector, setSelector] = useState<"model" | "effort" | "tasks" | null>(null)
  const [selectionIndex, setSelectionIndex] = useState(0)
  const [clock, setClock] = useState(Date.now())
  const [message, setMessage] = useState("")
  const [draft, setDraft] = useState("")
  const [slashIndex, setSlashIndex] = useState(0)
  const [tasks, setTasks] = useState<Array<{ session_id: string; updated_at?: string; title?: string }>>([])
  const [activeTaskId, setActiveTaskId] = useState("")
  const entryId = useRef(1)
  const waiting = useRef(false)
  const turnActive = useRef(false)
  const busyRef = useRef(false)
  const promptCommandId = useRef("")
  const preferenceErrors = useRef(new Map<string, () => void>())

  const addEntry = (role: Role, text: string) => {
    if (!text.trim()) return
    setEntries((previous) => [...previous, { id: entryId.current++, role, text }].slice(-300))
  }

  const trackTool = (data: Record<string, any>) => {
    const callId = String(data.call_id ?? data.id ?? `tool-${Date.now()}`)
    const name = String(data.name ?? data.tool ?? "tool")
    const rawStatus = data.state ?? data.status?.status ?? data.status
    const status = typeof rawStatus === "string" ? rawStatus : "running"
    const startedAt = Date.now() - (Number.isFinite(data.elapsed_ms) ? Math.max(0, Number(data.elapsed_ms)) : 0)
    const operation = Array.isArray(data.operations) ? data.operations.map((item: any) => {
      const output = preview(item?.output_excerpt)
      const error = preview(item?.error_excerpt)
      return [item?.type ?? "operation", item?.state, error || output, item?.exit_code === undefined ? "" : `exit ${item.exit_code}`].filter(Boolean).join(" · ")
    }).filter(Boolean).join("\n") : ""
    const detail = [data.command_preview, data.arguments_preview, operation].map((item) => preview(item, 260)).filter(Boolean).join("\n")
    const terminal = ["completed", "complete", "failed", "canceled", "cancelled", "succeeded"].includes(status.toLowerCase())
    const error = preview(data.status?.error ?? data.error)
    const displayState = error ? "failed" : terminal ? status : status === "awaiting" ? "working" : status
    setEntries((current) => {
      const previous = current.find((entry) => entry.callId === callId)
      const next: Entry = {
        id: previous?.id ?? entryId.current++, role: "tool", text: error || detail,
        callId, toolName: name, toolState: displayState,
        startedAt: previous?.startedAt ?? startedAt,
        elapsedMs: Number.isFinite(data.elapsed_ms) ? Number(data.elapsed_ms) : undefined,
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
  useEffect(() => { setSlashIndex(0) }, [draft])
  useEffect(() => { busyRef.current = busy }, [busy])

  useEffect(() => {
    void transport.start({ workspace, model: "", effort: "", sessionId: initialSession })
  }, [transport, workspace, initialSession])

  const handleEvent = useRef<(event: ServerEvent) => void>(() => {})
  handleEvent.current = (event) => {
    const data = event.payload ?? {}
    switch (event.type) {
      case "ready":
        setConnected(true)
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
      case "turn_started":
        setBusy(true)
        setTools([])
        turnActive.current = true
        break
      case "assistant":
        if (data.text) addEntry("assistant", String(data.text))
        break
      case "tool": {
        trackTool(data)
        break
      }
      case "tool_call": {
        trackTool(data)
        break
      }
      case "turn_finished":
        setBusy(false)
        setTools([])
        waiting.current = false
        turnActive.current = false
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
      case "task_created":
        addEntry("system", `Task started in the background · ${String(data.task_id ?? data.id ?? "task")}`)
        break
      case "task_attached":
        setActiveTaskId(String(data.task_id ?? data.id ?? ""))
        if (data.session_id) setSessionId(String(data.session_id))
        if (data.workspace) setMessage(String(data.workspace))
        setBusy(["running", "queued", "awaiting", "canceling"].includes(String(data.status)))
        addEntry("system", `Following task ${String(data.task_id ?? data.id ?? "")}`)
        break
      case "task_resumed":
        setActiveTaskId(String(data.task_id ?? data.id ?? ""))
        setBusy(true)
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
        if (data.text) addEntry("assistant", String(data.text))
        addEntry("system", `Task ${String(data.status ?? "finished")}`)
        setActiveTaskId("")
        if (data.session_id) setSessionId(String(data.session_id))
        setBusy(false)
        waiting.current = false
        break
      case "detached":
        addEntry("system", `Detached from session ${sessionId || ""}`)
        setSessionId("")
        setBusy(false)
        setTools([])
        void transport.close().finally(() => renderer.destroy())
        break
      case "error":
        addEntry("system", String(data.message ?? "The agent encountered an error."))
        if (event.id && preferenceErrors.current.has(event.id)) {
          preferenceErrors.current.get(event.id)?.()
          preferenceErrors.current.delete(event.id)
        }
        const isPromptError = Boolean(event.id && promptCommandId.current && event.id === promptCommandId.current)
          || data.command_type === "prompt"
          || data.request_type === "prompt"
        if (isPromptError) {
          waiting.current = false
          turnActive.current = false
          promptCommandId.current = ""
          setBusy(false)
          setTools([])
        } else if (!turnActive.current && !activeTaskId && !waiting.current) {
          setBusy(false)
          setTools([])
        }
        break
      case "log": {
        const line = String(data.text ?? "")
        if (!/^\[pk\] (Running|Finished) /.test(line)) addEntry("system", line)
        break
      }
      case "rpc_closed":
        setConnected(false)
        break
    }
  }

  // Transport hands events through a stable callback so the view remains the only state owner.
  useEffect(() => {
    transport.setEventHandler((event: ServerEvent) => handleEvent.current(event))
  }, [transport])

  const sendPrompt = () => {
    const value = textarea.current?.plainText ?? draft
    const text = value.trim()
    if (!text || !connected || (waiting.current && !activeTaskId)) return
    if (text.startsWith("/")) {
      runSlashCommand(text)
      textarea.current!.initialValue = ""
      setDraft("")
      return
    }
    addEntry("user", text)
    textarea.current!.initialValue = ""
    setDraft("")
    if (activeTaskId) {
      transport.send("send_input" as any, { task_id: activeTaskId, text })
      return
    }
    waiting.current = true
    setBusy(true)
    promptCommandId.current = transport.send("prompt", { text }) ?? ""
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
      case "cancel": if (busy) transport.send("cancel"); else addEntry("system", "No turn is running."); break
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
      case "help": addEntry("system", "Enter sends · Shift-Enter or Ctrl-J adds a line · Esc stops · Ctrl-P opens commands · /task new [--workspace PATH] PROMPT · /tasks · /task attach ID · /task resume ID · /task cancel ID · /new · /attach ID · /detach · /status · /login · /help · /exit"); break
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

  useKeyboard((key) => {
    const isEscape = key.name === "escape" || key.name === "esc"
    if (selector) {
      const count = selector === "model" ? models.length : selector === "effort" ? efforts.length : tasks.length
      if (isEscape) { setSelector(null); textarea.current?.focus(); return }
      if (key.name === "up") { setSelectionIndex((index) => (index - 1 + count) % count); return }
      if (key.name === "down") { setSelectionIndex((index) => (index + 1) % count); return }
      if (key.name === "return") {
        if (selector === "tasks") {
          const task: any = tasks[selectionIndex]
          if (task?.task_id) transport.send("task_attach", { task_id: task.task_id })
          else if (task?.session_id) runSlashCommand(`/attach ${task.session_id}`)
          setSelector(null)
          textarea.current?.focus()
        } else commitSelector(selectionIndex)
        key.preventDefault()
        return
      }
    }
    if (key.ctrl && key.name === "p") {
      textarea.current!.initialValue = "/"
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
        if (selected && selected.action !== "task" && selected.action !== "attach") runSlashCommand(selected.name)
        else if (selected) { textarea.current!.initialValue = `${selected.name} `; setDraft(`${selected.name} `) }
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
        if (selected) { textarea.current!.initialValue = `${selected.name} `; setDraft(`${selected.name} `) }
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
      textarea.current!.initialValue = ""
      setDraft("")
      return
    }
    if (isEscape && busyRef.current) {
      if (activeTaskId) transport.send("task_cancel", { task_id: activeTaskId })
      else transport.send("cancel")
      addEntry("system", "Stopping the current turn…")
      return
    }
    if (key.ctrl && key.name === "d") {
      if (activeTaskId && !busy) transport.send("detach", { task_id: activeTaskId })
      else if (sessionId && !busy) transport.send("detach", { session_id: sessionId })
      else if (busyRef.current) addEntry("system", "A turn is still running. Use Esc to stop it before detaching.")
      else void transport.close().finally(() => renderer.destroy())
      return
    }
    if (key.ctrl && key.name === "c" && !busy) {
      void transport.close().finally(() => renderer.destroy())
    }
  })

  const selectedOptions = selector === "model" ? models.map((item) => ({ label: item.label, value: item.id, description: item.id })) : selector === "effort" ? efforts.map((item) => ({ label: `${item[0]!.toUpperCase()}${item.slice(1)} reasoning`, value: item, description: "" })) : tasks.map((task: any) => ({ label: task.title || task.prompt || task.session_id || task.task_id, value: task.session_id || task.task_id, description: `${task.kind ?? "task"} · ${task.status ?? task.updated_at ?? "saved"}` }))
  const filteredCommands = slashCommands.filter((item) => item.name.startsWith(draft.trim().split(/\s/)[0] || "/"))
  const slashWindowStart = Math.max(0, Math.min(slashIndex - 5, filteredCommands.length - 6))
  const cwd = message || workspace

  return (
    <box style={{ flexDirection: "column", width: "100%", height: "100%", minHeight: 0, flexGrow: 1, backgroundColor: palette.bg, paddingLeft: 2, paddingRight: 2 }}>
      <box style={{ flexDirection: "row", justifyContent: "space-between", height: 1 }}>
        <text fg={palette.text} content="pk  /  terminal agent" />
        <text fg={connected ? palette.green : palette.amber} content={connected ? "● connected" : "○ connecting…"} />
      </box>
      {!sessionId && entries.length <= 1 && <box style={{ flexDirection: "column", marginTop: 1, marginBottom: 1, flexShrink: 0 }}>
        <ascii-font text="PK" font="block" color={palette.accent} />
      </box>}
      <box style={{ flexDirection: "row", justifyContent: "space-between", height: 1, flexShrink: 0 }}>
        <text fg={palette.muted} content={`${shortPath(cwd, 42)}  ·  ${sessionId ? `session ${sessionId.slice(0, 8)}` : "new session"}`} />
        <text fg={palette.dim} content={usage?.available && usage.cachedInput !== undefined ? `cache ${usage.cachedInput.toLocaleString()}` : "cache —"} />
      </box>
      <scrollbox style={{ flexGrow: 1, minHeight: 0, flexDirection: "column", paddingTop: 0, paddingBottom: 0 }} focused={!selector}>
        {entries.map((entry) => <TranscriptEntry key={entry.id} entry={entry} clock={clock} />)}
        {busy && tools.length === 0 && <box style={{ flexDirection: "row", gap: 1, paddingLeft: 2, height: 1 }}><text fg={palette.accent} content="◌" /><text fg={palette.muted} content="Thinking…" /></box>}
      </scrollbox>
      <box style={{ border: ["top"], borderColor: palette.line, paddingTop: 0, flexShrink: 0 }}>
        <box style={{ flexDirection: "row", gap: 1, height: 1 }}>
          <text fg={palette.accent} content="›" />
          <text fg={palette.muted} content="Message" />
        </box>
        <box style={{ border: true, borderColor: palette.line, backgroundColor: palette.panel, paddingLeft: 1, paddingRight: 1, minHeight: 3, maxHeight: 5, flexShrink: 0 }}>
          <textarea ref={textarea} focused={!selector} placeholder="Ask pk to inspect, explain, or change this workspace…" onContentChange={() => setDraft(textarea.current?.plainText ?? "")} onSubmit={sendPrompt} keyBindings={[{ name: "return", action: "submit" }, { name: "return", shift: true, action: "newline" }, { name: "kpenter", action: "submit" }, { name: "kpenter", shift: true, action: "newline" }, { name: "j", ctrl: true, action: "newline" }]} />
        </box>
        {draft.startsWith("/") && filteredCommands.length > 0 && <box style={{ border: true, borderColor: palette.line, backgroundColor: palette.raised, paddingLeft: 1, paddingRight: 1, marginTop: 1, flexDirection: "column" }}>
          {filteredCommands.slice(slashWindowStart, slashWindowStart + 6).map((item, localIndex) => <box key={item.name} style={{ flexDirection: "row", gap: 2, backgroundColor: slashWindowStart + localIndex === slashIndex % filteredCommands.length ? palette.panel : palette.raised, height: 1 }}>
            <text fg={palette.accent} content={item.name} />
            <text fg={palette.muted} content={item.description} />
          </box>)}
        </box>}
        <box style={{ flexDirection: "row", justifyContent: "space-between", height: 1 }}>
          <text fg={palette.dim} content={`${activeTaskId ? "Enter steer" : "Enter send"}  ·  ^J newline  ·  ^P menu  ·  ^D detach`} />
          <text fg={palette.muted} content={`${model}  ·  ${effort}`} />
        </box>
      </box>
      {selector && <box style={{ position: "absolute", left: "25%", right: "25%", top: "25%", border: true, borderColor: palette.line, backgroundColor: palette.raised, padding: 2, flexDirection: "column" }}>
        <text fg={palette.text} content={selector === "model" ? "Select model" : selector === "effort" ? "Reasoning effort" : "Saved sessions"} />
        <box style={{ height: 1 }} />
        {selectedOptions.map((option, index) => <box key={option.value} style={{ flexDirection: "row", gap: 1, backgroundColor: index === selectionIndex ? palette.panel : palette.raised, paddingLeft: 1, height: 1 }}>
          <text fg={index === selectionIndex ? palette.accent : palette.muted} content={index === selectionIndex ? "›" : " "} />
          <text fg={index === selectionIndex ? palette.text : palette.muted} content={option.label} />
          {option.value === (selector === "model" ? model : effort) && <text fg={palette.dim} content="current" />}
        </box>)}
        <box style={{ height: 1 }} />
        <text fg={palette.dim} content="↑↓ move  ·  Enter choose  ·  Esc close" />
      </box>}
    </box>
  )
}

function TranscriptEntry({ entry, clock }: { entry: Entry; clock: number }) {
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
  return <box style={{ flexDirection: "column", paddingLeft: isUser ? 0 : 2, paddingBottom: 1 }}>
    <text fg={isUser ? palette.blue : palette.accent} content={isUser ? "you" : "pk"} />
    {isUser ? <text fg={palette.text} content={entry.text} /> : <markdown content={entry.text} syntaxStyle={markdownStyle} fg={palette.text} style={{ width: "100%", flexGrow: 1, minHeight: 1, flexShrink: 0 }} />}
  </box>
}
