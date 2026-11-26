import { useQuery } from "@tanstack/react-query"
import { getRouteApi } from "@tanstack/react-router"
import { identityQuery, useMachines } from "@/features/machines/queries"
import { LOCAL } from "@/lib/api"
import { harnessName } from "@/lib/format"
import { useNow } from "@/lib/use-now"
import { bucketOf, metricOf, rangeOf, windowOf, type UsageHour } from "./model"
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
    // The current period has only run so far; compare it with the same stretch of the one before.
    previousRows: hourly.filter((row) => bucketOf(previous, row.hour * 1000) >= 0 && (back > 0 || row.hour * 1000 < previous.starts[0]! + now - win.starts[0]!)),
  }
}

// Display names for row IDs: project rows already carry their folder name, machines come from the mesh.
export function useNames() {
  const machines = useMachines()
  const identity = useQuery(identityQuery).data
  const machineId = (node: string) => (node === identity?.node.id ? LOCAL : node)
  const names = {
    project: (id: string) => id || "No folder",
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
