import { queryOptions } from "@tanstack/react-query"
import { api, ApiError, LOCAL, request, routeMissing } from "@/lib/api"
import type { SessionInfo } from "@/lib/api-types"
import type { AgentCommand } from "./composer-hints"
import type { LogEntry } from "./model"

class ArchiveUnavailableError extends ApiError {
  constructor(readonly main: string) {
    super("archive unavailable", 503)
  }
}

export function archiveUnavailableMessage(error: unknown): string | undefined {
  if (error instanceof ArchiveUnavailableError) return `Archived history on ${error.main} is unavailable right now`
}

// truncated: older entries exist that this response left out.
export type ConversationPage = { entries: LogEntry[]; truncated: boolean }

async function conversationLog(machine: string, sessionId: string, before = 0): Promise<ConversationPage> {
  let page: { res: Response; text: string }
  try {
    page = await request(`/api/sessions/${encodeURIComponent(sessionId)}/conversation`, { machine, query: before > 0 ? { before: String(before) } : undefined })
  } catch (error) {
    if (error instanceof ApiError && error.status === 503 && error.message === "archive unavailable") {
      throw new ArchiveUnavailableError(error.headers?.get("X-Agentpack-Archive-Main") || "main gateway")
    }
    throw error
  }
  const { res, text } = page
  const entries: LogEntry[] = []
  for (const line of text.split("\n")) {
    if (!line) continue
    entries.push(JSON.parse(line) as LogEntry)
  }
  return { entries, truncated: res.headers.get("X-Agentpack-Truncated") === "true" }
}

// The page of history just before seq. A peer that predates paging ignores ?before= and answers
// with its newest tail, which reads as Needs update rather than as duplicated messages.
export async function olderConversationPage(machine: string, sessionId: string, before: number): Promise<ConversationPage> {
  const page = await conversationLog(machine, sessionId, before)
  if (page.entries.some((entry) => entry.seq >= before)) throw new ApiError("404 page not found", 404)
  return page
}

// Older pages stay loaded while the tail refetches; anything the tail already holds wins.
export function joinHistory(older: LogEntry[], tail: LogEntry[]): LogEntry[] {
  const first = tail[0]?.seq ?? Infinity
  return older.length === 0 ? tail : [...older.filter((entry) => entry.seq < first), ...tail]
}

export function mergeConversationPages(current: ConversationPage | undefined, next: ConversationPage): ConversationPage {
  if (!current?.entries.length) return next
  if (next.entries.length >= current.entries.length && next.entries[0] === current.entries[0] && next.entries[current.entries.length - 1] === current.entries.at(-1)) return next
  const entries = new Map(current.entries.map((entry) => [entry.seq, entry]))
  for (const entry of next.entries) entries.set(entry.seq, entry)
  return {
    entries: Array.from(entries.values()).sort((left, right) => left.seq - right.seq),
    truncated: current.truncated || next.truncated,
  }
}

export function conversationEventsQuery(machine: string, sessionId: string) {
  return {
    queryKey: ["conversation", machine, sessionId] as const,
    queryFn: (): Promise<ConversationPage> => conversationLog(machine, sessionId),
    refetchOnWindowFocus: false,
  }
}
export function conversationNeedsUpdate(...errors: unknown[]): boolean {
  return errors.some(routeMissing)
}
export function conversationErrorMessage(machine: string, action: string, error: unknown): string {
  if (conversationNeedsUpdate(error)) {
    return `${machine === LOCAL ? "Gateway" : "Peer"} needs an update to ${action}.`
  }
  const detail = error instanceof Error ? error.message : String(error)
  return `Couldn't ${action}: ${detail}`
}

export function agentSnapshotQuery(machine: string, info?: Pick<SessionInfo, "directory" | "definition_root" | "harness" | "mode">) {
  const definition = info?.definition_root || info?.directory || ""
  const harness = info?.harness ?? ""
  const mode = info?.mode || "default"
  return queryOptions({
    queryKey: ["agent-snapshot", machine, harness, definition, mode] as const,
    queryFn: () => api<{ config: unknown; commands?: { availableCommands?: AgentCommand[] } } | null>("/api/agent-snapshot", {
      query: { owner: machine, harness, definition_root: definition, mode },
    }),
    enabled: Boolean(harness && definition),
    staleTime: 60_000,
    retry: false,
  })
}
