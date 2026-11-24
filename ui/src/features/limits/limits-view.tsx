import { useEffect, useState } from "react"
import { useMutation, useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query"
import { getRouteApi, Link } from "@tanstack/react-router"
import { Check, Copy, RefreshCw } from "lucide-react"
import { Meter, Section } from "@/components/kit/data"
import { Banner, Empty, RowSkeleton } from "@/components/kit/feedback"
import { Button } from "@/components/ui/button"
import type { Machine } from "@/features/machines/queries"
import { AgentTile } from "@/components/kit/agent-tile"
import { ToolbarSlot, ToolbarTitle } from "@/features/shell/toolbar-slot"
import { usageQuery } from "@/features/usage/queries"
import { api, LOCAL } from "@/lib/api"
import type { LimitWindow } from "@/lib/api-types"
import { harnessName, plural, relativeTime } from "@/lib/format"
import { hud, useCopied } from "@/lib/hud"
import { cn } from "@/lib/utils"
import { limitsQuery } from "./queries"
import { absoluteResetTime, disagreementSummary, isWindowStale, loginCommands, needsLogin, observedAt, paceText, remainingPercent, resetPassed, resetText, runsOutAt, tightestWindow, useLimitGroups, windowsOf, type LimitReport, type ProviderGroup } from "./model"

const limitsRoute = getRouteApi("/_app/usage/limits")

// Per tab, when each peer was last asked: views showing limits share it, so moving between them asks nobody twice.
const nudged = new Map<string, number>()
const NUDGE_MS = 5 * 60_000

// Peers write their limits only after their own turns, or when asked. While limits are on screen, ask every
// online peer to read its CLIs again; a failed nudge leaves the replicated reading, which is dated.
export function useLimitsNudge(machines: Machine[]) {
  const queryClient = useQueryClient()
  const peers = machines.filter((machine) => machine.online && machine.id !== LOCAL).map((machine) => machine.id).join("\n")
  useEffect(() => {
    const now = Date.now()
    for (const id of peers.split("\n").filter(Boolean)) {
      if (now - (nudged.get(id) ?? -Infinity) < NUDGE_MS) continue
      nudged.set(id, now)
      api("/api/limits/refresh", { machine: id, method: "POST" }).then(
        () => queryClient.invalidateQueries({ queryKey: limitsQuery(false).queryKey }),
        () => undefined,
      )
    }
  }, [peers, queryClient])
}

export function LimitsView() {
  const { machine: only } = limitsRoute.useSearch()
  const { machines, queries, groups } = useLimitGroups(only)
  useLimitsNudge(machines)
  const now = useMinuteTick()

  const pending = machines.filter((machine) => queries.byMachine[machine.id]?.pending)
  const outdated = machines.filter((machine) => machine.needsUpdate || queries.byMachine[machine.id]?.needsUpdate)
  const offline = machines.filter((machine) => !machine.online && !outdated.includes(machine) && queries.byMachine[machine.id]?.data)
  // Newest measurement among chosen reports (falling back to all machines); 0 when none has data yet.
  const selectedObserved = groups.length > 0
    ? Math.max(0, ...groups.map((group) => observedAt([group.selected.snapshot], queries.byMachine[group.selected.id]?.updatedAt ?? 0)))
    : 0
  const latest = selectedObserved || Math.max(0, ...machines.map((machine) => {
    const entry = queries.byMachine[machine.id]
    return entry?.data ? observedAt(entry.data, entry.updatedAt) : 0
  }))
  const tightest = tightestWindow(groups)

  return (
    <div className="flex h-full min-h-0 flex-col">
      <ToolbarTitle title="Limits" subtitle={[`Best report per provider · ${plural(machines.length, "machine")}`, latest > 0 && `updated ${relativeTime(new Date(latest), Math.max(now, latest))}`].filter(Boolean).join(" · ")} />
      <RefreshButton machines={machines} />
      {outdated.length > 0 && <Banner>{`${names(outdated)} ${outdated.length === 1 ? "needs" : "need"} an update to show limits.`}</Banner>}
      {machines.map((machine) => {
        const error = !outdated.includes(machine) && queries.errors[machine.id]
        return error ? <Banner key={machine.id} tone="danger">{`Couldn't load limits from ${machine.name}: ${error.message}`}</Banner> : null
      })}
      {offline.length > 0 && <Banner>{`${names(offline)} ${offline.length === 1 ? "is" : "are"} offline. Showing last known limits.`}</Banner>}
      {groups.length === 0 && pending.length === 0 ? (
        <Empty title="No provider limits yet">
          Limits appear when a connected machine has a signed-in provider CLI. Refresh to check again.
        </Empty>
      ) : (
        <div className="min-h-0 flex-1 overflow-y-auto">
          <div className="mx-auto w-full max-w-295 px-(--s7) py-(--s6) max-[760px]:p-(--s4)">
            {tightest && <BurnHero group={tightest.group} window={tightest.window} now={now} />}
            <Section title="Limits" className={tightest ? "mt-(--s6)" : "border-t-0 pt-0"}>
              {/* Usable providers first, so loaded ones keep their place when pending ones arrive. */}
              <div role="region" aria-label="Provider limits" className="flex flex-col divide-y-[0.5px] divide-(--sep-soft)">
                {[...groups.filter((group) => group.usable), ...groups.filter((group) => !group.usable)].map((group) => (
                  <ProviderLimits key={group.provider} group={group} now={now} />
                ))}
                {pending.map((machine) => <RowSkeleton key={machine.id} lines={2} />)}
              </div>
            </Section>
            <p className="mt-(--s3) text-(length:--fs-sm) text-(--fg2)">The best report per provider wins. One login can be shared by several machines.</p>
          </div>
        </div>
      )}
    </div>
  )
}

const names = (machines: Machine[]) => machines.map((machine) => machine.name).join(", ")

const DAY = 24 * 3_600_000
const AHEAD = 6 * 3_600_000

// The tightest window's last day from limit_samples, with its projection to running out at this pace.
function BurnHero({ group, window, now }: { group: ProviderGroup; window: LimitWindow; now: number }) {
  const { selected } = group
  const from = Math.floor(now / 3_600_000) * 3_600_000 - DAY
  const samples = useQuery(usageQuery(from, from + DAY + 2 * 3_600_000)).data?.samples ?? []
  const left = remainingPercent(window) ?? 0
  const used = 100 - left
  const points = samples
    .filter((sample) => sample.node_id === selected.snapshot.node_id && sample.harness === selected.snapshot.harness && sample.window_id === window.id && sample.used_percent != null)
    // A sample is the hour's last reading; mid-hour, never later than now, is where it most likely fell.
    .map((sample) => [Math.min(now, sample.hour * 1000 + 1_800_000), sample.used_percent!] as const)
  points.push([now, used])
  const W = 560
  const H = 90
  const x = (time: number) => ((time - (now - DAY)) / (DAY + AHEAD)) * W
  const y = (percent: number) => H - (percent / 100) * H
  const observed = observedAt([selected.snapshot], 0)
  const stale = isWindowStale(window, observed, now)
  const end = stale ? undefined : runsOutAt(window, now)
  const projected = end === undefined ? undefined : Math.min(end, now + AHEAD)
  const reset = Date.parse(window.reset_at ?? "")
  const rawPace = stale
    ? { text: resetPassed(window, now) ? "Stale · reset passed" : observed > 0 ? `As of ${relativeTime(new Date(observed), Math.max(now, observed))}` : "Stale", warn: false }
    : paceText(window, now)
  const pace = !stale && rawPace.warn ? { ...rawPace, text: `Estimate · ${rawPace.text}` } : rawPace
  const resetInfo = resetText(window, now)
  const absoluteReset = absoluteResetTime(window, now)
  const resetLine = resetInfo.text ? (absoluteReset ? `${resetInfo.text} (${absoluteReset})` : resetInfo.text) : ""
  return (
    <section aria-label="Tightest limit">
      <div className="flex min-w-0 items-center gap-(--s3) text-(length:--fs-sm)">
        <h2 className="min-w-0 flex-1 truncate font-semibold text-(--fg2)">{`${harnessName(group.provider)} · ${window.label}`}</h2>
        {observed > 0 && <span className="shrink-0 text-(--fg3)">{`Reported by ${selected.name} ${relativeTime(new Date(observed), Math.max(now, observed))}`}</span>}
      </div>
      <p className="mt-(--s3) text-(length:--fs-sm) text-(--fg2)">Remaining</p>
      <p className="text-[28px] leading-tight font-semibold tabular-nums">{`${Math.round(left)}% left`}</p>
      <p className={cn("text-(length:--fs-sm) text-(--fg2)", pace.warn && "text-(--warn-word)")}>{[pace.text, resetLine].filter(Boolean).join(" · ")}</p>
      <svg viewBox={`0 0 ${W} ${H + 16}`} role="img" aria-label={`${window.label} used over the last day`} className="mt-(--s4) block h-auto w-[min(100%,720px)] overflow-visible">
        {[0, 50, 100].map((percent) => <line key={percent} x1={0} x2={W} y1={y(percent)} y2={y(percent)} stroke="var(--sep-soft)" />)}
        <polyline fill="none" stroke="var(--accent)" strokeWidth={2} strokeLinejoin="round" points={points.map(([time, percent]) => `${x(time)},${y(percent)}`).join(" ")} />
        <circle cx={x(now)} cy={y(used)} r={3} fill="var(--accent)" />
        {projected !== undefined && end !== undefined && (
          <polyline data-slot="limit-projection" fill="none" stroke="var(--warn)" strokeWidth={2} strokeDasharray="4 3" points={`${x(now)},${y(used)} ${x(projected)},${y(used + ((100 - used) * (projected - now)) / (end - now))}`} />
        )}
        {reset > now && reset < now + AHEAD && (
          <g>
            <line x1={x(reset)} x2={x(reset)} y1={0} y2={H} stroke="var(--fg3)" strokeDasharray="2 3" />
            <text x={x(reset) + 4} y={10} fontSize={10} fill="var(--fg3)">reset</text>
          </g>
        )}
        <text x={0} y={H + 13} fontSize={10} fill="var(--fg3)">24 h ago</text>
        <text x={x(now)} y={H + 13} fontSize={10} fill="var(--fg3)" textAnchor="middle">now</text>
      </svg>
    </section>
  )
}

function ProviderLimits({ group, now }: { group: ProviderGroup; now: number }) {
  const [open, setOpen] = useState(false)
  const { selected } = group
  const observed = observedAt([selected.snapshot], 0)
  const disagreement = disagreementSummary(group, now)
  return (
    <section
      aria-label={harnessName(group.provider)}
      data-provider={group.provider}
      className={cn("flex min-w-0 flex-col gap-(--s2) py-(--s3)", !group.usable && "text-(--fg3)")}
    >
      <header className="flex min-w-0 items-center gap-(--s3)">
        <AgentTile harness={group.provider} state="idle" label={harnessName(group.provider)} />
        <h3 className="font-semibold">{harnessName(group.provider)}</h3>
        <p className="min-w-0 truncate text-(length:--fs-sm) text-(--fg2)">
          {[selected.name, selected.snapshot.plan, observed > 0 ? relativeTime(new Date(observed), Math.max(now, observed)) : undefined].filter(Boolean).join(" · ")}
        </p>
      </header>
      {disagreement && <Banner tone="warn">{disagreement}</Banner>}
      <ReportBody report={selected} now={now} />
      {group.others.length > 0 && (
        <button
          type="button"
          aria-expanded={open}
          onClick={() => setOpen(!open)}
          className="inline-flex min-h-11 items-center self-start rounded-(--r-row) py-(--s2) text-(length:--fs-sm) text-(--fg2) hover:text-(--fg)"
        >
          {`Other machines (${group.others.length})`}
        </button>
      )}
      {open && group.others.map((report) => {
        const reportObserved = observedAt([report.snapshot], 0)
        return (
          <div key={report.id} className="flex min-w-0 flex-col gap-(--s2) ps-(--s7)">
            <p className="truncate text-(length:--fs-sm) text-(--fg2)">
              {[report.name, report.snapshot.plan, reportObserved > 0 ? relativeTime(new Date(reportObserved), Math.max(now, reportObserved)) : undefined].filter(Boolean).join(" · ")}
            </p>
            <ReportBody report={report} now={now} />
          </div>
        )
      })}
    </section>
  )
}

function ReportBody({ report, now }: { report: LimitReport; now: number }) {
  const { snapshot } = report
  if (needsLogin(snapshot)) {
    const command = loginCommands[snapshot.harness]
    return (
      <div className="flex flex-wrap items-center gap-(--s3)">
        <span className="text-(--fg) font-medium">Signed out</span>
        <span className="text-(length:--fs-sm) text-(--fg2)">{`Limits need a login on ${report.name}.`}</span>
        <Link to="/machines/$machine" params={{ machine: report.id }} className="text-(length:--fs-sm) text-(--accent) underline-offset-2 hover:underline">
          {`Sign in on ${report.name}`}
        </Link>
        {command && <CopyCommand command={command} />}
      </div>
    )
  }
  const windows = windowsOf(snapshot)
  if (windows.length === 0) {
    return <p className="text-(length:--fs-sm) text-(--fg2)">{snapshot.error || "No usage windows reported by this machine."}</p>
  }
  const observed = observedAt([snapshot], now)
  return windows.map((window) => <WindowMeter key={window.id} window={window} now={now} observed={observed} source={report.name} />)
}

// One window: label and what is left, the meter, then the pace and the reset.
function WindowMeter({ window, now, provider, observed = now, source }: { window: LimitWindow; now: number; provider?: string; observed?: number; source?: string }) {
  const left = remainingPercent(window)
  const reset = resetText(window, now)
  const absoluteReset = absoluteResetTime(window, now)
  const resetLine = reset.text ? (absoluteReset ? `${reset.text} (${absoluteReset})` : reset.text) : ""
  // A passed reset or hour-old reading gets its age and no pace projection: extrapolating stale input misleads.
  const expired = resetPassed(window, now)
  const stale = isWindowStale(window, observed, now)
  const rawPace = expired
    ? { text: "Stale · reset passed", warn: false }
    : stale
      ? { text: `As of ${relativeTime(new Date(observed), Math.max(now, observed))}`, warn: false }
      : paceText(window, now)
  const pace = !stale && rawPace.warn ? { ...rawPace, text: `Estimate · ${rawPace.text}` } : rawPace
  const provenance = source && observed > 0 && !stale ? `${source} · ${relativeTime(new Date(observed), Math.max(now, observed))}` : ""
  return (
    <div className="flex min-w-0 flex-col gap-(--s1)">
      <div className="flex min-w-0 items-baseline gap-(--s3)">
        {provider && <AgentTile harness={provider} state="idle" label={harnessName(provider)} className="self-center" />}
        {provider && <span className="font-semibold">{harnessName(provider)}</span>}
        <span className="min-w-0 truncate text-(--fg2)">{window.label}</span>
        <span className="ms-auto shrink-0 text-(length:--fs-sm) text-(--fg2) tabular-nums">{left === undefined ? "Unknown" : `${Math.round(left)}% left`}</span>
      </div>
      <Meter value={left} label={`${window.label} remaining`} />
      <div className="flex min-w-0 flex-wrap gap-x-(--s3) gap-y-(--s1) text-(length:--fs-xs) text-(--fg2)">
        <span className={cn("min-w-0 truncate", pace.warn && "text-(--warn-word)")}>{[pace.text, provenance].filter(Boolean).join(" · ")}</span>
        {resetLine && <span className={cn("ms-auto shrink-0", reset.refreshing && "text-(--fg3)")}>{resetLine}</span>}
      </div>
    </div>
  )
}

// The overview's Limits section: each usable provider's windows, with Show All opening the full view.
export function LimitsSummary({ agent }: { agent?: string }) {
  const { machines, groups } = useLimitGroups()
  useLimitsNudge(machines)
  const now = useMinuteTick()
  const shown = groups.filter((group) => group.usable && (!agent || group.provider === agent))
  if (shown.length === 0) return null
  return (
    <Section title="Limits" action={<Link to="/usage/limits" search={true} className="text-(length:--fs-sm) text-(--accent) hover:underline">Show All</Link>}>
      <div className="flex flex-col divide-y-[0.5px] divide-(--sep-soft)">
        {shown.flatMap((group) =>
          windowsOf(group.selected.snapshot).map((window) => (
            <div key={`${group.provider}:${window.id}`} className="py-(--s3) first:pt-0">
              <WindowMeter window={window} now={now} provider={group.provider} observed={observedAt([group.selected.snapshot], now)} />
            </div>
          )),
        )}
      </div>
    </Section>
  )
}

// Shows the command the user runs on that machine; click copies it.
function CopyCommand({ command }: { command: string }) {
  const [copied, copy] = useCopied()
  const Icon = copied ? Check : Copy
  return (
    <button
      type="button"
      aria-label={copied ? "Copied" : `Copy ${command}`}
      onClick={() => copy(command, "Couldn't copy. Select the command and copy it manually.")}
      className="flex min-w-0 items-center justify-between gap-(--s3) rounded-(--r-row) bg-(--track) px-(--s3) py-(--s1) text-left text-(--fg)"
    >
      <code className="min-w-0 truncate font-(family-name:--mono) text-(length:--fs-sm)">{command}</code>
      <Icon aria-hidden className="size-(--fs) shrink-0 text-(--fg2)" />
    </button>
  )
}

// One timer for the whole view: reset text is minute-granular, so per-row timers would only repeat work.
function useMinuteTick(): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 60_000)
    return () => clearInterval(timer)
  }, [])
  return now
}

// Each machine's response lands in its cache as it arrives, not after the slowest one finishes.
async function refreshOne(queryClient: QueryClient, machine: Machine): Promise<void> {
  try {
    await api("/api/limits/refresh", { machine: machine.id, method: "POST", query: { explicit: "1" } })
    await queryClient.invalidateQueries({ queryKey: limitsQuery(false).queryKey })
  } catch (error) {
    hud.error(`Couldn't refresh limits on ${machine.name}: ${(error as Error).message}`, () => void refreshOne(queryClient, machine))
  }
}

function RefreshButton({ machines }: { machines: Machine[] }) {
  const queryClient = useQueryClient()
  const refresh = useMutation({
    mutationFn: () => Promise.all(machines.filter((machine) => machine.online).map((machine) => refreshOne(queryClient, machine))),
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
