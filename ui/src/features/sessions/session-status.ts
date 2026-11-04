import type { ListedSession } from "./group"

export function sessionGlyphState(session: ListedSession): "running" | "stopped" | "idle" | "restarting" | "offline" | "needsYou" {
  if (session.conversation_state === "needs_you") return "needsYou"
  if (session.conversation_state === "idle" && session.state.running) return "idle"
  if (session.conversation_state === "working" && session.state.running) return "running"
  if (session.offline) return "offline"
  if (session.state.restarting) return "restarting"
  if (session.state.running) return "running"
  return "stopped"
}

export function sessionStatusWord(session: ListedSession, stopping: boolean): string | undefined {
  if (stopping) return "Stopping…"
  if (session.conversation_state === "needs_you") return "Needs you"
  if (session.conversation_state === "idle" && session.state.running) return "Idle"
  if (session.offline) return "Offline"
  if (session.state.restarting) return "Restarting"
  if (session.state.running) return undefined
  return "Stopped"
}
