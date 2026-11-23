import { queryOptions } from "@tanstack/react-query"
import { api } from "@/lib/api"
import type { SessionInfo } from "@/lib/api-types"

export function chatShellsQuery(machine: string, sessionId: string) {
  return queryOptions({
    // Not under the sessions key: every session-list refresh would refetch each open chat's shells.
    queryKey: ["m", machine, "shells", sessionId],
    queryFn: async () => (await api<SessionInfo[] | null>(`/api/sessions/${encodeURIComponent(sessionId)}/shells`, { machine })) ?? [],
    retry: false,
  })
}
