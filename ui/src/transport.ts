import { decode, encode, type Command, type CommandType, type ServerEvent } from "./protocol"

export type EventHandler = (event: ServerEvent) => void

export class PkTransport {
  private child: Bun.Subprocess<"pipe", "pipe", "pipe"> | undefined
  private sequence = 0

  private onEvent: EventHandler

  constructor(onEvent: EventHandler = () => {}) { this.onEvent = onEvent }

  setEventHandler(handler: EventHandler) { this.onEvent = handler }

  async start(config: { workspace: string; model: string; effort: string; sessionId?: string }) {
    const executable = process.env.PK_EXECUTABLE || "pk"
    this.child = Bun.spawn([executable, "rpc"], {
      stdin: "pipe",
      stdout: "pipe",
      stderr: "pipe",
      env: process.env,
    }) as Bun.Subprocess<"pipe", "pipe", "pipe">
    void this.readEvents(this.child.stdout)
    void this.readLogs(this.child.stderr)
    this.send("start", {
      workspace: config.workspace,
      model: config.model,
      effort: config.effort,
      ...(config.sessionId ? { session_id: config.sessionId } : {}),
    })
  }

  send(type: CommandType, payload?: Record<string, unknown>): string | undefined {
    if (!this.child || this.child.exitCode !== null) return undefined
    const command: Command = {
      version: 1,
      id: `ui-${++this.sequence}`,
      type,
      ...(payload ? { payload } : {}),
    }
    this.child.stdin.write(encode(command))
    return command.id
  }

  async close() {
    this.send("shutdown")
    this.child?.stdin.end()
    await this.child?.exited
  }

  private async readEvents(stream: ReadableStream<Uint8Array>) {
    const reader = stream.getReader()
    const decoder = new TextDecoder()
    let buffer = ""
    try {
      while (true) {
        const { value, done } = await reader.read()
        if (done) break
        buffer += decoder.decode(value, { stream: true })
        let newline = buffer.indexOf("\n")
        while (newline >= 0) {
          const line = buffer.slice(0, newline).trim()
          buffer = buffer.slice(newline + 1)
          if (line) {
            try {
              this.onEvent(decode(line))
            } catch (error) {
              this.onEvent({ version: 1, type: "error", payload: { message: `Bad RPC event: ${String(error)}` } })
            }
          }
          newline = buffer.indexOf("\n")
        }
      }
      if (buffer.trim()) this.onEvent(decode(buffer))
    } catch (error) {
      this.onEvent({ version: 1, type: "error", payload: { message: `RPC connection failed: ${String(error)}` } })
    } finally {
      reader.releaseLock()
      this.onEvent({ version: 1, type: "rpc_closed" })
    }
  }

  private async readLogs(stream: ReadableStream<Uint8Array>) {
    const reader = stream.getReader()
    const decoder = new TextDecoder()
    let buffer = ""
    try {
      while (true) {
        const { value, done } = await reader.read()
        if (done) break
        buffer += decoder.decode(value, { stream: true })
        const lines = buffer.split("\n")
        buffer = lines.pop() ?? ""
        for (const line of lines) if (line.trim()) this.onEvent({ version: 1, type: "log", payload: { text: line.trim() } })
      }
      if (buffer.trim()) this.onEvent({ version: 1, type: "log", payload: { text: buffer.trim() } })
    } catch (error) {
      this.onEvent({ version: 1, type: "error", payload: { message: `RPC diagnostics stream failed: ${String(error)}` } })
    } finally {
      reader.releaseLock()
    }
  }
}
