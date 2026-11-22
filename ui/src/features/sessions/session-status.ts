import { useEffect, useState } from "react"
import type { WaitItem } from "@/lib/api-types"
import { lastActivity, rowKey, type ListedSession } from "./group"

// A chat "+" just started: the gateway answered and the agent is still coming up.
export function isStarting(session: Pick<ListedSession, "conversation_state" | "ended_at">): boolean {
  return session.conversation_state === "starting" && !session.ended_at
}

// A wake-up may fire this late (internal/acp wakeGrace); `until` includes it.
const WAKE_GRACE_MS = 2 * 60_000

// Background work the chat will wake for and that has not passed its deadline.
export function openWaits(session: Pick<ListedSession, "waiting">, now = Date.now()): WaitItem[] {
  return (session.waiting ?? []).filter((item) => !item.until || item.until > now)
}

// The row's reason for waiting: work still running first, then an asked chat, then a timer.
export function waitingWord(items: WaitItem[], now = Date.now()): string | undefined {
  const running = items.filter((item) => item.kind !== "reply" && item.kind !== "wake").length
  if (running) return `${running} running`
  if (items.some((item) => item.kind === "reply")) return "Awaits reply"
  const wake = items.find((item) => item.kind === "wake")
  if (!wake?.until) return undefined
  const minutes = Math.max(0, Math.round((wake.until - WAKE_GRACE_MS - now) / 60_000))
  return minutes ? `Wakes in ${minutes} m` : "Wakes soon"
}

// What the chat waits for, as nouns ("2 background tasks · a reply from X"), for Home and the chat.
export function waitingDetail(items: WaitItem[], chatName: (id: string) => string | undefined, now = Date.now()): string {
  const parts: string[] = []
  const running = items.filter((item) => item.kind !== "reply" && item.kind !== "wake").length
  if (running) parts.push(running === 1 ? "1 background task" : `${running} background tasks`)
  for (const item of items) {
    if (item.kind === "reply") parts.push(`a reply from ${(item.peer && chatName(item.peer)) || "another chat"}`)
  }
  const wake = waitingWord(items.filter((item) => item.kind === "wake"), now)
  if (wake) parts.push(wake === "Wakes soon" ? "a wake-up soon" : `a wake-up ${wake.slice("Wakes ".length)}`)
  return parts.join(" · ")
}

export function sessionGlyphState(session: ListedSession): "running" | "stopped" | "idle" | "waiting" | "restarting" | "offline" | "needsYou" | "starting" {
  if (session.archived_at) return "stopped"
  if (session.offline) return "offline"
  if (isStarting(session)) return "starting"
  // A stopped chat can keep a stale needs_you; nothing can answer it any more.
  if (!session.state.running && !session.state.restarting) return "stopped"
  if (session.conversation_state === "needs_you") return "needsYou"
  if (session.conversation_state === "idle" && session.state.running) return openWaits(session).length ? "waiting" : "idle"
  if (session.conversation_state === "working" && session.state.running) return "running"
  if (session.state.restarting) return "restarting"
  // A terminal whose agent reports no state (no hooks for its harness) is open, not working.
  if (session.state.running) return session.transport === "acp" ? "running" : "idle"
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
  // A terminal without reported state has no turns to finish, so it is never unread.
  if (sessionGlyphState(session) !== "idle" || !when || !session.conversation_state) return false
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
