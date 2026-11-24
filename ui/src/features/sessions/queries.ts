import { queryOptions } from "@tanstack/react-query"
import { array, boolean, object, optional, string } from "@/lib/validate"
import { api, LOCAL } from "@/lib/api"
import type { ProjectRow, SessionInfo } from "@/lib/api-types"
import { useMachines, useReplicatedQueries } from "@/features/machines/queries"
import type { ListedSession } from "./group"

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

// A start shows a provisional row until the list reports the session. An accepted start whose
// row never replicates (a peer gone quiet, a session that ended at once) stops showing after this.
const PROVISIONAL_REPLICA_MS = 2 * 60 * 1000
const sessionsKey = ["m", LOCAL, "sessions"]
export const projectHistoryKey = [...sessionsKey, "project"]

export function mergeProvisional(current: SessionInfo[] | undefined, rows: SessionInfo[], now = Date.now()): SessionInfo[] {
  const ids = new Set(rows.map((row) => row.id))
  const kept = (current ?? []).filter((row) =>
    row.provisionalStart && !ids.has(row.id) &&
    (row.provisionalStart === "claim" || now - Date.parse(row.started_at ?? "") < PROVISIONAL_REPLICA_MS))
  return [...kept, ...rows]
}

export const sessionsQuery = (live: boolean) =>
  queryOptions({
    queryKey: sessionsKey,
    queryFn: async ({ client, queryKey }): Promise<SessionInfo[]> => {
      const rows = (await api<SessionInfo[] | null>("/api/sessions")) ?? []
      return mergeProvisional(client.getQueryData<SessionInfo[]>(queryKey), rows)
    },
    staleTime: 15 * 1000,
    // Events cover changes; polling covers only a dropped stream.
    refetchInterval: live ? false : 15 * 1000,
  })

export const projectHistoryQuery = (machine: string, directory: string, live: boolean) =>
  queryOptions({
    queryKey: [...projectHistoryKey, machine, directory],
    queryFn: async ({ signal }) => (await api<SessionInfo[] | null>("/api/sessions", { machine, query: { directory }, signal })) ?? [],
    staleTime: 15_000,
    refetchInterval: live ? false : 15_000,
  })

// One machine's project folders. Events invalidate it; the poll covers only a dropped stream.
export const projectsQuery = (machine: string, live = true) =>
  queryOptions({
    queryKey: ["m", machine, "projects"],
    queryFn: async (): Promise<ProjectRow[]> => parseProjects(await api<unknown>("/api/projects", { machine })),
    staleTime: 5 * 60 * 1000,
    refetchInterval: live ? false : 60 * 1000,
  })

export const selectedProjectQuery = (machine: string, directory: string) =>
  queryOptions({
    queryKey: ["m", machine, "project-details", directory],
    queryFn: async () => {
      const rows = parseProjects(await api<unknown>("/api/projects", { machine, query: { directory } }))
      return rows.find((row) => row.directory === directory) ?? null
    },
    staleTime: 30 * 1000,
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
    retry: false,
  })

// Every machine's sessions as the sidebar lists them. Offline rows are last-known.
export function useListedSessions() {
  const machines = useMachines()
  const queries = useReplicatedQueries(sessionsQuery)
  const sessions: ListedSession[] = machines.flatMap((machine) =>
    (queries.byMachine[machine.id]?.data ?? []).map((session) => ({
      ...session,
      machine: machine.id,
      machineName: machine.name,
      offline: session.reachable === false || !machine.online,
    })),
  )
  return { machines, queries, sessions }
}

export const foundQuery = (machine: string) =>
  queryOptions({
    queryKey: ["m", machine, "folders", "found"],
    queryFn: async () => parseProjects(await api<unknown>("/api/folders/found", { machine })),
    staleTime: 10 * 60 * 1000,
    retry: false,
  })

export const foldersQuery = (machine: string, dir: string) =>
  queryOptions({
    queryKey: ["m", machine, "folders", dir],
    queryFn: async () => {
      const listing = object(await api<unknown>("/api/folders", { machine, query: { path: dir } }), "folders")
      return {
        path: string(listing.path, "folders.path"),
        parent: string(listing.parent, "folders.parent"),
        row: listing.row == null ? undefined : parseProjects([listing.row])[0],
        entries: array(listing.entries, "folders.entries").map((raw) => {
          const entry = object(raw, "folders entry")
          return {
            name: string(entry.name, "folders entry name"),
            hidden: boolean(entry.hidden, "folders entry hidden"),
            pack: boolean(entry.pack, "folders entry pack"),
            git: optional(entry.git, "folders entry git", string),
          }
        }),
        truncated: boolean(listing.truncated, "folders.truncated"),
      }
    },
    staleTime: 10 * 1000,
    retry: false,
  })
