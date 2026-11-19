import { useState } from "react"
import { Link, useNavigate } from "@tanstack/react-router"
import { ChevronLeft, ChevronRight, Folder, Server, Cpu, X } from "lucide-react"
import { DataGrid, DataGridCell, DataGridHead, DataGridHeaderCell, DataGridRow, KeyValue, Meter, Section } from "@/components/kit/data"
import { Banner, RowSkeleton } from "@/components/kit/feedback"
import { PopupSelect } from "@/components/kit/forms"
import { SegmentedControl, SegmentedControlItem } from "@/components/kit/list"
import { Button } from "@/components/ui/button"
import { LimitsSummary } from "@/features/limits/limits-view"
import { AgentTile } from "@/features/sessions/session-row"
import { StatusSlot } from "@/features/status/status-bar"
import { ToolbarSlot, ToolbarTitle } from "@/features/shell/toolbar-slot"
import { ApiError } from "@/lib/api"
import { basename, plural } from "@/lib/format"
import { cn } from "@/lib/utils"
import {
  activeIn,
  allTokens,
  averageOf,
  bucketEnd,
  bucketLabel,
  bucketize,
  changeOf,
  fmt,
  inView,
  metrics,
  rangeName,
  totalsBy,
  type Kind,
  type Metric,
  type Range,
  type UsageHour,
  type UsageSession,
  type View,
} from "./model"
import { UsageChart } from "./usage-chart"
import { seriesColor, useNames, useUsage, type Names } from "./use-usage"

type Dimension = "project" | "agent" | "machine" | "model"

const dimensionKey: Record<Dimension, (row: UsageHour) => string> = {
  project: (row) => row.folder_id,
  agent: (row) => row.harness,
  machine: (row) => row.node_id,
  model: (row) => row.model,
}
const dimensionTitle: Record<Dimension, string> = { project: "Projects", agent: "Agents", machine: "Machines", model: "Models" }
const subtitle: Record<View["kind"], string> = { overview: "All machines", limits: "Limits", project: "Project", machine: "Machine", agent: "Agent" }

