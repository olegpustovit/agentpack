import { queryOptions } from "@tanstack/react-query"
import { api, LOCAL } from "@/lib/api"
import type { SessionInfo } from "@/lib/api-types"

export function chatShellsQuery(machine: string, sessionId: string) {
  return queryOptions({
    queryKey: ["m", LOCAL, "sessions", "shells", machine, sessionId],
    queryFn: async () => (await api<SessionInfo[] | null>(`/api/sessions/${encodeURIComponent(sessionId)}/shells`, { machine })) ?? [],
    retry: false,
  })
}
