import type { ListedSession } from "./group"

export function sessionGlyphState(session: ListedSession): "running" | "stopped" | "idle" | "restarting" | "offline" | "needsYou" {
  if (session.archived_at) return "stopped"
  if (session.offline) return "offline"
  if (session.conversation_state === "needs_you") return "needsYou"
  if (session.conversation_state === "idle" && session.state.running) return "idle"
  if (session.conversation_state === "working" && session.state.running) return "running"
  if (session.state.restarting) return "restarting"
  if (session.state.running) return "running"
  return "stopped"
}

export function sessionStatusWord(session: ListedSession, stopping: boolean): string | undefined {
  if (session.archived_at) return "Archived"
  if (session.offline) return "Offline"
  if (stopping) return "Stopping…"
  if (session.conversation_state === "needs_you") return "Needs you"
  if (session.conversation_state === "idle" && session.state.running) return "Idle"
  if (session.state.restarting) return "Restarting"
  if (session.state.running) return undefined
  return "Stopped"
}

// Idle chat with activity newer than the last time this browser opened it (or the first-visit baseline).
export function isUnread(session: ListedSession, seenAt: number | undefined): boolean {
  if (sessionGlyphState(session) !== "idle" || !session.last_used) return false
  const used = Date.parse(session.last_used)
  return !Number.isNaN(used) && (seenAt === undefined || used > seenAt)
}

const SEEN_KEY = "agentpack.sessions.seen"

// Per-viewer convenience: storage may be blocked, so every access falls back to nothing.
export function loadSeen(): Record<string, number> {
  try {
    const stored = localStorage.getItem(SEEN_KEY)
    // First visit: rows never opened count as seen now, so old history isn't all "unread".
    if (stored === null) {
      const baseline = { "": Date.now() }
      saveSeen(baseline)
      return baseline
    }
    const value: unknown = JSON.parse(stored)
    return value && typeof value === "object" ? (value as Record<string, number>) : {}
  } catch {
    return {}
  }
}

export function saveSeen(seen: Record<string, number>) {
  try {
    localStorage.setItem(SEEN_KEY, JSON.stringify(seen))
  } catch {
    // Unread markers just reset on reload.
  }
}
