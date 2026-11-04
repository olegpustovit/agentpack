import { ApiError, LOCAL } from "@/lib/api"
import type { LogEntry } from "./model"

async function conversationLog(machine: string, sessionId: string, since = 0): Promise<LogEntry[]> {
  const url = new URL(`/api/sessions/${encodeURIComponent(sessionId)}/conversation`, location.origin)
  if (machine && machine !== LOCAL) url.searchParams.set("node", machine)
  if (since > 0) url.searchParams.set("since", String(since))
  const res = await fetch(url)
  const text = await res.text()
  if (!res.ok) {
    throw new ApiError(text.slice(0, 200) || res.statusText, res.status)
  }
  const entries: LogEntry[] = []
  for (const line of text.split("\n")) {
    if (!line) continue
    entries.push(JSON.parse(line) as LogEntry)
  }
  return entries
}

export function conversationEventsQuery(machine: string, sessionId: string, _live = false) {
  return {
    queryKey: ["conversation", machine, sessionId] as const,
    queryFn: (): Promise<LogEntry[]> => conversationLog(machine, sessionId),
    refetchInterval: false as false | number,
  }
}
export function conversationNeedsUpdate(...errors: unknown[]): boolean {
  return errors.some((error) => error instanceof ApiError && error.status === 404)
}
export function conversationErrorMessage(machine: string, action: string, error: unknown): string {
  if (conversationNeedsUpdate(error)) {
    return `${machine === LOCAL ? "Gateway" : "Peer"} needs an update to ${action}.`
  }
  const detail = error instanceof Error ? error.message : String(error)
  return `Couldn't ${action}: ${detail}`
}
