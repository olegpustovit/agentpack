import { useEffect, useState } from "react"
import { lastActivity, rowKey, type ListedSession } from "./group"

// A chat "+" just started: the gateway answered and the agent is still coming up.
export function isStarting(session: Pick<ListedSession, "conversation_state" | "ended_at">): boolean {
  return session.conversation_state === "starting" && !session.ended_at
}

export function sessionGlyphState(session: ListedSession): "running" | "stopped" | "idle" | "restarting" | "offline" | "needsYou" | "starting" {
  if (session.archived_at) return "stopped"
  if (session.offline) return "offline"
  if (isStarting(session)) return "starting"
  // A stopped chat can keep a stale needs_you; nothing can answer it any more.
  if (!session.state.running && !session.state.restarting) return "stopped"
  if (session.conversation_state === "needs_you") return "needsYou"
  if (session.conversation_state === "idle" && session.state.running) return "idle"
  if (session.conversation_state === "working" && session.state.running) return "running"
  if (session.state.restarting) return "restarting"
  if (session.state.running) return "running"
  return "stopped"
}

export type GlyphState = ReturnType<typeof sessionGlyphState>

export const HOLD_MS = 1500

// The server's state, shown as it is: a finished turn reads idle at once. The one exception is a
// live chat reading "stopped" for a refetch while its worker is slow to answer, shown only once it lasts.
export function useSteadyGlyph(state: GlyphState, chat: boolean): GlyphState {
  const [shown, setShown] = useState(state)
  const blip = chat && state === "stopped"
  useEffect(() => {
    const timer = setTimeout(() => setShown(state), blip ? HOLD_MS : 0)
    return () => clearTimeout(timer)
  }, [state, blip])
  return blip ? shown : state
}

export function sessionStatusWord(session: ListedSession, stopping: boolean): string | undefined {
  if (session.archived_at) return "Cold storage"
  if (session.offline) return "Offline"
  if (stopping) return "Stopping…"
  if (isStarting(session)) return undefined
  if (session.state.restarting) return "Restarting"
  if (!session.state.running) return "Stopped"
  if (session.conversation_state === "needs_you") return "Needs you"
  if (session.state.running) return undefined
  return "Stopped"
}

// ⌘⇧J: the session after the open one that waits on you, in list order, wrapping around.
export function nextNeedsYou(sessions: ListedSession[], openKey: string | undefined): ListedSession | undefined {
  const waiting = sessions.filter((session) => sessionGlyphState(session) === "needsYou")
  const at = waiting.findIndex((session) => rowKey(session) === openKey)
  return waiting[(at + 1) % waiting.length]
}

// Idle chat with activity newer than the last time this browser opened it (or the first-visit baseline).
export function isUnread(session: ListedSession, seenAt: number | undefined): boolean {
  const when = lastActivity(session)
  if (sessionGlyphState(session) !== "idle" || !when) return false
  const used = Date.parse(when)
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
