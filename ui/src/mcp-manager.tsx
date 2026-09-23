import { useKeyboard, useRenderer } from "@opentui/react"
import { useEffect, useRef, useState } from "react"
import type { ServerEvent } from "./protocol"

export type MCPServer = {
  id: string
  transport: "stdio" | "http" | "streamable_http" | string
  url?: string
  auth_mode?: string
  auth_status?: string
  credential_env?: string[]
  command?: string
  arguments_count?: number
  environment_keys?: string[]
  working_directory?: string
}

export type MCPManagerProps = {
  open: boolean
  onClose: () => void
  send: (type: string, payload?: Record<string, unknown>) => string | undefined
  event?: ServerEvent
}

type FormMode = "stdio" | "http"
type AuthMode = "anonymous" | "oauth" | "bearer_env" | "header_env" | "bearer_secret" | "header_secret"
type RequestKind = "list" | "add" | "remove" | "login" | "logout"
type Pending = { kind: RequestKind; id?: string }

const colors = { panel: "#181b1e", raised: "#202428", line: "#2b3035", text: "#e5e8eb", muted: "#858e96", dim: "#5d666e", accent: "#8ab4a1", amber: "#d3ac72", red: "#d88787" }
const authModes: AuthMode[] = ["anonymous", "oauth", "bearer_env", "header_env", "bearer_secret", "header_secret"]
const maxField = 1024

