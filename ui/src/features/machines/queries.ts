import { queryOptions, useQueries, useQuery, type UseQueryOptions } from "@tanstack/react-query"
import { array, boolean, number, object, optional, string } from "@/lib/validate"
import { ApiError, LOCAL, api, routeMissing } from "@/lib/api"
import type { Identity, TailnetNode, Topology } from "@/lib/api-types"
import { useEventsConnected } from "@/features/events/use-events"

export type Machine = { id: string; name: string; online: boolean; main?: boolean; needsUpdate?: boolean }

export const identityQuery = queryOptions({
  queryKey: ["identity"],
  queryFn: () => api<Identity>("/api/identity"),
  staleTime: 60 * 60 * 1000,
})

export const latestAgentpackVersionQuery = queryOptions({
  queryKey: ["agentpack-latest-version"],
  queryFn: () => api<{ version: string; url: string }>("/api/version/latest"),
  staleTime: 15 * 60 * 1000,
  retry: false,
})

export const machineUpdateQuery = (machine: string) =>
  queryOptions({
    queryKey: ["m", machine, "update"],
    queryFn: () => api<{ state: "idle" | "running" | "failed"; error: string }>("/api/node/update", { machine }),
    staleTime: 0,
    refetchInterval: (query) => query.state.data?.state === "running" ? 2000 : false,
  })

export function compareAgentpackVersions(left: string, right: string): number {
  const parse = (value: string) => {
    const match = /^v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?$/.exec(value)
    return match && { parts: match.slice(1, 4).map(Number), pre: match[4] }
  }
  const a = parse(left)
  const b = parse(right)
  if (!a || !b) return 0
  for (let i = 0; i < 3; i++) {
    const leftPart = a.parts[i] ?? 0
    const rightPart = b.parts[i] ?? 0
    if (leftPart !== rightPart) return leftPart - rightPart
  }
  if (a.pre === b.pre) return 0
  return a.pre ? -1 : 1
}

export type MachineAgent = { harness: string; installed: boolean; version?: string }
export type MachineInfo = {
  version: string
  os: string
  arch: string
  cores: number
  memory?: number
  disk_free?: number
  disk_total?: number
  agents: MachineAgent[]
}

function parseMachineInfo(raw: unknown): MachineInfo {
  const body = object(raw, "machine")
  return {
    version: string(body.version, "version"),
    os: string(body.os, "os"),
    arch: string(body.arch, "arch"),
    cores: number(body.cores, "cores"),
    memory: optional(body.memory, "memory", number),
    disk_free: optional(body.disk_free, "disk_free", number),
    disk_total: optional(body.disk_total, "disk_total", number),
    agents: array(body.agents, "agents").map((item, index) => {
      const agent = object(item, `agents[${index}]`)
      return {
        harness: string(agent.harness, `agents[${index}].harness`),
        installed: boolean(agent.installed, `agents[${index}].installed`),
        version: optional(agent.version, `agents[${index}].version`, string),
      }
    }),
  }
}

// System facts and agent CLI versions, read on the machine itself. A peer without the route answers 404.
export const machineInfoQuery = (machine: string) =>
  queryOptions({
    queryKey: ["m", machine, "machine"],
    queryFn: async () => parseMachineInfo(await api<unknown>("/api/machine", { machine })),
    staleTime: 60 * 1000,
    retry: (count, error) => !(error instanceof ApiError && error.status === 404) && count < 2,
  })

// The running version comes from identity for this machine and from topology for peers, so the
// list and the detail page agree, and this machine is flagged too.
export function useAgentpackVersions(): (machine: string) => { version?: string; latest?: string; outdated: boolean } {
  const live = useEventsConnected()
  const local = useQuery(identityQuery).data?.node.version
  const peers = useQuery(topologyQuery(live)).data?.peers
  const latest = useQuery(latestAgentpackVersionQuery).data?.version
  return (machine) => {
    const version = machine === LOCAL ? local : peers?.find((peer) => peer.id === machine)?.version
    return { version, latest, outdated: !!version && !!latest && compareAgentpackVersions(version, latest) < 0 }
  }
}

