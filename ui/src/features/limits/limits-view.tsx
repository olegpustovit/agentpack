import { useEffect, useState, type ReactNode } from "react"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import { getRouteApi } from "@tanstack/react-router"
import { RefreshCw } from "lucide-react"
import { DataGrid, DataGridCell, DataGridHeaderCell, DataGridHead, DataGridRow, Meter } from "@/components/kit/data"
import { Banner, Empty, RowSkeleton } from "@/components/kit/feedback"
import { Button } from "@/components/ui/button"
import { useMachineQueries, useMachines, type Machine } from "@/features/machines/queries"
import { useIsPhone } from "@/features/shell/use-is-phone"
import { StatusSlot } from "@/features/status/status-bar"
import { ToolbarSlot, ToolbarTitle } from "@/features/shell/toolbar-slot"
import { api } from "@/lib/api"
import type { LimitSnapshot, LimitsResponse } from "@/lib/api-types"
import { harnessName, plural, relativeTime } from "@/lib/format"
import { hud } from "@/lib/hud"
import { cn } from "@/lib/utils"
import { limitsQuery, rememberLimits } from "./queries"
import { observedAt, providerGroups, remainingPercent, resetLabel, windowsOf, type LimitReport, type ProviderGroup } from "./model"

export function LimitsView() {
  const machines = useMachines()
  const queries = useMachineQueries(limitsQuery)
  const now = useMinuteTick()
  const isPhone = useIsPhone()

  const { machine: only } = getRouteApi("/_app/limits").useSearch()
  const reports: LimitReport[] = machines.filter((machine) => !only || machine.id === only).flatMap((machine) =>
    (queries.byMachine[machine.id]?.data ?? []).map((snapshot) => ({ id: machine.id, name: machine.name, snapshot })),
  )
  const groups = providerGroups(reports)
  const pending = machines.filter((machine) => queries.byMachine[machine.id]?.pending)
  const offline = machines.filter((machine) => !machine.online && queries.byMachine[machine.id]?.data)
  // Newest measurement across machines; 0 when none has data yet. A gateway can answer from its saved copy, so
  // the fetch time would overstate freshness. The minute tick can lag a fresh fetch, so `now` is clamped below.
  const latest = Math.max(0, ...machines.map((machine) => {
    const entry = queries.byMachine[machine.id]
    return entry?.data ? observedAt(entry.data, entry.updatedAt) : 0
  }))

  return (
    <div className="flex h-full min-h-0 flex-col">
      <ToolbarTitle title="Provider limits" subtitle={`Best report per provider, across ${plural(machines.length, "machine")}`} />
      <RefreshButton machines={machines} />
      {latest > 0 && (
        <StatusSlot>{`Updated ${relativeTime(new Date(latest), Math.max(now, latest))}`}</StatusSlot>
      )}
      {machines.map((machine) => {
        const entry = queries.byMachine[machine.id]
        if (entry?.needsUpdate) {
          return <Banner key={machine.id} tone="warn">{`${machine.name} needs an update to show its limits.`}</Banner>
        }
        const error = queries.errors[machine.id]
        if (error) return <Banner key={machine.id} tone="danger">{`Couldn't load limits from ${machine.name}: ${error.message}`}</Banner>
        return null
      })}
      {offline.length > 0 && (
        <Banner>
          {`${offline.map((machine) => machine.name).join(", ")} ${offline.length === 1 ? "is" : "are"} offline. Showing last known limits.`}
        </Banner>
      )}
      {groups.length === 0 && pending.length === 0 ? (
        <Empty title="No provider limits yet">
          Limits appear when a connected machine has a signed-in provider CLI. Refresh to check again.
        </Empty>
      ) : isPhone ? (
        <div className="flex min-h-0 flex-1 flex-col gap-(--s4) overflow-y-auto p-(--s4)">
          {[...groups.filter((group) => group.usable), ...groups.filter((group) => !group.usable)].map((group) => (
            <ProviderCard key={group.provider} group={group} now={now} />
          ))}
          {pending.map((machine) => (
            <RowSkeleton key={machine.id} lines={2} />
          ))}
        </div>
      ) : (
        <div className="flex min-h-0 flex-1 flex-col px-(--s6)">
        <DataGrid aria-label="Provider limits" className="min-w-260">
          <DataGridHead>
            <tr>
              <DataGridHeaderCell className="w-32">Provider</DataGridHeaderCell>
              <DataGridHeaderCell className="w-72">Window</DataGridHeaderCell>
              <DataGridHeaderCell className="w-40">Remaining</DataGridHeaderCell>
              <DataGridHeaderCell className="w-24 text-right">% left</DataGridHeaderCell>
              <DataGridHeaderCell>Resets</DataGridHeaderCell>
              <DataGridHeaderCell className="w-52">Machine</DataGridHeaderCell>
              <DataGridHeaderCell className="w-24">Plan</DataGridHeaderCell>
            </tr>
          </DataGridHead>
          {groups.filter((group) => group.usable).map((group) => (
            <ProviderRows key={group.provider} group={group} now={now} />
          ))}
          {groups.filter((group) => !group.usable).map((group) => (
            <ProviderRows key={group.provider} group={group} now={now} dim />
          ))}
          {/* Machines still answering go last, so loaded rows do not jump when they do. */}
          {pending.map((machine) => (
            <tbody key={machine.id}>
              <DataGridRow>
                <DataGridCell colSpan={7}>
                  <RowSkeleton />
                </DataGridCell>
              </DataGridRow>
            </tbody>
          ))}
        </DataGrid>
        </div>
      )}
    </div>
  )
}

