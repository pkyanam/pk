import { useKeyboard, useRenderer } from "@opentui/react"
import { useEffect, useRef, useState } from "react"
import type { ServerEvent } from "./protocol"
import { palette as colors } from "./theme"

export type ManagedSession = {
  id: string
  created_at: string
  updated_at: string
  workspace?: string
  title?: string
  preview?: string
  item_count: number
  active: boolean
}

export type TrashedSession = {
  trash_id: string
  session_id: string
  archived_at: string
  workspace?: string
  title?: string
  state: string
}

type SessionResult = { session_id: string; trash_id?: string; ok: boolean; error?: string }
type SessionTab = "sessions" | "trash"
type RequestKind = "sessions" | "trash" | "archive" | "restore" | "purge"
type RequestInfo = { kind: RequestKind; query: string }

export type SessionManagerProps = {
  open: boolean
  onClose: () => void
  send: (type: string, payload?: Record<string, unknown>) => string | undefined
  event?: ServerEvent
  onLoad: (session: ManagedSession) => void
}

const listLimit = 500
const maxQueryBytes = 512

function formatDate(value: string): string {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return "unknown date"
  return date.toLocaleString(undefined, { month: "short", day: "numeric", hour: "numeric", minute: "2-digit" })
}

function compactPath(path: string, max: number): string {
  if (path.length <= max) return path
  return `…${path.slice(-(max - 1))}`
}

function singleLine(value: string | undefined, max: number): string {
  const text = (value ?? "").replace(/\s+/g, " ").trim()
  if (Bun.stringWidth(text) <= max) return text
  const segmenter = new Intl.Segmenter(undefined, { granularity: "grapheme" })
  let output = ""
  let width = 0
  for (const part of segmenter.segment(text)) {
    const partWidth = Bun.stringWidth(part.segment)
    if (width + partWidth > Math.max(0, max - 1)) return `${output}…`
    output += part.segment
    width += partWidth
  }
  return output
}

