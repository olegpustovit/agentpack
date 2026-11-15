import { useEffect, useState } from "react"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import { getRouteApi } from "@tanstack/react-router"
import { Copy, RefreshCw } from "lucide-react"
import { Meter } from "@/components/kit/data"
import { Banner, Empty, RowSkeleton } from "@/components/kit/feedback"
import { Button } from "@/components/ui/button"
import { useReplicatedQueries, useMachines, type Machine } from "@/features/machines/queries"
import { StatusSlot } from "@/features/status/status-bar"
import { ToolbarSlot, ToolbarTitle } from "@/features/shell/toolbar-slot"
import { api } from "@/lib/api"
import type { LimitWindow } from "@/lib/api-types"
import { harnessName, plural, relativeTime } from "@/lib/format"
import { hud } from "@/lib/hud"
import { cn } from "@/lib/utils"
import { limitsQuery } from "./queries"
import { loginCommands, needsLogin, observedAt, providerGroups, remainingPercent, resetText, windowsOf, type LimitReport, type ProviderGroup } from "./model"

export function LimitsView() {
  const machines = useMachines()
  const queries = useReplicatedQueries(limitsQuery)
  const now = useMinuteTick()

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
        if (machine.needsUpdate || entry?.needsUpdate) {
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
      ) : (
        <div className="min-h-0 flex-1 overflow-y-auto p-(--s4) min-[761px]:p-(--s6)">
          {/* One card per provider; usable cards first, so loaded cards keep their place when pending ones arrive. */}
          <section aria-label="Provider limits" className="grid grid-cols-[repeat(auto-fill,minmax(min(100%,320px),1fr))] items-start gap-(--s4)">
            {[...groups.filter((group) => group.usable), ...groups.filter((group) => !group.usable)].map((group) => (
              <ProviderCard key={group.provider} group={group} now={now} />
            ))}
            {pending.map((machine) => (
              <RowSkeleton key={machine.id} lines={2} className="h-auto rounded-(--r-pop) border-[0.5px] border-(--sep) p-(--s4)" />
            ))}
          </section>
        </div>
      )}
    </div>
  )
}

function ProviderCard({ group, now }: { group: ProviderGroup; now: number }) {
  const [open, setOpen] = useState(false)
  const { selected } = group
  return (
    <section
      aria-label={harnessName(group.provider)}
      data-provider={group.provider}
      className={cn("flex min-w-0 flex-col gap-(--s4) rounded-(--r-pop) border-[0.5px] border-(--sep) p-(--s4)", !group.usable && "text-(--fg3)")}
    >
      <header className="min-w-0">
        <h2 className="text-(length:--fs-lg) font-semibold">{harnessName(group.provider)}</h2>
        <p className="truncate text-(length:--fs-sm) text-(--fg2)">{[selected.name, selected.snapshot.plan].filter(Boolean).join(" · ")}</p>
      </header>
      <ReportBody report={selected} now={now} />
      {group.others.length > 0 && (
        <button type="button" aria-expanded={open} onClick={() => setOpen(!open)} className="self-start rounded-(--r-row) text-(length:--fs-sm) text-(--fg2) hover:text-(--fg)">
          {`Other machines (${group.others.length})`}
        </button>
      )}
      {open && group.others.map((report) => (
        <div key={report.id} className="flex min-w-0 flex-col gap-(--s3) border-t-[0.5px] border-(--sep-soft) pt-(--s3)">
          <p className="truncate text-(length:--fs-sm) text-(--fg2)">{[report.name, report.snapshot.plan].filter(Boolean).join(" · ")}</p>
          <ReportBody report={report} now={now} />
        </div>
      ))}
    </section>
  )
}

function ReportBody({ report, now }: { report: LimitReport; now: number }) {
  const { snapshot } = report
  if (needsLogin(snapshot)) {
    const command = loginCommands[snapshot.harness]
    return (
      <div className="flex flex-col gap-(--s3)">
        <h3 className="text-(--fg) font-medium">Signed out</h3>
        <p className="text-(length:--fs-sm) text-(--fg2)">{`Limits need a login on ${report.name}.`}</p>
        {command && <CopyCommand command={command} />}
      </div>
    )
  }
  const windows = windowsOf(snapshot)
  if (windows.length === 0) {
    return <p className="text-(length:--fs-sm) text-(--fg2)">{snapshot.error || "No usage windows reported by this machine."}</p>
  }
  return windows.map((window) => <WindowMeter key={window.id} window={window} now={now} />)
}

function WindowMeter({ window, now }: { window: LimitWindow; now: number }) {
  const left = remainingPercent(window)
  const reset = resetText(window, now)
  return (
    <div className="flex min-w-0 flex-col gap-(--s1)">
      <p className="text-(length:--fs-sm) text-(--fg2)">{window.label}</p>
      <p className="text-(length:--fs-lg) font-semibold tabular-nums">{left === undefined ? "Unknown" : `${Math.round(left)}% left`}</p>
      <Meter value={left} label={`${window.label} remaining`} />
      {reset.text && <p className={cn("text-(length:--fs-sm) text-(--fg2)", reset.refreshing && "text-(--fg3)")}>{reset.text}</p>}
    </div>
  )
}

// Shows the command the user runs on that machine; click copies it.
function CopyCommand({ command }: { command: string }) {
  return (
    <button
      type="button"
      aria-label={`Copy ${command}`}
      onClick={() => {
        // navigator.clipboard is undefined on plain-HTTP gateway origins.
        (navigator.clipboard?.writeText(command) ?? Promise.reject()).then(
          () => hud.success("Copied"),
          () => hud.error("Couldn't copy. Select the command and copy it manually."),
        )
      }}
      className="flex min-w-0 items-center justify-between gap-(--s3) rounded-(--r-row) bg-(--track) px-(--s3) py-(--s2) text-left text-(--fg)"
    >
      <code className="min-w-0 truncate font-(family-name:--mono) text-(length:--fs-sm)">{command}</code>
      <Copy aria-hidden className="size-(--fs) shrink-0 text-(--fg2)" />
    </button>
  )
}

// One timer for the whole view: reset text is minute-granular, so per-card timers would only repeat work.
function useMinuteTick(): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 60_000)
    return () => clearInterval(timer)
  }, [])
  return now
}

// Each machine's response lands in its cache as it arrives, not after the slowest one finishes.
function RefreshButton({ machines }: { machines: Machine[] }) {
  const queryClient = useQueryClient()
  async function refreshOne(machine: Machine) {
    try {
      await api("/api/limits/refresh", { machine: machine.id, method: "POST" })
      await queryClient.invalidateQueries({ queryKey: limitsQuery(machine.id, false).queryKey })
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