export function UsageView({ view }: { view: View }) {
  const usage = useUsage()
  const { metric, win, query, now } = usage
  const names = useNames(query.data)
  const [hover, setHover] = useState<number | null>(null)
  // A pinned bar belongs to one window; another range or period starts unpinned.
  const windowKey = `${win.range}:${win.starts[0]}`
  const [pin, setPin] = useState<{ key: string; index: number } | null>(null)
  const pinned = pin?.key === windowKey ? pin.index : null
  const setPinned = (index: number | null) => setPin(index === null ? null : { key: windowKey, index })
  const { kind: unit, label } = metrics[metric]
  const navigate = useNavigate()

  const rows = usage.rows.filter((row) => inView(row, view))
  const previousTotal = usage.previousRows.filter((row) => inView(row, view)).reduce((sum, row) => sum + metrics[metric].value(row), 0)
  const byAgent = view.kind !== "agent"
  const stack = byAgent ? dimensionKey.agent : dimensionKey.model
  const { totals, series } = bucketize(rows, win, metric, stack)
  const average = averageOf(totals, win, now)
  const total = totals.reduce((sum, value) => sum + value, 0)
  const color = (key: string, index: number) => seriesColor(key, index, byAgent)
  const sessions = (query.data?.sessions ?? []).filter((session) => inView(session, view) && activeIn(session, win.starts[0]!, win.end))
  const title = view.kind === "overview" || !view.id ? "Usage" : names.of(view.kind as Dimension, view.id)
  const period = rangeName(win)
  const shown = hover ?? pinned
  const pinnedSessions = pinned === null ? sessions : sessions.filter((session) => activeIn(session, win.starts[pinned]!, bucketEnd(win, pinned)))

  return (
    <div className="flex h-full min-h-0 flex-col">
      <ToolbarTitle title={title} subtitle={`${subtitle[view.kind]} · ${period}`} />
      <RangeControls range={usage.range} back={usage.back} period={period} />
      <StatusSlot>{`${plural(sessions.length, "session")} · ${fmt(rows.reduce((sum, row) => sum + row.output_tokens, 0))} output tokens · ${period.toLowerCase()}`}</StatusSlot>
      {query.error instanceof ApiError && query.error.status === 404 ? (
        <Banner tone="warn">This gateway needs an update to show usage.</Banner>
      ) : (
        query.error && <Banner tone="danger">{`Couldn't load usage: ${query.error.message}`}</Banner>
      )}
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto w-full max-w-295 px-(--s7) py-(--s6) max-[760px]:p-(--s4)">
          <section aria-label="Usage chart">
            <div className="flex min-w-0 items-end gap-(--s5)">
              <div data-slot="usage-hero" className="min-w-0 flex-1" aria-live="polite">
                <p className="text-(length:--fs-sm) text-(--fg2)">{shown === null ? period : bucketLabel(win, shown, true)}</p>
                <p className="my-0.5 text-[28px] leading-tight font-semibold tracking-tight tabular-nums">{fmt(shown === null ? total : totals[shown]!, unit)}</p>
                <p className="text-(length:--fs-sm) text-(--fg2) tabular-nums">
                  {shown === null ? heroDetail(average, total, previousTotal, unit, win.range, metric) : bucketDetail(totals[shown]!, total, period, sessions.filter((session) => activeIn(session, win.starts[shown]!, bucketEnd(win, shown))).length)}
                </p>
              </div>
              <PopupSelect label="Metric" value={metric} onValueChange={(value) => void navigate({ to: ".", search: (prev: Record<string, unknown>) => ({ ...prev, metric: value === "out" ? undefined : value }), replace: true })} items={Object.entries(metrics).map(([value, entry]) => ({ value, label: entry.label }))} />
            </div>
            {query.isPending ? (
              <RowSkeleton lines={2} className="mt-(--s5) h-[190px]" />
            ) : (
              <UsageChart
                win={win}
                series={series}
                totals={totals}
                average={average}
                kind={unit}
                label={`${label} by ${byAgent ? "agent" : "model"}, ${period}`}
                color={color}
                hover={hover}
                pinned={pinned}
                onHover={setHover}
                onPin={setPinned}
              />
            )}
            <ul aria-label="Legend" className="mt-(--s4) flex flex-wrap gap-x-(--s6) gap-y-(--s2)">
              {series.map((entry, index) => (
                <li key={entry.key}>
                  <Legend entry={entry} color={color(entry.key, index)} unit={unit} name={byAgent ? names.agent(entry.key) : names.model(entry.key)} link={byAgent ? entry.key : undefined} />
                </li>
              ))}
            </ul>
          </section>

          <div className="mt-(--s6) grid gap-x-(--s7) gap-y-(--s6) min-[1100px]:grid-cols-2">
            {view.kind === "overview" ? <LimitsSummary /> : <Summary rows={rows} sessions={sessions} view={view} names={names} />}
            <MostUsed rows={rows} view={view} metric={metric} names={names} />
            {view.kind === "agent" && <LimitsSummary agent={view.id} />}
            <Efficiency rows={rows} sessions={sessions} />
          </div>

          <SessionsTable
            className="mt-(--s6)"
            sessions={pinnedSessions}
            view={view}
            names={names}
            pinned={pinned === null ? undefined : bucketLabel(win, pinned, true)}
            onClear={() => setPinned(null)}
          />
        </div>
      </div>
    </div>
  )
}

function heroDetail(average: number, total: number, previous: number, unit: Kind, range: Range, metric: Metric) {
  const change = changeOf(total, previous)
  const since = range === "day" ? "the day before" : `last ${range}`
  return [
    `${range === "day" ? "Hourly" : "Daily"} average ${fmt(average, unit)}`,
    change !== undefined && `${change >= 0 ? "↑" : "↓"} ${Math.abs(change)}% from ${since}`,
    metric === "cost" && "Codex and Cursor don't report cost",
  ].filter(Boolean).join(" · ")
}

