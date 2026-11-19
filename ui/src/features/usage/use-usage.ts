import { useQuery } from "@tanstack/react-query"
import { getRouteApi } from "@tanstack/react-router"
import { identityQuery, useMachines } from "@/features/machines/queries"
import { LOCAL } from "@/lib/api"
import { basename, harnessName } from "@/lib/format"
import { useNow } from "@/lib/use-now"
import { bucketOf, metricOf, rangeOf, windowOf, type UsageHour, type UsageReport } from "./model"
import { usageQuery } from "./queries"

const usageRoute = getRouteApi("/_app/usage")

// The selected range and the one before it, which the change line compares against, in one read.
export function useUsage() {
  const search = usageRoute.useSearch()
  const range = rangeOf(search.range)
  const back = Math.max(0, Math.floor(Number(search.back) || 0))
  const metric = metricOf(search.metric)
  const now = useNow(false)
  const win = windowOf(range, back, now)
  const previous = windowOf(range, back + 1, now)
  const query = useQuery(usageQuery(previous.starts[0]!, win.end))
  const hourly = query.data?.hourly ?? []
  const inWindow = (row: UsageHour) => bucketOf(win, row.hour * 1000) >= 0
  return {
    range,
    back,
    metric,
    now,
    win,
    previous,
    query,
    rows: hourly.filter(inWindow),
    previousRows: hourly.filter((row) => bucketOf(previous, row.hour * 1000) >= 0),
  }
}

// Display names for row IDs: folders come from their sessions' directories, machines from the mesh.
export function useNames(report: UsageReport | undefined) {
  const machines = useMachines()
  const identity = useQuery(identityQuery).data
  const folders = new Map((report?.sessions ?? []).map((session) => [session.folder_id, basename(session.directory)]))
  const machineId = (node: string) => (node === identity?.node.id ? LOCAL : node)
  const names = {
    project: (id: string) => folders.get(id) || (id ? "Unknown folder" : "No folder"),
    machine: (id: string) => machines.find((machine) => machine.id === machineId(id))?.name ?? id,
    agent: (id: string) => harnessName(id),
    model: (id: string) => id || "Not reported",
  }
  return {
    ...names,
    of: (kind: keyof typeof names, id: string) => names[kind](id),
    // The Sessions route names this machine "local".
    machineId,
    online: (node: string) => machines.find((machine) => machine.id === machineId(node))?.online === true,
  }
}

export type Names = ReturnType<typeof useNames>

// One fixed colour per agent, as Screen Time keeps per category; other series take slots in order.
const agentSlots: Record<string, number> = { claude: 1, codex: 2, grok: 3, cursor: 4, agent: 4, omp: 5 }
export function seriesColor(key: string, index: number, byAgent: boolean): string {
  return `var(--series-${byAgent ? (agentSlots[key] ?? 6) : (index % 5) + 1})`
}
