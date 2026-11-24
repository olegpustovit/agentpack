import { Link, useMatchRoute, useNavigate, useSearch } from "@tanstack/react-router"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Network } from "lucide-react"
import { Empty } from "@/components/kit/feedback"
import { GroupHeader, SearchField, SourceList, SourceRow } from "@/components/kit/list"
import { StatusGlyph } from "@/components/kit/status-dot"
import { sessionsQuery, projectsQuery } from "@/features/sessions/queries"
import { LOCAL } from "@/lib/api"
import { useAgentpackVersions, useDiscovering, useMachineQueries, useReplicatedQueries, useMachines, type Machine } from "./queries"
import { updateJobLabel, useUpdateJob } from "./update-jobs"

export function MachineRow({ machine, sessions, needsUpdate: peerNeedsUpdate }: { machine: Machine; sessions?: number; needsUpdate?: boolean }) {
  const needsUpdate = peerNeedsUpdate || machine.needsUpdate
  const matchRoute = useMatchRoute()
  const selected = matchRoute({ to: "/machines/$machine", params: { machine: machine.id } })
  const job = useUpdateJob(machine.id)
  const trailing = job
    ? updateJobLabel(job)
    : needsUpdate
      ? "Needs update"
      : !machine.online
        ? "Offline"
        : machine.id === LOCAL
          ? "gateway"
          : sessions
  return (
    <SourceRow
      render={<Link to="/machines/$machine" params={{ machine: machine.id }} />}
      selected={Boolean(selected)}
    >
      <StatusGlyph state={needsUpdate ? "needsUpdate" : !machine.online ? "offline" : "running"} />
      <span className="min-w-0 flex-1 truncate">{machine.name}</span>
      {machine.main && <Badge variant="secondary">Main</Badge>}
      <span className="shrink-0 text-(length:--fs-sm) text-(--fg3) sel:text-(--accent-fg)">
        {trailing}
      </span>
    </SourceRow>
  )
}

export function MachineList() {
  const machines = useMachines()
  const projects = useMachineQueries(projectsQuery)
  const sessions = useReplicatedQueries(sessionsQuery)
  const versionOf = useAgentpackVersions()
  const matchRoute = useMatchRoute()
  const topology = matchRoute({ to: "/machines/topology" })
  const discovering = useDiscovering()
  const q = useSearch({ strict: false, select: (search: { q?: string }) => search.q })
  const navigate = useNavigate()
  const needle = (q ?? "").trim().toLowerCase()
  const filtered = machines.filter((machine) => machine.name.toLowerCase().includes(needle))
  const setFilter = (value: string) =>
    void navigate({ to: ".", search: (prev: Record<string, unknown>) => ({ ...prev, q: value || undefined }), replace: true })
  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex shrink-0 p-(--s3) pb-0">
        <SearchField
          aria-label="Filter machines"
          placeholder="Filter machines"
          value={q ?? ""}
          onChange={(event) => setFilter(event.target.value)}
        />
      </div>
      <SourceList aria-label="Machines" aria-busy={discovering} className="min-h-0 flex-1">
        <SourceRow render={<Link to="/machines/topology" />} selected={Boolean(topology)} className="mt-(--s3)">
          <Network aria-hidden className="shrink-0 text-(--fg2) sel:text-(--accent-fg)" />
          <span className="min-w-0 flex-1 truncate">Topology</span>
        </SourceRow>
        <GroupHeader className="mt-(--s3)">Machines</GroupHeader>
        {discovering && <p className="px-(--s3) py-(--s2) text-(length:--fs-sm) text-(--fg2)">Discovering connected machines</p>}
        {!discovering && needle && filtered.length === 0 && (
          <Empty title={`No machines match "${(q ?? "").trim()}"`} className="px-(--s3) py-(--s4)">
            <Button type="button" size="sm" onClick={() => setFilter("")}>Clear filter</Button>
          </Empty>
        )}
        {filtered.map((machine) => (
          <MachineRow key={machine.id} machine={machine} sessions={sessions.byMachine[machine.id]?.data?.length} needsUpdate={projects.byMachine[machine.id]?.needsUpdate || versionOf(machine.id).outdated} />
        ))}
      </SourceList>
    </div>
  )
}
