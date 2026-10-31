import { queryOptions } from "@tanstack/react-query"
import { api } from "@/lib/api"
import type { Agentpack, AgentpackWithRevisions, AgentpackConflict, Draft, LatestVersion, Package, ReplicationPeers } from "@/lib/api-types"

const FIVE_MINUTES = 5 * 60 * 1000

// A peer older than ?include=revisions ignores it and answers bare agentpacks. Peers update on their own schedule.
function withRevisions(row: AgentpackWithRevisions | Agentpack): AgentpackWithRevisions {
  return "agentpack" in row ? row : { agentpack: row, revisions: [] }
}

export const packagesQuery = (machine: string) =>
  queryOptions({
    queryKey: ["m", machine, "library", "packages"],
    queryFn: () => api<Package[]>("/api/library/packages", { machine }),
    staleTime: FIVE_MINUTES,
  })

export const agentpacksQuery = (machine: string) =>
  queryOptions({
    queryKey: ["m", machine, "library", "agentpacks"],
    queryFn: async () => (await api<(AgentpackWithRevisions | Agentpack)[]>("/api/library/agentpacks?include=revisions", { machine })).map(withRevisions),
    staleTime: FIVE_MINUTES,
  })

export const draftsQuery = queryOptions({
  queryKey: ["drafts"],
  queryFn: () => api<Draft[]>("/api/library/drafts"),
  staleTime: FIVE_MINUTES,
})

export const replicationQuery = queryOptions({
  queryKey: ["replication"],
  queryFn: () => api<ReplicationPeers>("/api/library/replication/peers"),
  staleTime: FIVE_MINUTES,
})

export const conflictQuery = (machine: string, id: string) =>
  queryOptions({
    queryKey: ["m", machine, "library", "conflict", id],
    queryFn: () => api<AgentpackConflict>(`/api/library/agentpacks/${encodeURIComponent(id)}/conflict`, { machine }),
    staleTime: 0,
  })

// Pins are in the key because they decide `current`: updating a project asks again by itself.
export const latestQuery = (request: { module: string; owner: string; repo: string; path: string; pins: string[]; machine: string }) =>
  queryOptions({
    queryKey: ["latest", request.module.toLowerCase(), request.pins],
    queryFn: async () => {
      const latest = await api<Partial<LatestVersion>>("/api/library/latest", { machine: request.machine, body: { owner: request.owner, repo: request.repo, path: request.path, pins: request.pins } })
      // Peers update on their own schedule, so the answer may not have this shape.
      if (!latest.commit) throw new Error("This machine can't look up versions yet.")
      return { current: [], exact: false, ...latest, commit: latest.commit }
    },
    staleTime: 60 * 60 * 1000,
    retry: false,
  })
