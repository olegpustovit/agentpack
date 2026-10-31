import { queryOptions } from "@tanstack/react-query"
import { z } from "zod"
import { api } from "@/lib/api"
import type { ProjectRow, SessionInfo } from "@/lib/api-types"

// Go sends a nil slice as null, and a peer may predate a field. Unknown fields pass through.
const list = z.array(z.looseObject({})).nullish().transform((value) => value ?? [])
const projectsSchema = z.array(
  z.looseObject({
    directory: z.string(),
    name: z.string().catch(""),
    available: z.boolean().catch(false),
    launches: list,
    packages: list,
    mcp: list,
  }),
)

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
    queryFn: async (): Promise<ProjectRow[]> => projectsSchema.parse(await api<unknown>("/api/projects", { machine })) as ProjectRow[],
    staleTime: 5 * 60 * 1000,
    refetchInterval: live ? false : 60 * 1000,
  })
