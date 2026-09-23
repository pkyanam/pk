import { useKeyboard, usePaste, useRenderer } from "@opentui/react"
import { useEffect, useLayoutEffect, useRef, useState } from "react"
import { SecretInput } from "./secret-input"
import { palette as colors } from "./theme"

export type WorkersAISetupProps = {
  open: boolean
  saving?: boolean
  error?: string
  onClose: () => void
  onRegisterPasteHandler?: (handler: ((text: string) => void) | null) => void
  onRegisterKeyHandler?: (handler: ((key: SetupKey) => void) | null) => void
  // Return false only when the transport could not queue the request. The
  // component clears its local secret after a request is accepted.
  onSubmit: (accountID: string, apiToken: string) => boolean
}

export type SetupKey = {
  name: string
  sequence?: string
  ctrl?: boolean
  meta?: boolean
  super?: boolean
  option?: boolean
  preventDefault: () => void
  stopPropagation: () => void
}

function leftClick(event: { button: number; preventDefault: () => void; stopPropagation: () => void }, action: () => void) {
  if (event.button !== 0) return
  event.preventDefault()
  event.stopPropagation()
  action()
}

function AccountIDInput({ value, focused, onChange }: { value: string; focused: boolean; onChange: (value: string) => void }) {
  const current = useRef(value)
  useEffect(() => { current.current = value }, [value])
  const update = (next: string) => {
    current.current = next
    onChange(next)
  }

  usePaste((event) => {
    if (!focused) return
    const mime = event.metadata?.mimeType?.toLowerCase() ?? ""
    if (event.metadata?.kind === "binary" || mime.includes("uri-list") || (mime && !mime.startsWith("text/plain"))) return
    const text = new TextDecoder().decode(event.bytes.subarray(0, 512)).replace(/[\u0000-\u001f\u007f]/g, "")
    if (!text) return
    event.preventDefault()
    event.stopPropagation()
    update(Array.from(current.current + text).slice(0, 32).join(""))
  })

  useKeyboard((key) => {
    if (!focused) return
    const name = key.name.toLowerCase()
    if (["escape", "esc", "return", "enter", "kpenter", "tab"].includes(name)) return
    if (key.ctrl && name === "u") {
      key.preventDefault()
      key.stopPropagation()
      update("")
      return
    }
    if (["backspace", "delete", "del"].includes(name)) {
      key.preventDefault()
      key.stopPropagation()
      update(Array.from(current.current).slice(0, -1).join(""))
      return
    }
    if (key.ctrl || key.meta || key.super || key.option) return
    const sequence = key.sequence
    if (!sequence || /[\u0000-\u001f\u007f]/u.test(sequence)) return
    key.preventDefault()
    key.stopPropagation()
    update(Array.from(current.current + sequence).slice(0, 32).join(""))
  })

  return <text id="workers-ai-account-id" selectable={false} fg={value ? colors.text : colors.dim} content={value || "Paste Cloudflare account ID"} />
}

