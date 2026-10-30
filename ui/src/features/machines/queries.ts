import { useEffect } from "react"
import { queryOptions, useQueries, useQuery, type UseQueryOptions } from "@tanstack/react-query"
import { z } from "zod"
import { ApiError, LOCAL, api } from "@/lib/api"
import type { Identity, TailnetNode, Topology } from "@/lib/api-types"

export type Machine = { id: string; name: string; online: boolean }

export const identityQuery = queryOptions({
  queryKey: ["identity"],
  queryFn: () => api<Identity>("/api/identity"),
  staleTime: 60 * 60 * 1000,
})

export const nodesQuery = queryOptions({
  queryKey: ["nodes"],
  queryFn: () => api<TailnetNode[]>("/api/nodes"),
  staleTime: 30 * 1000,
  refetchInterval: 30 * 1000,
})

const topologySchema = z.object({
  self: z.object({ id: z.string(), name: z.string(), os: z.string(), relay: z.string() }),
  peers: z.array(
    z.object({
      id: z.string(),
      name: z.string(),
      os: z.string(),
      state: z.string(),
      connection: z.string(),
      relay: z.string(),
      rtt_ms: z.number().nullish().transform((v) => v ?? undefined),
      last_seen: z.string().nullish().transform((v) => v ?? undefined),
      sessions: z.number(),
    }),
  ),
})

export const topologyQuery = queryOptions({
  queryKey: ["topology"],
  queryFn: async (): Promise<Topology> => topologySchema.parse(await api<unknown>("/api/topology")),
  staleTime: 15 * 1000,
  refetchInterval: 15 * 1000,
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
  useEffect(() => {
    for (const machine of live) seen.set(machine.id, machine)
  })
  const liveIds = new Set(live.map((machine) => machine.id))
  const offline = [...seen.values()]
    .filter((machine) => !liveIds.has(machine.id))
    .map((machine) => ({ ...machine, online: false }))
  return [...live, ...offline]
}

export type MachineQuery<T> = {
  data: T | undefined
  error: Error | null
  // Offline machines keep their cached data and are always marked stale.
  stale: boolean
  // First load for an online machine that has no data yet.
  pending: boolean
  // A peer answered 404: it predates this endpoint and must be updated.
  needsUpdate: boolean
}

export type MachineQueries<T> = {
  byMachine: Record<string, MachineQuery<T>>
  firstLoadPending: boolean
  errors: Record<string, Error>
}

// One query per machine, built from a section-2 factory. Offline machines are disabled.
export function useMachineQueries<T>(
  factory: (machine: string) => UseQueryOptions<T, Error, T, string[]>,
): MachineQueries<T> {
  const machines = useMachines()
  return useQueries({
    queries: machines.map((machine) => ({ ...factory(machine.id), enabled: machine.online })),
    combine: (results): MachineQueries<T> => {
      const byMachine: Record<string, MachineQuery<T>> = {}
      const errors: Record<string, Error> = {}
      let firstLoadPending = false
      machines.forEach((machine, i) => {
        const result = results[i]
        const error = result?.error ?? null
        const pending = machine.online && result?.data === undefined && result?.isPending === true
        if (pending) firstLoadPending = true
        if (error) errors[machine.id] = error
        byMachine[machine.id] = {
          data: result?.data,
          error,
          stale: !machine.online || result?.isStale === true,
          pending,
          needsUpdate: machine.id !== LOCAL && error instanceof ApiError && error.status === 404,
        }
      })
      return { byMachine, firstLoadPending, errors }
    },
  })
}
