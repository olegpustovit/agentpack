import { Link, useMatchRoute } from "@tanstack/react-router"
import { Badge } from "@/components/ui/badge"
import { SourceRow } from "@/components/kit/list"
import { StatusGlyph } from "@/components/kit/status-dot"
import { LOCAL } from "@/lib/api"
import type { Machine } from "./queries"
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
      className="shrink-0"
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