// The grid needs 1040 px. A phone gets one card per provider, with the same facts.
function ProviderCard({ group, now }: { group: ProviderGroup; now: number }) {
  const [open, setOpen] = useState(false)
  return (
    <section aria-label={harnessName(group.provider)} data-provider={group.provider} className={cn("flex flex-col gap-(--s3) rounded-(--r-pop) border-[0.5px] border-(--sep) p-(--s4)", !group.usable && "text-(--fg3)")}>
      <h2 className="text-(length:--fs) font-medium">{harnessName(group.provider)}</h2>
      <ReportBlock report={group.selected} now={now} />
      {group.others.length > 0 && (
        <button type="button" aria-expanded={open} onClick={() => setOpen(!open)} className="self-start text-(length:--fs-sm) text-(--fg2)">
          {`Other machines (${group.others.length})`}
        </button>
      )}
      {open && group.others.map((report) => <ReportBlock key={report.id} report={report} now={now} />)}
    </section>
  )
}

function ReportBlock({ report, now }: { report: LimitReport; now: number }) {
  const windows = windowsOf(report.snapshot)
  return (
    <div className="flex flex-col gap-(--s3)">
      <p className="text-(length:--fs-sm) text-(--fg2)">{[report.name, report.snapshot.plan].filter(Boolean).join(" · ")}</p>
      {windows.length === 0 && <p className="text-(length:--fs-sm) text-(--fg2)">{report.snapshot.error || "No usage windows reported by this machine."}</p>}
      {windows.map((window) => {
        const left = remainingPercent(window)
        const reset = resetLabel(window, now)
        return (
          <div key={window.id} className="flex flex-col gap-(--s1)">
            <div className="flex min-w-0 items-baseline justify-between gap-(--s3)">
              <span className="min-w-0 truncate">{window.label}</span>
              <span className="shrink-0 text-(--fg2) tabular-nums">{left === undefined ? "Unknown" : `${Math.round(left)}% left`}</span>
            </div>
            <Meter value={left} label={`${window.label} remaining`} />
            <p className={cn("text-(length:--fs-sm) text-(--fg2)", reset.due && "text-(--fg3)")}>{reset.due ? reset.text : reset.text.split(" · ")[0]}</p>
          </div>
        )
      })}
    </div>
  )
}

// One timer for the whole grid: reset text is minute-granular, so per-row timers would only repeat work.
function useMinuteTick(): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 60_000)
    return () => clearInterval(timer)
  }, [])
  return now
}

