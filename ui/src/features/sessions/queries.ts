import { queryOptions } from "@tanstack/react-query"
import { api } from "@/lib/api"
import type { SessionInfo } from "@/lib/api-types"

export const sessionsQuery = (machine: string) =>
  queryOptions({
    queryKey: ["m", machine, "sessions"],
    queryFn: () => api<SessionInfo[]>("/api/sessions", { machine }),
    staleTime: 15 * 1000,
    refetchInterval: 15 * 1000,
  })
