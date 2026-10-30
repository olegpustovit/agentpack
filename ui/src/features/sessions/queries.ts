import { queryOptions } from "@tanstack/react-query"
import { api } from "@/lib/api"
import type { SessionInfo } from "@/lib/api-types"

export const sessionsQuery = (machine: string, live: boolean) =>
  queryOptions({
    queryKey: ["m", machine, "sessions"],
    queryFn: () => api<SessionInfo[]>("/api/sessions", { machine }),
    staleTime: 15 * 1000,
    // Safety-net poll while the event stream is up; events cover the rest.
    refetchInterval: live ? 60 * 1000 : 15 * 1000,
  })