function bucketDetail(value: number, total: number, period: string, count: number) {
  return `${total ? Math.round((value / total) * 100) : 0}% of ${period.toLowerCase()} · ${plural(count, "session")}`
}

// The toolbar's trailing tools: ‹ This Week › and Day · Week · Month.
function RangeControls({ range, back, period }: { range: Range; back: number; period: string }) {
  const navigate = useNavigate()
  const go = (next: { range?: Range; back?: number }) =>
    void navigate({ to: ".", search: (prev: Record<string, unknown>) => ({ ...prev, range: next.range === "week" ? undefined : (next.range ?? prev.range), back: next.back || undefined }), replace: true })
  return (
    <ToolbarSlot>
      <Button variant="ghost" size="icon" aria-label="Previous period" onClick={() => go({ back: back + 1 })}>
        <ChevronLeft />
      </Button>
      <span className="min-w-23 text-center text-(length:--fs-sm) tabular-nums max-[760px]:hidden">{period}</span>
      <Button variant="ghost" size="icon" aria-label="Next period" disabled={back === 0} onClick={() => go({ back: back - 1 })}>
        <ChevronRight />
      </Button>
      <SegmentedControl aria-label="Range" value={[range]} onValueChange={(value) => value[0] && go({ range: value[0] as Range, back: 0 })} className="ms-(--s3)">
        <SegmentedControlItem value="day">Day</SegmentedControlItem>
        <SegmentedControlItem value="week">Week</SegmentedControlItem>
        <SegmentedControlItem value="month">Month</SegmentedControlItem>
      </SegmentedControl>
    </ToolbarSlot>
  )
}

// The legend doubles as totals and drills into an agent.
function Legend({ entry, color, unit, name, link }: { entry: { key: string; total: number }; color: string; unit: Kind; name: string; link?: string }) {
  const body = (
    <>
      <span className="flex items-center gap-(--s3) text-(length:--fs-sm) text-(--fg2)">
        <span aria-hidden className="size-[9px] shrink-0 rounded-[3px]" style={{ background: color }} />
        {name}
      </span>
      <span className="ps-[15px] font-semibold tabular-nums">{fmt(entry.total, unit)}</span>
    </>
  )
  const className = "-mx-(--s3) -my-(--s2) flex flex-col gap-px rounded-(--r-row) px-(--s3) py-(--s2)"
  return link ? (
    <Link to="/usage/$kind/$id" params={{ kind: "agent", id: link }} search={true} className={cn(className, "hover:bg-(--hover)")}>
      {body}
    </Link>
  ) : (
    <div className={className}>{body}</div>
  )
}

function Summary({ rows, sessions, view, names }: { rows: UsageHour[]; sessions: UsageSession[]; view: View; names: Names }) {
  const sum = (value: (row: UsageHour) => number) => rows.reduce((total, row) => total + value(row), 0)
  const reported = sessions.some((session) => session.cost_micros >= 0)
  const top = (dimension: Dimension) => [...totalsBy(rows, "out", dimensionKey[dimension])].sort((a, b) => b[1] - a[1])[0]?.[0]
  const other: Dimension = view.kind === "agent" ? "project" : "agent"
  const leader = top(other)
  const last = Math.max(0, ...sessions.map((session) => session.last_event_ms))
  return (
    <Section title="Summary">
      <dl className="flex flex-col gap-(--s2) text-(length:--fs-sm)">
        <KeyValue label="Sessions">{sessions.length}</KeyValue>
        <KeyValue label="Turns">{fmt(sum((row) => row.turns), "count")}</KeyValue>
        <KeyValue label="Output tokens">{fmt(sum((row) => row.output_tokens))}</KeyValue>
        <KeyValue label="All tokens">{fmt(sum(allTokens))}</KeyValue>
        <KeyValue label="Reported cost">{reported ? fmt(sum((row) => row.cost_micros) / 1e6, "cost") : "Not reported"}</KeyValue>
        <KeyValue label="Agent time">{fmt(sum((row) => row.agent_ms) / 60_000, "time")}</KeyValue>
        <KeyValue label={`Most used ${other}`}>{leader === undefined ? "—" : names.of(other, leader)}</KeyValue>
        <KeyValue label="Last active">{last ? new Date(last).toLocaleDateString(undefined, { month: "short", day: "numeric" }) : "—"}</KeyValue>
      </dl>
    </Section>
  )
}

