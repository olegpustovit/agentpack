import { queryOptions, useQuery } from "@tanstack/react-query"
import { LOCAL, api } from "./api"

export type Machine = { id: string; name: string; online: boolean }

type Identity = { node: { id: string; name: string } }
type TailnetNode = { id: string; name: string; online: boolean }

export const identityQuery = queryOptions({
  queryKey: ["identity"],
  queryFn: () => api<Identity>("/api/identity"),
  staleTime: 60 * 60 * 1000,
})

export const nodesQuery = queryOptions({
  queryKey: ["nodes"],
  queryFn: () => api<TailnetNode[]>("/api/nodes"),
  staleTime: 30 * 1000,
})

// Machines seen in this tab. Module state is per tab and resets on reload.
// shortcut: no persistence across reloads, upgrade if offline machines must survive a reload.
const seen = new Map<string, Machine>()

export function useMachines(): Machine[] {
  const identity = useQuery(identityQuery).data
  const nodes = useQuery(nodesQuery).data
  const live: Machine[] = []
  if (identity) live.push({ id: LOCAL, name: identity.node.name, online: true })
  for (const node of nodes ?? []) {
    if (identity && node.id === identity.node.id) continue
    live.push({ id: node.id, name: node.name, online: node.online })
  }
  for (const machine of live) seen.set(machine.id, machine)
  const liveIds = new Set(live.map((machine) => machine.id))
  const offline = [...seen.values()]
    .filter((machine) => !liveIds.has(machine.id))
    .map((machine) => ({ ...machine, online: false }))
  return [...live, ...offline]
}