function parseArgs(raw: string): string[] {
  const values: string[] = []
  const pattern = /"([^"\\]*(?:\\.[^"\\]*)*)"|'([^'\\]*(?:\\.[^'\\]*)*)'|(\S+)/g
  for (const match of raw.matchAll(pattern)) values.push((match[1] ?? match[2] ?? match[3] ?? "").replace(/\\(["'\\])/g, "$1"))
  return values
}

function authLabel(mode: AuthMode) {
  return ({ anonymous: "No auth", oauth: "OAuth", bearer_env: "Bearer · environment variable", header_env: "Custom header · environment variable", bearer_secret: "Bearer · enter credential", header_secret: "Custom header · enter credential" } as const)[mode]
}

function leftClick(event: { button: number; preventDefault: () => void; stopPropagation: () => void }, action: () => void) {
  if (event.button !== 0) return
  event.preventDefault()
  event.stopPropagation()
  action()
}

export function MCPManager({ open, onClose, send, event }: MCPManagerProps) {
  const renderer = useRenderer()
  const [servers, setServers] = useState<MCPServer[]>([])
  const [tools, setTools] = useState<Array<{ server_id: string; name: string; description: string }>>([])
  const [savedSessionTools, setSavedSessionTools] = useState(false)
  const [mode, setMode] = useState<FormMode>("stdio")
  const [auth, setAuth] = useState<AuthMode>("anonymous")
  const [id, setID] = useState("")
  const [command, setCommand] = useState("")
  const [args, setArgs] = useState("")
  const [workingDirectory, setWorkingDirectory] = useState("")
  const [url, setURL] = useState("")
  const [headerName, setHeaderName] = useState("X-API-Key")
  const [credentialReference, setCredentialReference] = useState("")
  const [secret, setSecret] = useState("")
  const [selectedIndex, setSelectedIndex] = useState(0)
  const [selectedTab, setSelectedTab] = useState<"servers" | "tools">("servers")
  const [formOpen, setFormOpen] = useState(false)
  const [fieldIndex, setFieldIndex] = useState(0)
  const [removeID, setRemoveID] = useState("")
  const [busy, setBusy] = useState(false)
  const [notice, setNotice] = useState("")
  const [formError, setFormError] = useState("")
  const pending = useRef(new Map<string, Pending>())
  const opened = useRef(false)
  const rows = selectedTab === "servers" ? servers : tools
  const visibleRows = Math.max(3, Math.min(10, Math.floor((renderer.height - 18) / 2)))
  const formOrder = mode === "stdio"
    ? [0, 1, 2, 3, 4, 5]
    : [0, 1, 2, 3, ...((auth === "header_env" || auth === "header_secret") ? [4] : []), ...((auth === "bearer_env" || auth === "header_env" || auth === "bearer_secret" || auth === "header_secret") ? [5] : []), 6]
  const saveIndex = formOrder[formOrder.length - 1]!
  const advanceField = () => {
    const current = formOrder.indexOf(fieldIndex)
    setFieldIndex(formOrder[((current < 0 ? 0 : current) + 1) % formOrder.length]!)
  }

  const request = (kind: RequestKind, type: string, payload?: Record<string, unknown>) => {
    const requestID = send(type, payload)
    if (!requestID) { setNotice("pk is disconnected; this request was not sent."); return }
    pending.current.set(requestID, { kind, id: typeof payload?.id === "string" ? payload.id : undefined })
    setBusy(true)
    setNotice(kind === "list" ? "Loading saved MCP configuration…" : kind === "add" ? "Saving server configuration…" : kind === "remove" ? "Removing server configuration…" : kind === "login" ? "Starting browser authorization…" : "Clearing local authorization…")
  }

  const refresh = () => request("list", "mcp_list")

  useEffect(() => {
    if (!open) { opened.current = false; setSecret(""); setFormOpen(false); setRemoveID(""); return }
    if (opened.current) return
    opened.current = true
    refresh()
  }, [open])

  useEffect(() => {
    if (!open || !event?.id) return
    const current = pending.current.get(event.id)
    if (!current) return
    const data = event.payload ?? {}
    if (event.type === "error") {
      pending.current.delete(event.id)
      setBusy(false)
      setNotice(String(data.message ?? "MCP operation failed."))
      return
    }
    if (event.type === "mcp_catalog" || event.type === "mcp_updated") {
      if (current.kind !== "list" && event.type !== "mcp_updated") return
      pending.current.delete(event.id)
      const nextServers = Array.isArray(data.servers) ? data.servers.filter((server: any) => server && typeof server.id === "string") as MCPServer[] : []
      setServers(nextServers)
      setTools(Array.isArray(data.tools) ? data.tools.map((tool: any) => ({ server_id: String(tool.server_id ?? ""), name: String(tool.name ?? ""), description: String(tool.description ?? "") })) : [])
      setSavedSessionTools(data.saved_session_tools === true)
      setSelectedIndex((index) => Math.max(0, Math.min(index, Math.max(0, nextServers.length - 1))))
      setBusy(false)
      setNotice(event.type === "mcp_updated" && data.next_session_only ? "Configuration saved · use /new to activate it in a session." : "")
      return
    }
    if (event.type === "mcp_auth_status") {
      const status = String(data.status ?? "")
      if (status === "authorizing") { setNotice("Browser authorization is waiting for your consent."); return }
      pending.current.delete(event.id)
      setBusy(false)
      setNotice(status === "authenticated" ? "Authorization complete." : status === "needs_login" ? "Local sign-in cleared." : status || "Authorization updated.")
      refresh()
    }
  }, [event, open])

  const clearForm = () => {
    setID(""); setCommand(""); setArgs(""); setWorkingDirectory(""); setURL(""); setHeaderName("X-API-Key"); setCredentialReference(""); setSecret(""); setAuth("anonymous"); setMode("stdio"); setFormOpen(false); setFieldIndex(0); setFormError("")
  }

  const addServer = () => {
    if (busy) return
    const server: Record<string, unknown> = { id: id.trim() }
    if (mode === "stdio") {
      server.command = command.trim()
      server.args = parseArgs(args)
      server.env = {}
      if (workingDirectory.trim()) server.working_directory = workingDirectory.trim()
    } else {
      server.url = url.trim()
      if (auth === "oauth") server.auth = { mode: "oauth" }
      else if (auth === "bearer_env") server.auth = { mode: "bearer_env", bearer_env: credentialReference.trim() }
      else if (auth === "header_env") server.auth = { mode: "header_env", header_name: headerName.trim(), header_value_env: credentialReference.trim() }
      else if (auth === "header_secret") server.auth = { mode: "header_secret", header_name: headerName.trim() }
    }
    if (!id.trim() || (mode === "stdio" ? !command.trim() : !url.trim())) {
      setFormError(mode === "stdio" ? "Enter a server ID and executable path." : "Enter a server ID and HTTPS endpoint URL.")
      setFieldIndex(!id.trim() ? 1 : mode === "stdio" ? 2 : 2)
      return
    }
    if (mode === "http" && ((auth === "bearer_env" || auth === "header_env") && !credentialReference.trim() || (auth === "header_env" || auth === "header_secret") && !headerName.trim() || (auth === "bearer_secret" || auth === "header_secret") && !secret.trim())) {
      setFormError("Complete the selected authentication fields.")
      setFieldIndex((auth === "header_env" || auth === "header_secret") && !headerName.trim() ? 4 : 5)
      return
    }
    const payload: Record<string, unknown> = { server }
    if (auth === "bearer_secret" || auth === "header_secret") { payload.credential_kind = auth === "bearer_secret" ? "bearer" : "header"; payload.secret = secret }
    request("add", "mcp_add", payload)
    setFormError("")
    setSecret("")
    clearForm()
  }

  const currentServer = servers[selectedIndex]
  const runRemove = () => {
    if (!removeID || busy) return
    request("remove", "mcp_remove", { id: removeID })
    setRemoveID("")
  }

  useKeyboard((key) => {
    if (!open) return
    const name = key.name.toLowerCase()
    if (removeID) {
      if (name === "escape" || name === "n") setRemoveID("")
      else if (name === "y" || name === "return") runRemove()
      return
    }
    if (formOpen) {
      if (name === "escape") { clearForm(); return }
      if (name === "tab") {
        const current = Math.max(0, formOrder.indexOf(fieldIndex))
        const step = key.shift ? -1 : 1
        setFieldIndex(formOrder[(current + step + formOrder.length) % formOrder.length]!)
        return
      }
      if (name === "return") {
        if (fieldIndex === saveIndex) addServer()
        else if (fieldIndex === 0) setMode((value) => value === "stdio" ? "http" : "stdio")
        else if (fieldIndex === 3 && mode === "http") setAuth((value) => authModes[(authModes.indexOf(value) + 1) % authModes.length]!)
        else advanceField()
        return
      }
      if (name === "left" || name === "right") {
        if (fieldIndex === 0) setMode((value) => value === "stdio" ? "http" : "stdio")
        else if (fieldIndex === 3 && mode === "http") setAuth((value) => authModes[(authModes.indexOf(value) + (name === "right" ? 1 : authModes.length - 1)) % authModes.length]!)
        return
      }
      return
    }
    if (name === "escape") { onClose(); return }
    if (name === "1") { setSelectedTab("servers"); setSelectedIndex(0); return }
    if (name === "2") { setSelectedTab("tools"); setSelectedIndex(0); return }
    if (name === "up" || name === "arrowup") { setSelectedIndex((index) => Math.max(0, index - 1)); return }
    if (name === "down" || name === "arrowdown") { setSelectedIndex((index) => Math.min(rows.length - 1, index + 1)); return }
    if (name === "a" && !busy) { setFormOpen(true); setMode("stdio"); setAuth("anonymous"); setNotice("Choose local stdio or remote HTTP; credentials are sent only with Save."); return }
    if (name === "r" && !busy) { refresh(); return }
    if (name === "x" && currentServer && !busy) { setRemoveID(currentServer.id); return }
    if (name === "l" && currentServer?.auth_mode === "oauth" && !busy) { request("login", "mcp_login", { id: currentServer.id }); return }
    if (name === "o" && currentServer?.auth_mode === "oauth" && !busy) { request("logout", "mcp_logout", { id: currentServer.id }); return }
  })

  if (!open) return null

  const field = (label: string, value: string, update: (value: string) => void, index: number, placeholder = "") => <box key={label} style={{ flexDirection: "column", gap: 0 }}>
    <text fg={fieldIndex === index ? colors.accent : colors.muted} content={`${fieldIndex === index ? "› " : "  "}${label}`} />
    <input focused={formOpen && fieldIndex === index} value={value} maxLength={maxField} placeholder={placeholder} onMouseDown={(event) => leftClick(event, () => setFieldIndex(index))} onInput={(next: string) => { update(next); setFormError("") }} />
  </box>

  return <box style={{ position: "absolute", left: "5%", right: "5%", top: "5%", bottom: "5%", border: true, borderColor: colors.line, backgroundColor: colors.panel, padding: 2, flexDirection: "column", gap: 1 }}>
    <box style={{ flexDirection: "row", justifyContent: "space-between" }}><text fg={colors.text} content="MCP connections" /><text fg={colors.dim} content="Esc close" /></box>
    <text fg={colors.muted} content="Changes apply to new sessions." />
    {formOpen ? <>
      <text fg={colors.accent} content={fieldIndex === saveIndex ? "Enter or click Save · Esc cancels" : fieldIndex === 0 || (mode === "http" && fieldIndex === 3) ? "←/→ or Enter change · Tab next · Esc cancel" : "Type value · Enter/Tab next · Esc cancel"} />
      {formError && <text fg={colors.red} content={formError} />}
      <text onMouseDown={(event) => leftClick(event, () => setFieldIndex(0))} fg={fieldIndex === 0 ? colors.accent : colors.muted} content={`${fieldIndex === 0 ? "› " : "  "}Connection type: ${mode === "stdio" ? "Local stdio" : "Remote Streamable HTTP"} · ←/→ change`} />
      {field("Server ID", id, setID, 1, "lowercase letters, digits, . _ -")}
      {mode === "stdio" ? <>
        {field("Executable path", command, setCommand, 2, "/absolute/path/to/server")}
        {field("Arguments · space-separated; quote values containing spaces", args, setArgs, 3, 'serve --label "My server"')}
        {field("Working directory (optional)", workingDirectory, setWorkingDirectory, 4, "Leave empty for home directory")}
      </> : <>
        {field("Endpoint URL", url, setURL, 2, "https://example.com/mcp")}
        <text onMouseDown={(event) => leftClick(event, () => setFieldIndex(3))} fg={fieldIndex === 3 ? colors.accent : colors.muted} content={`${fieldIndex === 3 ? "› " : "  "}Authentication: ${authLabel(auth)} · ←/→ change`} />
        {(auth === "header_env" || auth === "header_secret") && field("Header name", headerName, setHeaderName, 4, "X-API-Key")}
        {(auth === "bearer_env" || auth === "header_env") && field("Credential environment variable", credentialReference, setCredentialReference, 5, "MCP_API_KEY")}
        {(auth === "bearer_secret" || auth === "header_secret") && <>
          <text fg={fieldIndex === 5 ? colors.accent : colors.muted} content={`${fieldIndex === 5 ? "› " : "  "}Credential · stored separately`} />
          <box style={{ position: "relative", height: 1 }} onMouseDown={(event) => leftClick(event, () => setFieldIndex(5))}>
            <input id="mcp-secret-input" focused={formOpen && fieldIndex === 5} value={secret} maxLength={4096} placeholder="Enter credential" selectable={false} onInput={(next: string) => { setSecret(next); setFormError("") }} />
            <text style={{ position: "absolute", left: 0, top: 0, right: 0, bg: colors.panel }} fg={colors.text} content={secret ? "•".repeat(Math.min(secret.length, 64)) : "Type credential · masked"} />
          </box>
        </>}
      </>}
      <box style={{ flexDirection: "row", gap: 2 }}>
        <box id="mcp-save" focusable onMouseDown={(event) => leftClick(event, () => { setFieldIndex(saveIndex); addServer() })} style={{ backgroundColor: fieldIndex === saveIndex ? colors.accent : colors.raised, paddingLeft: 1, paddingRight: 1 }}><text fg={fieldIndex === saveIndex ? colors.panel : colors.accent} content="Save server" /></box>
        <box onMouseDown={(event) => leftClick(event, clearForm)} style={{ backgroundColor: colors.raised, paddingLeft: 1, paddingRight: 1 }}><text fg={colors.muted} content="Cancel · Esc" /></box>
      </box>
    </> : <>
      <box style={{ flexDirection: "row", gap: 2 }}>
        <box onMouseDown={(event) => leftClick(event, () => { setSelectedTab("servers"); setSelectedIndex(0) })} style={{ backgroundColor: selectedTab === "servers" ? colors.raised : colors.panel, paddingLeft: 1, paddingRight: 1 }}><text fg={selectedTab === "servers" ? colors.accent : colors.muted} content={`Servers · ${servers.length} [1]`} /></box>
        <box onMouseDown={(event) => leftClick(event, () => { setSelectedTab("tools"); setSelectedIndex(0) })} style={{ backgroundColor: selectedTab === "tools" ? colors.raised : colors.panel, paddingLeft: 1, paddingRight: 1 }}><text fg={selectedTab === "tools" ? colors.accent : colors.muted} content={`Tools · ${tools.length} [2]`} /></box>
      </box>
      <text fg={colors.dim} content={notice || (selectedTab === "servers" ? "A add · R refresh · X remove · L OAuth login · O logout" : `Tools from ${savedSessionTools ? "the saved session snapshot" : "the current catalog"} · listing is read-only`)} />
      <box style={{ flexDirection: "column", flexGrow: 1, minHeight: 3, border: ["top", "bottom"], borderColor: colors.line, paddingTop: 1, paddingBottom: 1 }}>
        {rows.length === 0 && <text fg={colors.dim} content={busy ? "Loading…" : selectedTab === "servers" ? "No servers configured. Press A to add one." : "No MCP tools are present in this session catalog."} />}
        {rows.slice(Math.max(0, Math.min(selectedIndex - Math.floor(visibleRows / 2), rows.length - visibleRows)), Math.max(0, Math.min(selectedIndex - Math.floor(visibleRows / 2), rows.length - visibleRows)) + visibleRows).map((row: any, localIndex) => {
          const base = Math.max(0, Math.min(selectedIndex - Math.floor(visibleRows / 2), rows.length - visibleRows))
          const index = base + localIndex
          if (selectedTab === "tools") return <box key={`${row.server_id}-${row.name}`} style={{ flexDirection: "column", minHeight: 2, backgroundColor: index === selectedIndex ? colors.raised : colors.panel, paddingLeft: 1, paddingRight: 1 }}><text fg={index === selectedIndex ? colors.accent : colors.text} content={`${row.server_id} · ${row.name}`} /><text fg={colors.dim} content={row.description} /></box>
          const server = row as MCPServer
          const description = server.transport === "stdio" ? `${server.command} · ${server.arguments_count ?? 0} args · env keys ${(server.environment_keys ?? []).join(", ") || "none"}` : `${server.url} · ${server.auth_mode ?? "anonymous"} (${server.auth_status ?? "configured"})${server.credential_env?.length ? ` · ${server.credential_env.join(", ")}` : ""}`
          return <box key={server.id} onMouseDown={(event) => leftClick(event, () => setSelectedIndex(index))} style={{ flexDirection: "column", minHeight: 2, backgroundColor: index === selectedIndex ? colors.raised : colors.panel, paddingLeft: 1, paddingRight: 1 }}><text fg={index === selectedIndex ? colors.accent : colors.text} content={`${server.id} · ${server.transport === "stdio" ? "Local stdio" : "Remote HTTP"} · ${server.auth_status ?? server.auth_mode ?? "configured"}`} /><text fg={colors.dim} content={description} /></box>
        })}
      </box>
      {currentServer && selectedTab === "servers" && <box style={{ border: ["top"], borderColor: colors.line, paddingTop: 1, flexDirection: "column", minHeight: 3 }}>
        <text fg={colors.text} content={`${currentServer.id} · ${currentServer.transport === "stdio" ? "local stdio process" : "remote Streamable HTTP"}`} />
        <text fg={colors.muted} content={currentServer.transport === "stdio" ? `Executable: ${currentServer.command} · Working directory: ${currentServer.working_directory || "home directory"}` : `${currentServer.url} · Authentication: ${authLabel((currentServer.auth_mode || "anonymous") as AuthMode)} · ${currentServer.auth_status ?? "configured"}`} />
        <text fg={colors.dim} content={currentServer.auth_mode === "oauth" ? "L login · O clear local OAuth sign-in · X remove" : "X remove · environment values and stored credentials are never displayed"} />
      </box>}
      <box style={{ flexDirection: "row", justifyContent: "space-between" }}>
        <text fg={colors.dim} content="↑↓ choose · A add · R refresh · X remove · /new applies saved config" />
        <box style={{ flexDirection: "row", gap: 1 }}>
          {currentServer?.auth_mode === "oauth" && <>
            <box onMouseDown={(event) => leftClick(event, () => !busy && request("login", "mcp_login", { id: currentServer.id }))} style={{ backgroundColor: colors.raised, paddingLeft: 1, paddingRight: 1 }}><text fg={colors.accent} content="Login · L" /></box>
            <box onMouseDown={(event) => leftClick(event, () => !busy && request("logout", "mcp_logout", { id: currentServer.id }))} style={{ backgroundColor: colors.raised, paddingLeft: 1, paddingRight: 1 }}><text fg={colors.muted} content="Logout · O" /></box>
          </>}
          {currentServer && <box onMouseDown={(event) => leftClick(event, () => !busy && setRemoveID(currentServer.id))} style={{ backgroundColor: colors.raised, paddingLeft: 1, paddingRight: 1 }}><text fg={colors.red} content="Remove · X" /></box>}
          <text fg={colors.dim} content={busy ? "Working…" : "Esc close"} />
        </box>
      </box>
    </>}
    {removeID && <box style={{ position: "absolute", left: "15%", right: "15%", top: "35%", border: true, borderColor: colors.red, backgroundColor: colors.raised, padding: 2, flexDirection: "column", gap: 1, minHeight: 4 }}>
      <text fg={colors.red} content={`Remove MCP server “${removeID}”? This changes future sessions; it does not erase old transcripts.`} />
      <box style={{ flexDirection: "row", gap: 2 }}>
        <box onMouseDown={(event) => leftClick(event, runRemove)} style={{ backgroundColor: colors.panel, paddingLeft: 1, paddingRight: 1 }}><text fg={colors.red} content="Remove · Y" /></box>
        <box onMouseDown={(event) => leftClick(event, () => setRemoveID(""))} style={{ backgroundColor: colors.panel, paddingLeft: 1, paddingRight: 1 }}><text fg={colors.accent} content="Cancel · Esc" /></box>
      </box>
    </box>}
  </box>
}