const mostUsedDimensions: Record<View["kind"], Dimension[]> = {
  overview: ["project", "agent", "machine", "model"],
  limits: ["project", "agent", "machine", "model"],
  project: ["agent", "machine", "model"],
  machine: ["project", "agent", "model"],
  agent: ["project", "machine", "model"],
}

function MostUsed({ rows, view, metric, names }: { rows: UsageHour[]; view: View; metric: Metric; names: Names }) {
  const dimensions = mostUsedDimensions[view.kind]
  const [chosen, setChosen] = useState<Dimension>(dimensions[0]!)
  const dimension = dimensions.includes(chosen) ? chosen : dimensions[0]!
  const entries = [...totalsBy(rows, metric, dimensionKey[dimension])].filter(([, value]) => value > 0).sort((a, b) => b[1] - a[1]).slice(0, 5)
  const most = entries[0]?.[1] || 1
  return (
    <Section
      title="Most used"
      action={
        <SegmentedControl aria-label="Most used by" value={[dimension]} onValueChange={(value) => value[0] && setChosen(value[0] as Dimension)}>
          {dimensions.map((entry) => <SegmentedControlItem key={entry} value={entry}>{dimensionTitle[entry]}</SegmentedControlItem>)}
        </SegmentedControl>
      }
    >
      {entries.length === 0 ? (
        <p className="text-(length:--fs-sm) text-(--fg2)">Nothing in this range.</p>
      ) : (
        <ul className="-mx-(--s3) flex flex-col">
          {entries.map(([id, value]) => {
            const row = (
              <>
                {dimension === "agent" ? <AgentTile harness={id} state="idle" label={names.agent(id)} /> : dimension === "machine" ? <Server aria-hidden className="size-3.5 shrink-0 text-(--fg2)" /> : dimension === "project" ? <Folder aria-hidden className="size-3.5 shrink-0 text-(--fg2)" /> : <Cpu aria-hidden className="size-3.5 shrink-0 text-(--fg2)" />}
                <span className="min-w-0 flex-1 truncate">{names.of(dimension, id)}</span>
                <span aria-hidden className="h-1.5 w-35 shrink-0 overflow-hidden rounded-full bg-(--track) max-[760px]:w-20">
                  <span className="block h-full rounded-full bg-(--accent)" style={{ width: `${Math.max(2, (value / most) * 100)}%` }} />
                </span>
                <span className="w-14 shrink-0 text-right text-(length:--fs-sm) text-(--fg2) tabular-nums">{fmt(value, metrics[metric].kind)}</span>
                {dimension !== "model" && <ChevronRight aria-hidden className="size-3 shrink-0 text-(--fg3)" />}
              </>
            )
            const className = "flex h-(--row-h-2) min-w-0 items-center gap-(--s4) rounded-(--r-row) px-(--s3)"
            return (
              <li key={id} className="border-b-[0.5px] border-(--sep-soft) last:border-b-0">
                {dimension === "model" ? (
                  <div className={className}>{row}</div>
                ) : (
                  <Link to="/usage/$kind/$id" params={{ kind: dimension, id }} search={true} className={cn(className, "hover:bg-(--hover)")}>
                    {row}
                  </Link>
                )}
              </li>
            )
          })}
        </ul>
      )}
    </Section>
  )
}

