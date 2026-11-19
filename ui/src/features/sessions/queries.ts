import { queryOptions } from "@tanstack/react-query"
import { array, object, optional, string } from "@/lib/validate"
import { api, LOCAL } from "@/lib/api"
import type { ProjectRow, SessionInfo } from "@/lib/api-types"

// Go sends a nil slice as null, and a peer may predate a field. Unknown fields pass through.
function parseProjects(raw: unknown): ProjectRow[] {
  return array(raw, "projects").map((item, index) => {
    const row = object(item, `projects[${index}]`)
    const list = (key: string) => (optional(row[key], `projects[${index}].${key}`, array) ?? []).map((entry, at) => object(entry, `projects[${index}].${key}[${at}]`))
    return {
      ...row,
      directory: string(row.directory, `projects[${index}].directory`),
      name: typeof row.name === "string" ? row.name : "",
      available: typeof row.available === "boolean" ? row.available : false,
      launches: list("launches"),
      packages: list("packages"),
      mcp: list("mcp"),
      modes: list("modes"),
    } as ProjectRow
  })
}

export const sessionsQuery = (_machine: string, live: boolean) =>
  queryOptions({
    queryKey: ["m", LOCAL, "sessions"],
    queryFn: () => api<SessionInfo[]>("/api/sessions"),
    staleTime: 15 * 1000,
    // Events cover changes; polling covers only a dropped stream.
    refetchInterval: live ? false : 15 * 1000,
  })

// One machine's project folders. Events invalidate it; the poll covers only a dropped stream.
export const projectsQuery = (machine: string, live = true) =>
  queryOptions({
    queryKey: ["m", machine, "projects"],
    queryFn: async (): Promise<ProjectRow[]> => parseProjects(await api<unknown>("/api/projects", { machine })),
    staleTime: 5 * 60 * 1000,
    refetchInterval: live ? false : 60 * 1000,
  })
