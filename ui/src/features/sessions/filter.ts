import { harnessName } from "@/lib/format"
import { matchesSession, type ListedSession } from "./group"
import { sessionGlyphState } from "./session-status"

// The sidebar's view: a filter (q, agent, status, host) and a grouping. All of it lives in the URL.
export type SessionStatus = "needs" | "working" | "idle" | "stopped" | "offline"
export type SessionFilter = { q: string; agents: string[]; statuses: SessionStatus[]; host?: string }
export type FilterKey = "q" | "agent" | "status" | "host"

export const statusLabels: Record<SessionStatus, string> = { needs: "Needs you", working: "Working", idle: "Idle", stopped: "Stopped", offline: "Offline" }
const statuses = Object.keys(statusLabels) as SessionStatus[]

export function statusOf(session: ListedSession): SessionStatus {
  switch (sessionGlyphState(session)) {
    case "needsYou": return "needs"
    case "running": case "starting": case "restarting": return "working"
    case "idle": case "waiting": return "idle"
    case "offline": return "offline"
    default: return "stopped"
  }
}

const list = (value: unknown) => typeof value === "string" ? value.split(",").filter(Boolean) : []

export function readFilter(search: Record<string, unknown>): SessionFilter {
  return {
    q: typeof search.q === "string" ? search.q : "",
    agents: list(search.agent),
    statuses: list(search.status).filter((value): value is SessionStatus => statuses.includes(value as SessionStatus)),
    host: typeof search.host === "string" && search.host ? search.host : undefined,
  }
}

// The URL form of a filter change; empty values drop their key.
export function filterSearch(patch: Partial<SessionFilter>): Partial<Record<FilterKey, string | undefined>> {
  const out: Partial<Record<FilterKey, string | undefined>> = {}
  if ("q" in patch) out.q = patch.q || undefined
  if ("agents" in patch) out.agent = patch.agents?.join(",") || undefined
  if ("statuses" in patch) out.status = patch.statuses?.join(",") || undefined
  if ("host" in patch) out.host = patch.host || undefined
  return out
}

export const clearedFilter = { q: undefined, agent: undefined, status: undefined, host: undefined }

export function matchesFilter(session: ListedSession, filter: SessionFilter): boolean {
  return matchesSession(session, filter.q)
    && (filter.agents.length === 0 || filter.agents.includes(session.harness))
    && (filter.statuses.length === 0 || filter.statuses.includes(statusOf(session)))
    && (!filter.host || session.machine === filter.host)
}

// One token per active dimension, in popover order. Each removes its own dimension.
export type FilterToken = { key: FilterKey; label: string; value: string }

export function filterTokens(filter: SessionFilter, machineName: (id: string) => string): FilterToken[] {
  const more = (names: string[]) => names.length > 1 ? `${names[0]} +${names.length - 1}` : names[0]!
  const tokens: FilterToken[] = []
  if (filter.q.trim()) tokens.push({ key: "q", label: "Contains", value: `“${filter.q.trim()}”` })
  if (filter.statuses.length) tokens.push({ key: "status", label: "Status", value: more(filter.statuses.map((status) => statusLabels[status])) })
  if (filter.agents.length) tokens.push({ key: "agent", label: "Agent", value: more(filter.agents.map(harnessName)) })
  if (filter.host) tokens.push({ key: "host", label: "Machine", value: machineName(filter.host) })
  return tokens
}

// How many sessions each option would show, counted across every listed session.
export function countBy<K extends string>(sessions: ListedSession[], key: (session: ListedSession) => K): Map<K, number> {
  const counts = new Map<K, number>()
  for (const session of sessions) counts.set(key(session), (counts.get(key(session)) ?? 0) + 1)
  return counts
}
