export const terminalCommandEvent = "agentpack:terminal-command"
export type TerminalCommand = "toggle" | "new"

export type TerminalRequest = { command: TerminalCommand; repeat: boolean }

export function requestTerminal(command: TerminalCommand, repeat = false) {
  return !window.dispatchEvent(new CustomEvent<TerminalRequest>(terminalCommandEvent, { detail: { command, repeat }, cancelable: true }))
}

export function terminalShortcut(event: KeyboardEvent, isMac: boolean) {
  if (event.altKey || event.isComposing) return
  if (event.code === "Backquote" && event.ctrlKey && !event.metaKey) return event.shiftKey ? "new" : "toggle"
  if (event.key.toLowerCase() === "j" && (isMac ? event.metaKey && !event.ctrlKey : event.ctrlKey && !event.metaKey)) {
    return event.shiftKey ? "next" : "toggle"
  }
}


type Chat = { machine: string; sessionId: string }
let lastChat: Chat | undefined
let pending: { chat: Chat; command: "open" | "new" } | undefined

export function rememberChatTerminal(chat: Chat) {
  lastChat = chat
}

export function queueLastChatTerminal(command: TerminalCommand) {
  if (!lastChat) return
  pending = { chat: lastChat, command: command === "toggle" ? "open" : "new" }
  return lastChat
}

export function takeChatTerminalCommand(machine: string, sessionId: string) {
  if (pending?.chat.machine !== machine || pending.chat.sessionId !== sessionId) return
  const command = pending.command
  pending = undefined
  return command
}