export function SessionManager({ open, onClose, send, event, onLoad }: SessionManagerProps) {
  const renderer = useRenderer()
  const [tab, setTab] = useState<SessionTab>("sessions")
  const [query, setQuery] = useState("")
  const [sessions, setSessions] = useState<ManagedSession[]>([])
  const [trash, setTrash] = useState<TrashedSession[]>([])
  const [selectedIndex, setSelectedIndex] = useState(0)
  const [selectedIDs, setSelectedIDs] = useState<Set<string>>(() => new Set())
  const [confirmPurge, setConfirmPurge] = useState(false)
  const [busy, setBusy] = useState(false)
  const [searchDirty, setSearchDirty] = useState(false)
  const [searchMode, setSearchMode] = useState(false)
  const [notice, setNotice] = useState("")
  const pending = useRef(new Map<string, RequestInfo>())
  const searchInput = useRef<any>(null)
  const latestListRequest = useRef("")
  const opened = useRef(false)
  // List entries have exactly two lines (title/meta and workspace/preview).
  // Reserve one extra line for borders and keep the viewport estimate tied to
  // terminal height so large lists scroll without their previews changing row
  // geometry.
  const visibleRows = Math.max(1, Math.min(12, Math.floor((renderer.height - 30) / 2)))

  const sendRequest = (kind: RequestKind, type: string, payload?: Record<string, unknown>) => {
    const id = send(type, payload)
    if (!id) {
      setNotice("pk is disconnected; this request was not sent.")
      return ""
    }
    pending.current.set(id, { kind, query })
    if (kind === "sessions" || kind === "trash") latestListRequest.current = id
    setBusy(true)
    setNotice(kind === "archive" ? "Moving sessions to recoverable trash…" : kind === "restore" ? "Restoring archived sessions…" : kind === "purge" ? "Permanently deleting archived sessions…" : "Loading sessions…")
    return id
  }

  const refresh = (target: SessionTab = tab, search = query) => {
    setSearchDirty(false)
    const bounded = search.slice(0, maxQueryBytes)
    if (target === "sessions") sendRequest("sessions", "sessions_list", { query: bounded, limit: listLimit })
    else sendRequest("trash", "sessions_trash_list", { query: bounded })
  }

  useEffect(() => {
    if (!open) {
      opened.current = false
      setConfirmPurge(false)
      setSelectedIDs(new Set())
      return
    }
    if (opened.current) return
    opened.current = true
    setTab("sessions")
    setQuery("")
    setSearchMode(false)
    if (searchInput.current) searchInput.current.value = ""
    setSelectedIDs(new Set())
    setSelectedIndex(0)
    refresh("sessions", "")
  }, [open])

  useEffect(() => {
    if (!open || !opened.current || !searchDirty) return
    const timer = setTimeout(() => refresh(tab, query), 250)
    return () => clearTimeout(timer)
  }, [query, tab, open, searchDirty])

  const visibleItems = tab === "sessions" ? sessions : trash
  const rowKey = (index: number) => tab === "sessions" ? (sessions[index] as ManagedSession | undefined)?.id : (trash[index] as TrashedSession | undefined)?.trash_id

  useEffect(() => {
    setSelectedIndex((index) => visibleItems.length === 0 ? 0 : Math.max(0, Math.min(index, visibleItems.length - 1)))
    setSelectedIDs(new Set())
  }, [tab, sessions, trash])

  const setTabAndRefresh = (next: SessionTab) => {
    if (next === tab) return
    setTab(next)
    setSelectedIDs(new Set())
    setSelectedIndex(0)
    setNotice("")
    refresh(next)
  }

  const toggleSelected = (index = selectedIndex) => {
    const id = rowKey(index)
    if (!id) return
    if (tab === "sessions" && sessions[index]?.active) {
      setNotice("This session is open in another view and cannot be archived here.")
      return
    }
    setSelectedIDs((previous) => {
      const next = new Set(previous)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  const selectableIDs = visibleItems.flatMap((item) => {
    if (tab === "sessions" && (item as ManagedSession).active) return []
    return [tab === "sessions" ? (item as ManagedSession).id : (item as TrashedSession).trash_id]
  })
  const allSelected = selectableIDs.length > 0 && selectableIDs.every((id) => selectedIDs.has(id))
  const toggleAll = () => {
    if (busy || selectableIDs.length === 0) return
    const clearing = allSelected
    setSelectedIDs(clearing ? new Set() : new Set(selectableIDs))
    const skipped = tab === "sessions" ? visibleItems.length - selectableIDs.length : 0
    setNotice(clearing ? (skipped > 0 ? `Selection cleared; ${skipped} open session${skipped === 1 ? "" : "s"} ${skipped === 1 ? "remains" : "remain"} unselected.` : "Selection cleared.") : skipped > 0 ? `Selected ${selectableIDs.length}; skipped ${skipped} open session${skipped === 1 ? "" : "s"}.` : `Selected ${selectableIDs.length} session${selectableIDs.length === 1 ? "" : "s"}.`)
  }

  const selected = [...selectedIDs]
  const runArchive = () => {
    if (tab !== "sessions" || selected.length === 0 || busy) return
    sendRequest("archive", "sessions_archive", { session_ids: selected })
  }
  const runRestore = () => {
    if (tab !== "trash" || selected.length === 0 || busy) return
    sendRequest("restore", "sessions_restore", { trash_ids: selected })
  }
  const runPurge = () => {
    if (!confirmPurge || selected.length === 0 || busy) return
    setConfirmPurge(false)
    sendRequest("purge", "sessions_purge", { trash_ids: selected })
  }

  useEffect(() => {
    if (!open || !event?.id) return
    const request = pending.current.get(event.id)
    if (!request) return
    const data = event.payload ?? {}
    if (event.type === "error") {
      pending.current.delete(event.id)
      if (request.kind === "sessions" || request.kind === "trash") {
        if (event.id === latestListRequest.current) setBusy(false)
      } else setBusy(false)
      setNotice(String(data.message ?? "The session operation failed."))
      return
    }
    const expected = request.kind === "sessions" ? "sessions"
      : request.kind === "trash" ? "sessions_trash"
        : request.kind === "archive" ? "sessions_archived"
          : request.kind === "restore" ? "sessions_restored" : "sessions_purged"
    if (event.type !== expected) return
    pending.current.delete(event.id)
    if (request.kind === "sessions" || request.kind === "trash") {
      if (event.id !== latestListRequest.current) return
      const rows = Array.isArray(data.sessions) ? data.sessions : []
      if (request.kind === "sessions") setSessions(rows as ManagedSession[])
      else setTrash(rows as TrashedSession[])
      setBusy(false)
      setNotice("")
      return
    }
    const results = Array.isArray(data.results) ? data.results as SessionResult[] : []
    const succeeded = results.filter((result) => result.ok).length
    const failed = results.filter((result) => !result.ok)
    setSelectedIDs(new Set())
    setBusy(false)
    const summary = failed.length > 0
      ? `${succeeded} complete · ${failed.length} failed: ${failed[0]?.error ?? "operation failed"}`
      : request.kind === "archive" ? `Archived ${succeeded} session${succeeded === 1 ? "" : "s"} · recoverable from Trash.`
        : request.kind === "restore" ? `Restored ${succeeded} session${succeeded === 1 ? "" : "s"}.`
          : `Permanently deleted ${succeeded} archived session${succeeded === 1 ? "" : "s"}.`
    refresh(tab)
    setNotice(summary)
  }, [event, open])

  useKeyboard((key) => {
    if (!open) return
    const name = key.name.toLowerCase()
    if (searchMode) {
      if (name === "escape" || name === "return") { setSearchMode(false); return }
      return
    }
    if (confirmPurge) {
      if (name === "escape" || name === "n") setConfirmPurge(false)
      else if (name === "y" || name === "return") runPurge()
      return
    }
    if (name === "escape") { onClose(); return }
    if (name === "/") { setSearchMode(true); return }
    if (name === "1") { setTabAndRefresh("sessions"); return }
    if (name === "2") { setTabAndRefresh("trash"); return }
    if (name === "up" || name === "arrowup") { setSelectedIndex((index) => Math.max(0, index - 1)); return }
    if (name === "down" || name === "arrowdown") { setSelectedIndex((index) => Math.min(visibleItems.length - 1, index + 1)); return }
    if (name === "space" || key.sequence === " ") { toggleSelected(); return }
    if (name === "a") { runArchive(); return }
    if (name === "r") { runRestore(); return }
    if (name === "p") {
      if (tab === "trash" && selected.length > 0) setConfirmPurge(true)
      else if (tab === "sessions" && selected.length > 0) setNotice("Purge is only available in Trash. Press A to archive selected sessions first; they remain recoverable there.")
      return
    }
    if (name === "s") { toggleAll(); return }
    if (name === "c" && busy) { send("session_operation_cancel"); return }
    if (name === "return" && tab === "sessions") {
      const session = sessions[selectedIndex]
      if (session && !session.active) onLoad(session)
      else if (session?.active) setNotice("This session is already open.")
    }
  })

  if (!open) return null

  const start = Math.max(0, Math.min(selectedIndex - Math.floor(visibleRows / 2), Math.max(0, visibleItems.length - visibleRows)))
  const page = visibleItems.slice(start, start + visibleRows)
  const activeSession = tab === "sessions" ? sessions[selectedIndex] : undefined
  const trashSession = tab === "trash" ? trash[selectedIndex] : undefined
  const detailTitle = singleLine(tab === "sessions" ? activeSession?.title || "Untitled session" : trashSession?.title || "Archived session", Math.max(20, renderer.width - 16))
  const detailPreview = singleLine(activeSession?.preview || (trashSession ? `Archived ${formatDate(trashSession.archived_at)} · ${trashSession.state}` : "Select a session to inspect its saved metadata."), Math.max(20, renderer.width - 16))
  const detailMetadata = singleLine(`${compactPath(activeSession?.workspace || trashSession?.workspace || "", Math.max(24, renderer.width - 28))}${activeSession ? `  ·  Created ${formatDate(activeSession.created_at)}` : ""}`, Math.max(20, renderer.width - 16))
  const openDisabled = busy || !activeSession || activeSession.active

  const leftMouseDown = (event: { button: number; preventDefault: () => void; stopPropagation: () => void }, action: () => void) => {
    if (event.button !== 0) return
    event.preventDefault()
    event.stopPropagation()
    action()
  }

  return <box style={{ position: "absolute", left: "6%", right: "6%", top: "7%", bottom: "7%", border: true, borderColor: colors.line, backgroundColor: colors.panel, padding: 2, flexDirection: "column", gap: 1 }}>
    <box style={{ flexDirection: "row", justifyContent: "space-between" }}>
      <text fg={colors.text} content="Saved sessions" />
      <text fg={colors.dim} content="Esc close" />
    </box>
    <text fg={colors.muted} content="Search, reopen, or clean up local conversation history. Archive is recoverable; purge permanently deletes archived data." />
    <box style={{ flexDirection: "row", gap: 2 }}>
      <box onMouseDown={(event: { button: number; preventDefault: () => void; stopPropagation: () => void }) => leftMouseDown(event, () => setTabAndRefresh("sessions"))} style={{ backgroundColor: tab === "sessions" ? colors.raised : colors.panel, paddingLeft: 1, paddingRight: 1 }}><text fg={tab === "sessions" ? colors.accent : colors.muted} content={`Sessions · ${sessions.length}  [1]`} /></box>
      <box onMouseDown={(event: { button: number; preventDefault: () => void; stopPropagation: () => void }) => leftMouseDown(event, () => setTabAndRefresh("trash"))} style={{ backgroundColor: tab === "trash" ? colors.raised : colors.panel, paddingLeft: 1, paddingRight: 1 }}><text fg={tab === "trash" ? colors.accent : colors.muted} content={`Trash · ${trash.length}  [2]`} /></box>
    </box>
    <box style={{ flexDirection: "row", gap: 1 }}>
      <box onMouseDown={(event: { button: number; preventDefault: () => void; stopPropagation: () => void }) => leftMouseDown(event, toggleAll)} style={{ backgroundColor: colors.raised, paddingLeft: 1, paddingRight: 1 }}>
        <text fg={colors.accent} content={allSelected ? "Clear selection · S" : "Select all · S"} />
      </box>
      <text fg={colors.dim} content={tab === "sessions" ? "Open sessions are skipped" : "Select all archived items"} />
    </box>
    <box style={{ flexDirection: "row", gap: 1 }}>
      <text fg={searchMode ? colors.accent : colors.muted} content="/" />
      <input id="session-search" ref={searchInput} focused={searchMode} value={query} maxLength={maxQueryBytes} placeholder="Filter title, workspace, or preview…" onInput={(value: string) => { setQuery(value); setSearchDirty(true); setSelectedIndex(0) }} onChange={(value: string) => { setQuery(value); setSearchDirty(true); setSelectedIndex(0) }} onSubmit={() => { const value = String(searchInput.current?.value ?? "").slice(0, maxQueryBytes); setQuery(value); setSearchDirty(false); setSelectedIndex(0); refresh(tab, value); setSearchMode(false) }} />
      <text fg={colors.dim} content={searchMode ? "Enter done · Esc search off" : "press / to search"} />
    </box>
    <text fg={colors.dim} content={busy ? notice : notice || `${visibleItems.length === 0 ? "No results" : `Showing ${start + 1}–${Math.min(start + page.length, visibleItems.length)} of ${visibleItems.length}`} · ${selected.length} selected`} />
    <box style={{ flexDirection: "column", flexGrow: 1, minHeight: 3, border: ["top", "bottom"], borderColor: colors.line, paddingTop: 1, paddingBottom: 1 }}>
      {page.length === 0 && <text fg={colors.dim} content={busy ? "Loading…" : tab === "sessions" ? "No saved sessions match this filter." : "Trash is empty. Archived sessions can be restored or permanently removed here."} />}
      {page.map((item, localIndex) => {
        const index = start + localIndex
        const session = tab === "sessions" ? item as ManagedSession : undefined
        const archived = tab === "trash" ? item as TrashedSession : undefined
        const id = session?.id ?? archived?.trash_id ?? ""
        const isSelected = selectedIDs.has(id)
        const isCursor = index === selectedIndex
        const title = session?.title || archived?.title || "Untitled session"
        const workspace = session?.workspace || archived?.workspace || "Workspace unavailable"
        const meta = session ? `${session.item_count} entries · updated ${formatDate(session.updated_at)}${session.active ? " · open now" : ""}` : `${archived?.state ?? "archived"} · ${formatDate(archived?.archived_at ?? "")}`
        const line = singleLine(`${isSelected ? "[x]" : "[ ]"} ${title}  ·  ${meta}`, Math.max(20, renderer.width - 22))
        return <box key={id} onMouseDown={(event: { button: number; preventDefault: () => void; stopPropagation: () => void }) => leftMouseDown(event, () => { setSelectedIndex(index); toggleSelected(index) })} style={{ flexDirection: "column", minHeight: 2, backgroundColor: isCursor ? colors.raised : colors.panel, paddingLeft: 1, paddingRight: 1 }}>
          <text fg={isCursor ? colors.accent : colors.text} content={line} />
          <text fg={colors.dim} content={singleLine(`${compactPath(workspace, Math.max(20, renderer.width - 25))}  ·  ${session?.preview ?? ""}`, Math.max(20, renderer.width - 25))} />
        </box>
      })}
    </box>
    <box style={{ flexDirection: "column", borderColor: colors.line, paddingLeft: 1 }}>
      <text fg={colors.text} content={detailTitle || "Session details"} />
      <text fg={colors.muted} content={detailMetadata} />
      <text fg={colors.dim} content={detailPreview} />
      {tab === "sessions" && <box onMouseDown={(event: { button: number; preventDefault: () => void; stopPropagation: () => void }) => leftMouseDown(event, () => {
        if (!openDisabled && activeSession) onLoad(activeSession)
      })} style={{ alignSelf: "flex-start", backgroundColor: openDisabled ? colors.panel : colors.raised, paddingLeft: 1, paddingRight: 1 }}>
        <text fg={openDisabled ? colors.dim : colors.accent} content={activeSession?.active ? "Already open" : busy ? "Open session · wait for operation" : "Open session · click"} />
      </box>}
    </box>
    {confirmPurge && <box style={{ border: true, borderColor: colors.red, backgroundColor: colors.raised, padding: 1, gap: 1, minHeight: 4, flexDirection: "column" }}>
      <text fg={colors.red} content={`Permanently delete ${selected.length} archived session${selected.length === 1 ? "" : "s"}? This cannot be undone.`} />
      <box style={{ flexDirection: "row", gap: 2 }}>
        <box onMouseDown={(event: { button: number; preventDefault: () => void; stopPropagation: () => void }) => leftMouseDown(event, runPurge)} style={{ backgroundColor: colors.panel, paddingLeft: 1, paddingRight: 1 }}><text fg={colors.red} content="Delete permanently · Y" /></box>
        <box onMouseDown={(event: { button: number; preventDefault: () => void; stopPropagation: () => void }) => leftMouseDown(event, () => setConfirmPurge(false))} style={{ backgroundColor: colors.panel, paddingLeft: 1, paddingRight: 1 }}><text fg={colors.accent} content="Keep archived · Esc" /></box>
      </box>
    </box>}
    <box style={{ flexDirection: "row", justifyContent: "space-between" }}>
      <text fg={colors.dim} content={tab === "sessions" ? "↑↓ move · Space select · S all · Enter open · A archive" : "↑↓ move · Space select · S all · R restore · P purge"} />
      <box style={{ flexDirection: "row", gap: 1 }}>
        {tab === "sessions" ? <box onMouseDown={(event: { button: number; preventDefault: () => void; stopPropagation: () => void }) => leftMouseDown(event, runArchive)} style={{ backgroundColor: colors.raised, paddingLeft: 1, paddingRight: 1 }}><text fg={colors.accent} content={`Archive ${selected.length || "selected"}`} /></box> : <>
          <box onMouseDown={(event: { button: number; preventDefault: () => void; stopPropagation: () => void }) => leftMouseDown(event, runRestore)} style={{ backgroundColor: colors.raised, paddingLeft: 1, paddingRight: 1 }}><text fg={colors.accent} content={`Restore ${selected.length || "selected"}`} /></box>
          <box onMouseDown={(event: { button: number; preventDefault: () => void; stopPropagation: () => void }) => leftMouseDown(event, () => selected.length > 0 && setConfirmPurge(true))} style={{ backgroundColor: colors.raised, paddingLeft: 1, paddingRight: 1 }}><text fg={colors.red} content={`Purge ${selected.length || "selected"}`} /></box>
        </>}
      </box>
    </box>
  </box>
}
