import { useKeyboard, usePaste } from "@opentui/react"
import { useEffect, useRef } from "react"
import { palette } from "./theme"

export type SecretInputProps = {
  focused: boolean
  value: string
  onChange: (next: string) => void
  maxLength?: number
  placeholder?: string
  color?: string
  mutedColor?: string
  id?: string
}

const DEFAULT_MAX_BYTES = 4096

function byteLength(value: string): number {
  return new TextEncoder().encode(value).length
}

export function appendSecret(value: string, addition: string, maxBytes = DEFAULT_MAX_BYTES): string {
  const remaining = Math.max(0, maxBytes - byteLength(value))
  if (remaining === 0) return value
  let output = value
  let used = 0
  for (const character of addition) {
    const size = byteLength(character)
    if (used + size > remaining) break
    output += character
    used += size
  }
  return output
}

function printableInput(sequence: string): boolean {
  return sequence.length > 0 && !/[\u0000-\u001f\u007f]/u.test(sequence)
}

export function normalizeSecretPaste(text: string, mimeType?: string, kind?: string): string | undefined {
  if (kind === "binary" || mimeType?.toLowerCase().includes("uri-list")) return undefined
  if (mimeType && !mimeType.toLowerCase().startsWith("text/plain")) return undefined
  return text.replace(/[\u0000-\u001f\u007f]/g, "")
}

export function SecretInput({
  focused,
  value,
  onChange,
  maxLength = DEFAULT_MAX_BYTES,
  placeholder = "Paste secret · masked",
  color = palette.text,
  mutedColor = palette.muted,
  id = "secret-input",
}: SecretInputProps) {
  const byteLimit = Math.min(DEFAULT_MAX_BYTES, Math.max(0, Math.floor(maxLength)))
  const currentValue = useRef(value)
  useEffect(() => { currentValue.current = value }, [value])
  const change = (next: string) => {
    // Keep event bursts (paste/rapid keypresses inside one React batch) from
    // appending repeatedly to an older controlled-prop value.
    currentValue.current = next
    onChange(next)
  }

  usePaste((event) => {
    if (!focused) return
    const mimeType = event.metadata?.mimeType
    const pasted = new TextDecoder().decode(event.bytes.subarray(0, Math.max(256, byteLimit * 4)))
    // Explicit URI-list and binary payloads belong to the application's file
    // import path. Plain text is treated as a secret even if it starts with '/'.
    const clean = normalizeSecretPaste(pasted, mimeType, event.metadata?.kind)
    if (!clean) return
    event.preventDefault()
    event.stopPropagation()
    change(appendSecret(currentValue.current, clean, byteLimit))
  })

  useKeyboard((key) => {
    if (!focused) return
    const name = key.name.toLowerCase()
    if (name === "escape" || name === "esc" || name === "return" || name === "enter" || name === "kpenter") return
    if (key.ctrl && name === "u") {
      key.preventDefault()
      key.stopPropagation()
      change("")
      return
    }
    if (name === "backspace" || name === "delete" || name === "del") {
      key.preventDefault()
      key.stopPropagation()
      change(Array.from(currentValue.current).slice(0, -1).join(""))
      return
    }
    // Ctrl/Cmd+V is intentionally left to terminal paste handling. In
    // particular, pk's native clipboard bridge can turn file payloads into
    // paths and must not be captured as an API key.
    if ((key.ctrl || key.meta || key.super) && name === "v") return
    if (key.ctrl || key.meta || key.super || key.option) return
    const sequence = key.sequence
    if (!printableInput(sequence)) return
    key.preventDefault()
    key.stopPropagation()
    change(appendSecret(currentValue.current, sequence, byteLimit))
  })

  const visibleLength = Math.min(Array.from(value).length, byteLimit)
  const content = visibleLength > 0 ? "•".repeat(visibleLength) : placeholder
  return <box id={id} style={{ width: "100%", height: 1, overflow: "hidden" }}>
    <text selectable={false} fg={visibleLength > 0 ? color : mutedColor} content={content} />
  </box>
}