function Efficiency({ rows, sessions }: { rows: UsageHour[]; sessions: UsageSession[] }) {
  const sum = (value: (row: UsageHour) => number) => rows.reduce((total, row) => total + value(row), 0)
  const turns = sum((row) => row.turns)
  const read = sum((row) => row.cache_read_tokens)
  const fresh = read + sum((row) => row.cache_write_tokens) + sum((row) => row.input_tokens)
  const hit = fresh ? (read / fresh) * 100 : undefined
  const costed = sessions.filter((session) => session.cost_micros >= 0)
  return (
    <Section title="Efficiency">
      <div className="mb-(--s3) flex flex-col gap-(--s1)">
        <div className="flex items-baseline justify-between text-(length:--fs-sm)">
          <span className="font-semibold">Cache hit rate</span>
          <span className="text-(--fg2) tabular-nums">{hit === undefined ? "—" : `${hit.toFixed(1)}%`}</span>
        </div>
        <Meter value={hit} label="Cache hit rate" />
      </div>
      <dl className="flex flex-col gap-(--s2) text-(length:--fs-sm)">
        <KeyValue label="Output per turn">{turns ? fmt(sum((row) => row.output_tokens) / turns) : "—"}</KeyValue>
        <KeyValue label="Turns per session">{sessions.length ? (turns / sessions.length).toFixed(1) : "—"}</KeyValue>
        <KeyValue label="Cost per session">{costed.length ? fmt(costed.reduce((total, session) => total + session.cost_micros, 0) / 1e6 / costed.length, "cost") : "—"}</KeyValue>
        <KeyValue label="Agent time per turn">{turns ? fmt(sum((row) => row.agent_ms) / 60_000 / turns, "time") : "—"}</KeyValue>
      </dl>
      <details className="mt-(--s3) text-(length:--fs-sm) text-(--fg2)">
        <summary className="cursor-default text-(--accent) [&::-webkit-details-marker]:hidden">About these numbers</summary>
        <p className="mt-(--s2) max-w-[62ch]"><b className="font-semibold text-(--fg)">Cache hit rate</b> is cache reads ÷ (cache reads + cache writes + fresh input). Above 95% is normal for long chats. A drop means prompts are being rebuilt, which is where cost goes.</p>
        <p className="mt-(--s2) max-w-[62ch]"><b className="font-semibold text-(--fg)">Output tokens</b> count what the agent wrote. They are the default because cache reads make up nearly all of "All tokens", which mostly measures context size.</p>
        <p className="mt-(--s2) max-w-[62ch]"><b className="font-semibold text-(--fg)">Cost</b> is what the agent reports (Claude, Grok, OMP). Codex and Cursor don't report it; those show "—", never $0. Session rows total each session's whole life.</p>
      </details>
    </Section>
  )
}

type Column = { key: string; label: string; numeric?: boolean; value: (session: UsageSession) => number | string }

const sessionTitle = (session: UsageSession) => session.title || session.name || basename(session.directory) || "Session"

const PAGE = 8