// A peer's identity is proxied through the gateway, so OS and version come from the machine itself.
export const machineIdentityQuery = (machine: string) =>
  queryOptions({
    queryKey: ["m", machine, "identity"],
    queryFn: () => api<Identity>("/api/identity", { machine }),
    staleTime: 60 * 60 * 1000,
  })

export const nodesQuery = (live: boolean, discover = false) =>
  queryOptions({
    queryKey: discover ? ["nodes", "discovery"] : ["nodes"],
    queryFn: () => api<TailnetNode[]>("/api/nodes", {query: discover ? {discover: "1"} : {}}),
    staleTime: 30 * 1000,
    refetchInterval: live ? false : 30 * 1000,
  })

// True until the first machine list answers.
export function useDiscovering(): boolean {
  const live = useEventsConnected()
  return useQuery(nodesQuery(live)).isPending
}

function parseTopology(raw: unknown): Topology {
  const body = object(raw, "topology")
  const self = object(body.self, "self")
  return {
    self: { id: string(self.id, "self.id"), name: string(self.name, "self.name"), os: string(self.os, "self.os"), relay: string(self.relay, "self.relay"), main: optional(self.main, "self.main", boolean) },
    peers: array(body.peers, "peers").map((item, index) => {
      const peer = object(item, `peers[${index}]`)
      const at = (key: string) => `peers[${index}].${key}`
      return {
        id: string(peer.id, at("id")),
        main: optional(peer.main, at("main"), boolean),
        name: string(peer.name, at("name")),
        os: string(peer.os, at("os")),
        state: string(peer.state, at("state")),
        connection: string(peer.connection, at("connection")),
        relay: string(peer.relay, at("relay")),
        rtt_ms: optional(peer.rtt_ms, at("rtt_ms"), number),
        last_seen: optional(peer.last_seen, at("last_seen"), string),
        version: optional(peer.version, at("version"), string),
        sessions: number(peer.sessions, at("sessions")),
      }
    }),
  }
}

export const topologyQuery = (live: boolean) =>
  queryOptions({
    queryKey: ["topology"],
    queryFn: async (): Promise<Topology> => parseTopology(await api<unknown>("/api/topology")),
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
  if (identity) found.push({ id: LOCAL, name: identity.node.name, online: true, main: identity.node.main })
  for (const node of nodes ?? []) {
    if (identity && node.id === identity.node.id) continue
    found.push({ id: node.id, name: node.name, online: node.online, main: node.main, needsUpdate: node.needs_update })
  }
  // Idempotent, so recording during render is safe.
  for (const machine of found) seen.set(machine.id, machine)
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
          needsUpdate: machine.id !== LOCAL && routeMissing(error),
          updatedAt: result?.dataUpdatedAt ?? 0,
        }
      })
      return { byMachine, firstLoadPending, errors }
    },
  })
}

// One local read includes replicated rows, even when the originating machine is asleep.
export function useReplicatedQueries<T extends { node_id?: string; reachable?: boolean }>(
  factory: (machine: string, live: boolean) => UseQueryOptions<T[], Error, T[], string[]>,
): MachineQueries<T[]> {
  const live = useEventsConnected()
  const machines = useMachines()
  const identity = useQuery(identityQuery).data
  const result = useQuery(factory(LOCAL, live))
  const byMachine: Record<string, MachineQuery<T[]>> = {}
  const errors: Record<string, Error> = {}
  const groups = partitionReplicatedRows(result.data ?? [], identity?.node.id)
  for (const machine of machines) {
    const rows = groups[machine.id] ?? []
    byMachine[machine.id] = {
      data: result.data === undefined ? undefined : rows,
      error: result.error,
      stale: result.isStale || rows.some((row) => row.reachable === false),
      pending: result.isPending,
      needsUpdate: false,
      updatedAt: result.dataUpdatedAt,
    }
  }
  if (result.error) errors[LOCAL] = result.error
  return { byMachine, errors, firstLoadPending: result.isPending }
}

export function partitionReplicatedRows<T extends { node_id?: string }>(rows: T[], localNode?: string): Record<string, T[]> {
  const groups: Record<string, T[]> = {}
  for (const row of rows) {
    const machine = !row.node_id || row.node_id === localNode ? LOCAL : row.node_id
    ;(groups[machine] ??= []).push(row)
  }
  return groups
}