export function WorkersAISetup({ open, saving = false, error, onClose, onSubmit, onRegisterPasteHandler, onRegisterKeyHandler }: WorkersAISetupProps) {
  const renderer = useRenderer()
  const compact = renderer.width < 92 || renderer.height < 30
  const [accountID, setAccountID] = useState("")
  const [apiToken, setAPIToken] = useState("")
  const [field, setField] = useState<"account" | "token">("account")
  const [localError, setLocalError] = useState("")
  const tokenRef = useRef(apiToken)
  const accountRef = useRef(accountID)
  useEffect(() => { tokenRef.current = apiToken }, [apiToken])
  useEffect(() => { accountRef.current = accountID }, [accountID])
  useLayoutEffect(() => {
    onRegisterPasteHandler?.((text) => {
      const clean = text.replace(/[\u0000-\u001f\u007f]/g, "")
      if (field === "account") {
        const next = Array.from(accountRef.current + clean).slice(0, 32).join("")
        accountRef.current = next
        setAccountID(next)
      } else {
        const next = Array.from(tokenRef.current + clean).slice(0, 4096).join("")
        tokenRef.current = next
        setAPIToken(next)
      }
      setLocalError("")
    })
    return () => onRegisterPasteHandler?.(null)
  }, [field, onRegisterPasteHandler])
  useLayoutEffect(() => {
    onRegisterKeyHandler?.((key) => {
      if (!open) return
      const name = key.name.toLowerCase()
      if (name === "escape" || name === "esc") {
        key.preventDefault()
        key.stopPropagation()
        if (!saving) close()
        return
      }
      if (name === "tab" || name === "arrowdown" || name === "arrowup") {
        key.preventDefault()
        key.stopPropagation()
        setField((current) => name === "arrowup" ? (current === "token" ? "account" : "token") : (current === "account" ? "token" : "account"))
        return
      }
      if (["return", "enter", "kpenter"].includes(name)) {
        key.preventDefault()
        key.stopPropagation()
        if (field === "account") setField("token")
        else submit()
        return
      }
      if (saving || key.ctrl || key.meta || key.super || key.option) {
        if (key.ctrl && name === "u") {
          key.preventDefault()
          key.stopPropagation()
          if (field === "account") { accountRef.current = ""; setAccountID("") }
          else { tokenRef.current = ""; setAPIToken("") }
        }
        return
      }
      if (["backspace", "delete", "del"].includes(name)) {
        key.preventDefault()
        key.stopPropagation()
        if (field === "account") {
          const next = Array.from(accountRef.current).slice(0, -1).join("")
          accountRef.current = next
          setAccountID(next)
        } else {
          const next = Array.from(tokenRef.current).slice(0, -1).join("")
          tokenRef.current = next
          setAPIToken(next)
        }
        return
      }
      const sequence = key.sequence
      if (!sequence || /[\u0000-\u001f\u007f]/u.test(sequence)) return
      key.preventDefault()
      key.stopPropagation()
      if (field === "account") {
        const next = Array.from(accountRef.current + sequence).slice(0, 32).join("")
        accountRef.current = next
        setAccountID(next)
      } else {
        const next = Array.from(tokenRef.current + sequence).slice(0, 4096).join("")
        tokenRef.current = next
        setAPIToken(next)
      }
      setLocalError("")
    })
    return () => onRegisterKeyHandler?.(null)
  }, [field, onRegisterKeyHandler, open, saving])
  useEffect(() => {
    if (open) return
    tokenRef.current = ""
    accountRef.current = ""
    setAPIToken("")
    setAccountID("")
    setLocalError("")
  }, [open])

  const close = () => {
    if (saving) return
    tokenRef.current = ""
    accountRef.current = ""
    setAPIToken("")
    setAccountID("")
    setLocalError("")
    onClose()
  }

  const submit = () => {
    if (saving) return
    const account = accountRef.current.trim()
    const token = tokenRef.current.trim()
    if (!account) {
      setLocalError("Enter your Cloudflare account ID.")
      setField("account")
      return
    }
    if (!/^[0-9a-f]{32}$/iu.test(account)) {
      setLocalError("Enter the 32-character hexadecimal account ID from the Workers AI REST API page.")
      setField("account")
      return
    }
    if (!token) {
      setLocalError("Paste a Cloudflare API token with Workers AI access.")
      setField("token")
      return
    }
    setLocalError("")
    if (onSubmit(account, token)) {
      tokenRef.current = ""
      setAPIToken("")
    } else {
      setLocalError("Could not send the setup request. Try again.")
    }
  }

  useKeyboard((key) => {
    if (!open) return
    const name = key.name.toLowerCase()
    if (name === "escape" || name === "esc") {
      key.preventDefault()
      key.stopPropagation()
      if (!saving) close()
      return
    }
    if (name === "tab" || name === "arrowdown") {
      key.preventDefault()
      key.stopPropagation()
      setField((current) => current === "account" ? "token" : "account")
      return
    }
    if (name === "arrowup") {
      key.preventDefault()
      key.stopPropagation()
      setField((current) => current === "token" ? "account" : "token")
      return
    }
    if (["return", "enter", "kpenter"].includes(name)) {
      key.preventDefault()
      key.stopPropagation()
      if (field === "account") setField("token")
      else submit()
    }
  })

  if (!open) return null
  return <box style={{ position: "absolute", left: compact ? "2%" : "12%", right: compact ? "2%" : "12%", top: compact ? 0 : "20%", bottom: compact ? 0 : undefined, border: true, borderColor: colors.accent, backgroundColor: colors.raised, padding: compact ? 1 : 2, flexDirection: "column", gap: compact ? 0 : 1 }}>
    <box style={{ flexDirection: "row", justifyContent: "space-between" }}>
      <text fg={colors.text} content="Connect Cloudflare Workers AI" />
      <text fg={colors.dim} content="Esc cancel" />
    </box>
    <text fg={colors.muted} content="Direct Workers AI · not AI Gateway. Tool calling varies by model." />
    <text fg={colors.dim} content="Credentials stay in pk's private provider store." />
    <text fg={colors.dim} content="Help: developers.cloudflare.com/workers-ai/get-started/rest-api" />
    <text fg={colors.dim} content="Account ID · 32 hexadecimal characters" />
    <box onMouseDown={(event) => leftClick(event, () => setField("account"))} style={{ border: true, borderColor: field === "account" ? colors.accent : colors.line, backgroundColor: colors.panel, paddingLeft: 1, paddingRight: 1, height: 3, flexShrink: 0 }}>
      <AccountIDInput value={accountID} focused={open && field === "account" && !saving} onChange={(value) => { setAccountID(value); setLocalError("") }} />
    </box>
    <text fg={colors.dim} content="API token · needs Workers AI access; paste is masked" />
    <box onMouseDown={(event) => leftClick(event, () => setField("token"))} style={{ border: true, borderColor: field === "token" ? colors.accent : colors.line, backgroundColor: colors.panel, paddingLeft: 1, paddingRight: 1, height: 3, flexShrink: 0 }}>
      <SecretInput id="workers-ai-api-token" focused={open && field === "token" && !saving} value={apiToken} placeholder="Paste Cloudflare API token · masked" color={colors.text} mutedColor={colors.dim} onChange={(value) => { tokenRef.current = value; setAPIToken(value); setLocalError("") }} />
    </box>
    {(localError || error) && <text fg={colors.red} content={localError || error || ""} />}
    <box style={{ flexDirection: "row", gap: 2 }}>
      <box onMouseDown={(event) => leftClick(event, submit)} style={{ backgroundColor: colors.accent, paddingLeft: 1, paddingRight: 1, height: 1 }}>
        <text fg={colors.panel} content={saving ? "Connecting…" : "Connect · Enter"} />
      </box>
      <box onMouseDown={(event) => leftClick(event, close)} style={{ backgroundColor: colors.panel, paddingLeft: 1, paddingRight: 1, height: 1 }}>
        <text fg={colors.muted} content="Cancel" />
      </box>
    </box>
    <text fg={colors.dim} content={saving ? "Saving token privately · model discovery follows" : "Create a Workers AI API token with Workers AI access · Tab moves fields · Ctrl-U clears"} />
  </box>
}
