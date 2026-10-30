import { queryOptions } from "@tanstack/react-query"
import { api } from "@/lib/api"
import type { ProjectRow, SessionInfo } from "@/lib/api-types"

export const sessionsQuery = (machine: string, live: boolean) =>
  queryOptions({
    queryKey: ["m", machine, "sessions"],
    queryFn: () => api<SessionInfo[]>("/api/sessions", { machine }),
    staleTime: 15 * 1000,
    // Safety-net poll while the event stream is up; events cover the rest.
    refetchInterval: live ? 60 * 1000 : 15 * 1000,
  })

// One machine's recent folders. Only the New session sheet reads it, so it does not poll.
export const projectsQuery = (machine: string) =>
  queryOptions({
    queryKey: ["m", machine, "projects"],
    queryFn: () => api<ProjectRow[]>("/api/projects", { machine }),
    staleTime: 15 * 1000,
  })
