import { queryOptions } from "@tanstack/react-query"
import { api } from "@/lib/api"
import type { Agentpack, AgentpackWithRevisions, Draft, Package, ReplicationPeers } from "@/lib/api-types"

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
