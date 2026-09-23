export const PROTOCOL_VERSION = 1

export type CommandType = "start" | "prompt" | "steer" | "cancel" | "attach" | "detach" | "set_model" | "shutdown" | "tasks" | "new" | "login" | "status" | "task_create" | "task_list" | "task_attach" | "task_cancel" | "task_resume" | "send_input" | "clipboard_paste" | "clipboard_write" | "skills" | "skill_search" | "skill_source_list" | "skill_install" | "skill_installed_list" | "skill_remove" | "skill_cancel" | "plugins_list" | "plugins_enable" | "plugins_disable" | "plugins_discover" | "plugins_install" | "plugin_source_cancel" | "plugin_commands_list" | "plugin_command_execute" | "plugin_command_cancel" | "mcp_list" | "mcp_add" | "mcp_remove" | "mcp_login" | "mcp_logout" | "providers_list" | "provider_models" | "provider_select" | "provider_add" | "provider_remove" | "provider_default" | "tools" | "history_before" | "history_cancel" | "release_status" | "update" | "update_cancel" | "rollback" | "reload" | "reload_exit"
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
