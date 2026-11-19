import { useState } from "react"
import { Link, useMatchRoute, useParams } from "@tanstack/react-router"
import { ChartColumn, Folder, Gauge } from "lucide-react"
import { GroupHeader, SearchField, SourceList, SourceRow } from "@/components/kit/list"
import { StatusGlyph } from "@/components/kit/status-dot"
import { useLimitGroups } from "@/features/limits/limits-view"
import { tightestWindow } from "@/features/limits/model"
import { AgentTile } from "@/features/sessions/session-row"
import { cn } from "@/lib/utils"
import { fmt, metrics, totalsBy, viewKinds, type UsageHour } from "./model"
import { useNames, useUsage } from "./use-usage"

const groups = [
  { kind: "project", title: "Projects", key: (row: UsageHour) => row.folder_id },
  { kind: "machine", title: "Machines", key: (row: UsageHour) => row.node_id },
  { kind: "agent", title: "Agents", key: (row: UsageHour) => row.harness },
] as const satisfies { kind: (typeof viewKinds)[number]; title: string; key: (row: UsageHour) => string }[]

const trailing = "shrink-0 text-(length:--fs-sm) text-(--fg3) tabular-nums sel:text-(--accent-fg)"

// Mail-style source list: the sidebar is the filter, and each value follows the selected range and metric.
export function UsageSidebar() {
  const { metric, rows, query } = useUsage()
  const names = useNames(query.data)
  const params = useParams({ strict: false }) as { kind?: string; id?: string }
  const matchRoute = useMatchRoute()
  const tightest = tightestWindow(useLimitGroups().groups)
  const [filter, setFilter] = useState("")
  const needle = filter.toLowerCase()
  const kind = metrics[metric].kind
  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex shrink-0 p-(--s3) pb-0">
        <SearchField aria-label="Filter usage" placeholder="Filter" value={filter} onChange={(event) => setFilter(event.target.value)} />
      </div>
      <SourceList aria-label="Usage" className="min-h-0 flex-1">
        <SourceRow render={<Link to="/usage" search={true} />} selected={Boolean(matchRoute({ to: "/usage" }))} className="mt-(--s3)">
          <ChartColumn aria-hidden className="shrink-0 text-(--fg2) sel:text-(--accent-fg)" />
          <span className="min-w-0 flex-1 truncate">Overview</span>
        </SourceRow>
        <SourceRow render={<Link to="/usage/limits" search={true} />} selected={Boolean(matchRoute({ to: "/usage/limits" }))}>
          <Gauge aria-hidden className="shrink-0 text-(--fg2) sel:text-(--accent-fg)" />
          <span className="min-w-0 flex-1 truncate">Limits</span>
          {tightest && <span className={cn(trailing, tightest.left <= 20 && "text-(--warn-word)")}>{`${Math.round(100 - tightest.left)}%`}</span>}
        </SourceRow>
        {groups.map((group) => {
          const entries = [...totalsBy(rows, metric, group.key)].filter(([id]) => names.of(group.kind, id).toLowerCase().includes(needle)).sort((a, b) => b[1] - a[1])
          if (entries.length === 0) return null
          return (
            <div key={group.kind} role="group" aria-label={group.title}>
              <GroupHeader className="mt-(--s3)">{group.title}</GroupHeader>
              {entries.map(([id, value]) => (
                <SourceRow key={id} render={<Link to="/usage/$kind/$id" params={{ kind: group.kind, id }} search={true} />} selected={params.kind === group.kind && params.id === id}>
                  {group.kind === "agent" ? (
                    <AgentTile harness={id} state="idle" label={names.agent(id)} />
                  ) : group.kind === "machine" ? (
                    <StatusGlyph state={names.online(id) ? "running" : "offline"} />
                  ) : (
                    <Folder aria-hidden className="shrink-0 text-(--fg2) sel:text-(--accent-fg)" />
                  )}
                  <span className="min-w-0 flex-1 truncate">{names.of(group.kind, id)}</span>
                  <span className={trailing}>{fmt(value, kind)}</span>
                </SourceRow>
              ))}
            </div>
          )
        })}
      </SourceList>
    </div>
  )
}
