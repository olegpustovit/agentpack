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

// One machine's project folders. Events invalidate it; the poll covers only a dropped stream.
export const projectsQuery = (machine: string, live = true) =>
  queryOptions({
    queryKey: ["m", machine, "projects"],
    queryFn: () => api<ProjectRow[]>("/api/projects", { machine }),
    staleTime: 5 * 60 * 1000,
    refetchInterval: live ? false : 60 * 1000,
  })