// The provider cell spans the selected account's rows, the disclosure and any expanded rows.
function ProviderRows({ group, now, dim = false }: { group: ProviderGroup; now: number; dim?: boolean }) {
  const [open, setOpen] = useState(false)
  const selected = reportLines(group.selected, now)
  const others = open ? group.others.flatMap((report) => reportLines(report, now)) : []
  const span = selected.length + (group.others.length > 0 ? 1 + others.length : 0)
  return (
    <tbody data-provider={group.provider} className={cn("border-b-[0.5px] border-(--sep)", dim && "text-(--fg3)")}>
      {selected.map((line, index) => (
        <DataGridRow key={line.key}>
          {index === 0 && (
            <DataGridCell rowSpan={span} className="align-top leading-(--row-h) font-medium">
              {harnessName(group.provider)}
            </DataGridCell>
          )}
          {line.cells}
        </DataGridRow>
      ))}
      {group.others.length > 0 && (
        <DataGridRow>
          <DataGridCell colSpan={6}>
            <button
              type="button"
              aria-expanded={open}
              onClick={() => setOpen(!open)}
              className="rounded-(--r-row) text-(--fg2) hover:text-(--fg)"
            >
              {`Other machines (${group.others.length})`}
            </button>
          </DataGridCell>
        </DataGridRow>
      )}
      {others.map((line) => (
        <DataGridRow key={line.key}>{line.cells}</DataGridRow>
      ))}
    </tbody>
  )
}

type Line = { key: string; cells: ReactNode }

// One line per window. A report without windows gets one line that says why, in the flexible column.
function reportLines(report: LimitReport, now: number): Line[] {
  const { snapshot } = report
  const windows = windowsOf(snapshot)
  if (windows.length === 0) {
    return [
      {
        key: `${report.id}:none`,
        cells: (
          <>
            <DataGridCell colSpan={4} className="text-(--fg2)" title={snapshot.error}>
              {snapshot.error || "No usage windows reported by this machine."}
            </DataGridCell>
            <DataGridCell>{report.name}</DataGridCell>
            <DataGridCell>{snapshot.plan}</DataGridCell>
          </>
        ),
      },
    ]
  }
  return windows.map((window) => {
    const left = remainingPercent(window)
    const reset = resetLabel(window, now)
    return {
      key: `${report.id}:${window.id}`,
      cells: (
        <>
          <DataGridCell title={window.label}>{window.label}</DataGridCell>
          <DataGridCell>
            <Meter value={left} label={`${window.label} remaining`} />
          </DataGridCell>
          <DataGridCell className="text-right tabular-nums">{left === undefined ? "Unknown" : `${Math.round(left)}% left`}</DataGridCell>
          <DataGridCell className={cn(reset.due && "text-(--fg3)")} title={reset.text}>
            {reset.due ? reset.text : reset.text.split(" · ")[0]}
          </DataGridCell>
          <DataGridCell title={report.name}>{report.name}</DataGridCell>
          <DataGridCell>{snapshot.plan}</DataGridCell>
        </>
      ),
    }
  })
}

// Each machine's response lands in its cache as it arrives, not after the slowest one finishes.
function RefreshButton({ machines }: { machines: Machine[] }) {
  const queryClient = useQueryClient()
  async function refreshOne(machine: Machine) {
    try {
      const { limits } = await api<LimitsResponse>("/api/limits/refresh", { machine: machine.id, method: "POST" })
      queryClient.setQueryData<LimitSnapshot[]>(["m", machine.id, "limits"], limits)
      rememberLimits(machine.id, limits)
    } catch (error) {
      hud.error(`Couldn't refresh limits on ${machine.name}: ${(error as Error).message}`, () => void refreshOne(machine))
    }
  }
  const refresh = useMutation({
    mutationFn: () => Promise.all(machines.filter((machine) => machine.online).map(refreshOne)),
  })
  return (
    <ToolbarSlot>
      <Button disabled={refresh.isPending} onClick={() => refresh.mutate()}>
        <RefreshCw aria-hidden className={cn(refresh.isPending && "animate-spin motion-reduce:animate-none")} />
        Refresh
      </Button>
    </ToolbarSlot>
  )
}