function SessionsTable({ sessions, view, names, pinned, onClear, className }: { sessions: UsageSession[]; view: View; names: Names; pinned?: string; onClear: () => void; className?: string }) {
  const [sort, setSort] = useState<{ key: string; dir: 1 | -1 }>({ key: "started", dir: -1 })
  const [all, setAll] = useState(false)
  const every: Column[] = [
    { key: "title", label: "Session", value: sessionTitle },
    { key: "project", label: "Project", value: (session) => basename(session.directory) },
    { key: "machine", label: "Machine", value: (session) => names.machine(session.node_id) },
    { key: "turns", label: "Turns", numeric: true, value: (session) => session.turns },
    { key: "output", label: "Output", numeric: true, value: (session) => session.output_tokens },
    { key: "cost", label: "Cost", numeric: true, value: (session) => session.cost_micros },
    { key: "started", label: "Started", numeric: true, value: (session) => session.started_ms },
  ]
  const columns = every.filter((column) => column.key !== view.kind)
  const by = columns.find((column) => column.key === sort.key) ?? columns[0]!
  const sorted = [...sessions].sort((a, b) => (by.value(a) > by.value(b) ? 1 : by.value(a) < by.value(b) ? -1 : 0) * sort.dir)
  const shown = all ? sorted : sorted.slice(0, PAGE)
  const today = new Date().toDateString()
  return (
    <Section
      className={className}
      title={
        <span className="flex items-center gap-(--s3)">
          Sessions <span className="font-normal text-(--fg3)">{sessions.length}</span>
          {pinned && (
            <span className="inline-flex h-(--ctl-h-sm) items-center gap-(--s2) rounded-full bg-(--accent-soft) ps-(--s3) pe-(--s2) font-normal text-(--accent)">
              {pinned}
              <button type="button" aria-label="Show every day" onClick={onClear} className="grid size-3.5 place-items-center rounded-full hover:bg-(--accent) hover:text-(--accent-fg)">
                <X aria-hidden className="size-3" />
              </button>
            </span>
          )}
        </span>
      }
    >
      <DataGrid aria-label="Sessions in this range">
        <DataGridHead>
          <tr>
            {columns.map((column) => (
              <DataGridHeaderCell key={column.key} aria-sort={sort.key === column.key ? (sort.dir > 0 ? "ascending" : "descending") : undefined} className={cn(column.numeric && "text-right", column.key === "title" && "w-[34%]")}>
                <button type="button" onClick={() => setSort({ key: column.key, dir: sort.key === column.key ? (-sort.dir as 1 | -1) : -1 })} className="cursor-default hover:text-(--fg)">
                  {column.label}
                  {sort.key === column.key && <span aria-hidden className="ms-0.5 text-[9px]">{sort.dir > 0 ? "▲" : "▼"}</span>}
                </button>
              </DataGridHeaderCell>
            ))}
          </tr>
        </DataGridHead>
        <tbody>
          {shown.map((session) => (
            <DataGridRow key={session.id}>
              {columns.map((column) => (
                <DataGridCell key={column.key} className={cn(column.numeric && "text-right tabular-nums", column.key === "started" && "text-(--fg2)")}>
                  {column.key === "title" ? (
                    <Link to="/sessions/$machine/$sessionId" params={{ machine: names.machineId(session.node_id), sessionId: session.id }} className="inline-flex max-w-full min-w-0 items-center gap-(--s3)">
                      <AgentTile harness={session.harness} state="idle" label={names.agent(session.harness)} />
                      <span className="truncate">{sessionTitle(session)}</span>
                    </Link>
                  ) : column.key === "cost" ? (
                    session.cost_micros < 0 ? <span className="text-(--fg3)">—</span> : fmt(session.cost_micros / 1e6, "cost")
                  ) : column.key === "started" ? (
                    session.started_ms ? (new Date(session.started_ms).toDateString() === today ? new Date(session.started_ms).toLocaleTimeString(undefined, { hour: "numeric", minute: "2-digit" }) : new Date(session.started_ms).toLocaleDateString(undefined, { month: "short", day: "numeric" })) : "—"
                  ) : column.key === "turns" ? (
                    fmt(session.turns, "count")
                  ) : column.key === "output" ? (
                    fmt(session.output_tokens)
                  ) : (
                    column.value(session)
                  )}
                </DataGridCell>
              ))}
            </DataGridRow>
          ))}
          {sessions.length === 0 && (
            <tr>
              <td colSpan={columns.length} className="px-(--s3) py-(--s4) text-(--fg2)">No sessions in this range.</td>
            </tr>
          )}
        </tbody>
      </DataGrid>
      {sessions.length > PAGE && (
        <button type="button" onClick={() => setAll(!all)} className="mt-(--s3) text-(length:--fs-sm) text-(--accent) hover:underline">
          {all ? "Show Fewer" : `Show All ${sessions.length}`}
        </button>
      )}
    </Section>
  )
}
