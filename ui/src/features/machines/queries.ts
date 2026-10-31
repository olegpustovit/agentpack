import { useEffect } from "react"
import { queryOptions, useQueries, useQuery, type UseQueryOptions } from "@tanstack/react-query"
import { z } from "zod"
import { ApiError, LOCAL, api } from "@/lib/api"
import type { Identity, TailnetNode, Topology } from "@/lib/api-types"
import { useEventsConnected } from "@/features/events/use-events"

export type Machine = { id: string; name: string; online: boolean }

export const identityQuery = queryOptions({
  queryKey: ["identity"],
  queryFn: () => api<Identity>("/api/identity"),
  staleTime: 60 * 60 * 1000,
})

// A peer's identity is proxied through the gateway, so OS and version come from the machine itself.
export const machineIdentityQuery = (machine: string) =>
  queryOptions({
    queryKey: ["m", machine, "identity"],
    queryFn: () => api<Identity>("/api/identity", { machine }),
    staleTime: 60 * 60 * 1000,
  })

export const nodesQuery = (live: boolean) =>
  queryOptions({
    queryKey: ["nodes"],
    queryFn: () => api<TailnetNode[]>("/api/nodes"),
    staleTime: 30 * 1000,
    refetchInterval: live ? false : 30 * 1000,
  })

// True until the first machine list answers.
export function useDiscovering(): boolean {
  const live = useEventsConnected()
  return useQuery(nodesQuery(live)).isPending
}

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
      version: z.string().nullish().transform((v) => v ?? undefined),
      sessions: z.number(),
    }),
  ),
})

export const topologyQuery = (live: boolean) =>
  queryOptions({
    queryKey: ["topology"],
    queryFn: async (): Promise<Topology> => topologySchema.parse(await api<unknown>("/api/topology")),
    staleTime: 15 * 1000,
    refetchInterval: live ? false : 15 * 1000,
  })

// Machines seen in this tab. Module state is per tab and resets on reload.
// shortcut: no persistence across reloads, upgrade if offline machines must survive a reload.
const seen = new Map<string, Machine>()

export function useMachines(): Machine[] {
  const live = useEventsConnected()
  const identity = useQuery(identityQuery).data
  const nodes = useQuery(nodesQuery(live)).data
  const found: Machine[] = []
  if (identity) found.push({ id: LOCAL, name: identity.node.name, online: true })
  for (const node of nodes ?? []) {
    if (identity && node.id === identity.node.id) continue
    found.push({ id: node.id, name: node.name, online: node.online })
  }
  useEffect(() => {
    for (const machine of found) seen.set(machine.id, machine)
  })
  const foundIds = new Set(found.map((machine) => machine.id))
  const offline = [...seen.values()]
    .filter((machine) => !foundIds.has(machine.id))
    .map((machine) => ({ ...machine, online: false }))
  return [...found, ...offline]
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
  // When the cached data last succeeded, in ms since epoch; 0 when none yet.
  updatedAt: number
}

export type MachineQueries<T> = {
  byMachine: Record<string, MachineQuery<T>>
  firstLoadPending: boolean
  errors: Record<string, Error>
}

// One query per machine, built from a section-2 factory. Offline machines are disabled.
export function useMachineQueries<T>(
  factory: (machine: string, live: boolean) => UseQueryOptions<T, Error, T, string[]>,
): MachineQueries<T> {
  const live = useEventsConnected()
  const machines = useMachines()
  return useQueries({
    queries: machines.map((machine) => ({ ...factory(machine.id, live), enabled: machine.online })),
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
          updatedAt: result?.dataUpdatedAt ?? 0,
        }
      })
      return { byMachine, firstLoadPending, errors }
    },
  })
}
