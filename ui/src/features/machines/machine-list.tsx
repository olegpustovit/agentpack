import { Link, useMatchRoute } from "@tanstack/react-router"
import { Laptop, Network, Server } from "lucide-react"
import { SourceList, SourceRow } from "@/components/kit/list"
import { StatusDot } from "@/components/kit/status-dot"
import { LOCAL } from "@/lib/api"
import { useMachines, type Machine } from "./queries"

function MachineRow({ machine }: { machine: Machine }) {
  const matchRoute = useMatchRoute()
  const selected = matchRoute({ to: "/machines/$machine", params: { machine: machine.id } })
  const Glyph = machine.id === LOCAL ? Laptop : Server
  return (
    <SourceRow
      render={<Link to="/machines/$machine" params={{ machine: machine.id }} />}
      selected={Boolean(selected)}
      className="h-(--row-h-2) max-[760px]:h-11"
    >
      <Glyph aria-hidden className="size-(--s4) shrink-0 text-(--fg2) group-data-selected/row:text-(--accent-fg)" />
      <span className="min-w-0 flex-1 truncate">{machine.name}</span>
      <StatusDot state={machine.online ? "running" : "offline"} />
    </SourceRow>
  )
}

export function MachineList() {
  const machines = useMachines()
  const matchRoute = useMatchRoute()
  const topology = matchRoute({ to: "/machines/topology" })
  return (
    <div className="flex h-full min-h-0 flex-col">
      <SourceList aria-label="Machines" className="min-h-0 flex-1">
        <SourceRow render={<Link to="/machines/topology" />} selected={Boolean(topology)} className="h-(--row-h-2) max-[760px]:h-11">
          <Network aria-hidden className="size-(--s4) shrink-0 text-(--fg2) group-data-selected/row:text-(--accent-fg)" />
          <span className="min-w-0 flex-1 truncate">Topology</span>
        </SourceRow>
        {machines.map((machine) => (
          <MachineRow key={machine.id} machine={machine} />
        ))}
      </SourceList>
    </div>
  )
}
