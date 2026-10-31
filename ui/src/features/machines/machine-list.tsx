import { Link, useMatchRoute, useNavigate, useSearch } from "@tanstack/react-router"
import { Network } from "lucide-react"
import { GroupHeader, SearchField, SourceList, SourceRow } from "@/components/kit/list"
import { StatusGlyph } from "@/components/kit/status-dot"
import { sessionsQuery, projectsQuery } from "@/features/sessions/queries"
import { StatusSlot } from "@/features/status/status-bar"
import { LOCAL } from "@/lib/api"
import { plural } from "@/lib/format"
import { useDiscovering, useMachineQueries, useMachines, type Machine, type MachineQuery } from "./queries"

function MachineRow({ machine, sessions, needsUpdate }: { machine: Machine; sessions?: number; needsUpdate?: boolean }) {
  const matchRoute = useMatchRoute()
  const selected = matchRoute({ to: "/machines/$machine", params: { machine: machine.id } })
  return (
    <SourceRow
      render={<Link to="/machines/$machine" params={{ machine: machine.id }} />}
      selected={Boolean(selected)}
    >
      <StatusGlyph state={!machine.online ? "offline" : needsUpdate ? "needsUpdate" : "running"} />
      <span className="min-w-0 flex-1 truncate">{machine.name}</span>
      <span className="shrink-0 text-(length:--fs-sm) text-(--fg3) sel:text-(--accent-fg)">
        {!machine.online ? "Offline" : needsUpdate ? "Needs update" : machine.id === LOCAL ? "gateway" : sessions}
      </span>
    </SourceRow>
  )
}

function countRows(byMachine: Record<string, MachineQuery<unknown[]>>): number {
  return Object.values(byMachine).reduce((sum, entry) => sum + (entry.data?.length ?? 0), 0)
}

export function MachineList() {
  const machines = useMachines()
  const projects = useMachineQueries(projectsQuery)
  const sessions = useMachineQueries(sessionsQuery)
  const matchRoute = useMatchRoute()
  const topology = matchRoute({ to: "/machines/topology" })
  const discovering = useDiscovering()
  const q = useSearch({ strict: false, select: (search: { q?: string }) => search.q })
  const navigate = useNavigate()
  const needle = (q ?? "").toLowerCase()
  return (
    <div className="flex h-full min-h-0 flex-col">
      <StatusSlot>{`${plural(countRows(projects.byMachine), "project")} · ${plural(countRows(sessions.byMachine), "session")}`}</StatusSlot>
      <div className="flex shrink-0 p-(--s3) pb-0">
        <SearchField
          aria-label="Filter machines"
          placeholder="Filter machines"
          defaultValue={q ?? ""}
          onChange={(event) => void navigate({ to: ".", search: (prev: Record<string, unknown>) => ({ ...prev, q: event.target.value || undefined }), replace: true })}
        />
      </div>
      <SourceList aria-label="Machines" aria-busy={discovering} className="min-h-0 flex-1">
        <SourceRow render={<Link to="/machines/topology" />} selected={Boolean(topology)} className="mt-(--s3)">
          <Network aria-hidden className="shrink-0 text-(--fg2) sel:text-(--accent-fg)" />
          <span className="min-w-0 flex-1 truncate">Topology</span>
        </SourceRow>
        <GroupHeader className="mt-(--s3)">Machines</GroupHeader>
        {discovering && <p className="px-(--s3) py-(--s2) text-(length:--fs-sm) text-(--fg2)">Discovering connected machines</p>}
        {machines.filter((machine) => machine.name.toLowerCase().includes(needle)).map((machine) => (
          <MachineRow key={machine.id} machine={machine} sessions={sessions.byMachine[machine.id]?.data?.length} needsUpdate={projects.byMachine[machine.id]?.needsUpdate} />
        ))}
      </SourceList>
    </div>
  )
}
