export const PROTOCOL_VERSION = 1

export type CommandType = "start" | "prompt" | "cancel" | "attach" | "detach" | "set_model" | "shutdown" | "tasks" | "new" | "login" | "status" | "task_create" | "task_list" | "task_attach" | "task_cancel" | "task_resume" | "send_input"
export type Command = {
  version: typeof PROTOCOL_VERSION
  id: string
  type: CommandType
  payload?: Record<string, unknown>
}

export type ServerEvent = {
  version: number
  id?: string
  type: string
  payload?: Record<string, any>
}

export function encode(command: Command): string {
  return `${JSON.stringify(command)}\n`
}

export function decode(line: string): ServerEvent {
  const event = JSON.parse(line) as ServerEvent
  if (event.version !== PROTOCOL_VERSION || typeof event.type !== "string") {
    throw new Error(`Unsupported pk RPC event: ${line.slice(0, 160)}`)
  }
  return event
}
