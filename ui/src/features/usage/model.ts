import { basename } from "@/lib/format"

// Usage arrives as hourly rows (Unix seconds at the start of the hour), grouped here into the
// viewer's local days or hours. Session rows carry each session's lifetime totals.

export type UsageHour = {
  hour: number
  node_id: string
  harness: string
  folder_id: string
  model: string
  turns: number
  sessions: number
  input_tokens: number
  output_tokens: number
  cache_read_tokens: number
  cache_write_tokens: number
  cost_micros: number
  agent_ms: number
}

export type UsageSession = {
  id: string
  node_id: string
  title: string
  name: string
  directory: string
  folder_id: string
  harness: string
  model: string
  turns: number
  input_tokens: number
  output_tokens: number
  cache_read_tokens: number
  cache_write_tokens: number
  // -1: the agent doesn't report cost.
  cost_micros: number
  started_ms: number
  last_event_ms: number
}

export type LimitSample = { hour: number; node_id: string; harness: string; window_id: string; used_percent?: number | null; reset_at: number }

export type UsageCoverage = { node_id: string; state: "current" | "pending" | "offline" | "stale"; last_sync?: string }

export type UsageReport = { hourly: UsageHour[]; sessions: UsageSession[]; samples: LimitSample[]; coverage?: UsageCoverage[] }

export type Range = "day" | "week" | "month"

export type Kind = "tokens" | "cost" | "count" | "time"

type Counters = Pick<UsageHour, "input_tokens" | "output_tokens" | "cache_read_tokens" | "cache_write_tokens" | "cost_micros">

export const allTokens = (row: Counters) => row.input_tokens + row.output_tokens + row.cache_read_tokens + row.cache_write_tokens

// Output tokens lead: cache reads are nearly all of "All tokens", which mostly measures context size.
export const metrics = {
  out: { label: "Output tokens", kind: "tokens", value: (row: UsageHour) => row.output_tokens },
  all: { label: "All tokens", kind: "tokens", value: allTokens },
  cost: { label: "Reported cost", kind: "cost", value: (row: UsageHour) => Math.max(0, row.cost_micros) / 1e6 },
  sessions: { label: "New sessions", kind: "count", value: (row: UsageHour) => row.sessions },
  time: { label: "Agent time", kind: "time", value: (row: UsageHour) => row.agent_ms / 60_000 },
} as const satisfies Record<string, { label: string; kind: Kind; value: (row: UsageHour) => number }>

export type Metric = keyof typeof metrics

// Cost is -1 when the agent doesn't report it, so "no cost" and "$0" must stay apart.
export const reportsCost = (rows: { cost_micros: number }[]) => rows.some((row) => row.cost_micros >= 0)

// Shown under a cost total that only some sessions contribute to; empty when coverage is complete.
export function costCoverage(sessions: { cost_micros: number }[]): string {
  const reported = sessions.filter((session) => session.cost_micros >= 0).length
  return reported === sessions.length ? "" : `${reported} of ${sessions.length} sessions report cost`
}

export const metricOf = (value: string | undefined): Metric => (value && value in metrics ? (value as Metric) : "out")
export const rangeOf = (value: string | undefined): Range => (value === "day" || value === "month" ? value : "week")

// folder_id is only unique per machine (an inode on Unix), so a project is its folder name, as in Sessions:
// the same name merges across machines, and equal IDs on two machines stay apart.
export function byProject(report: UsageReport): UsageReport {
  const names = new Map(report.sessions.map((session) => [`${session.node_id}\0${session.folder_id}`, basename(session.directory)]))
  const named = <T extends { node_id: string; folder_id: string }>(row: T): T => ({
    ...row,
    folder_id: row.folder_id && (names.get(`${row.node_id}\0${row.folder_id}`) || "Unknown folder"),
  })
  return { ...report, hourly: report.hourly.map(named), sessions: report.sessions.map(named) }
}

// The filters on the Usage page, at most one value per dimension; a row must match all of them.
export type View = { project?: string; machine?: string; agent?: string }

export function inView(row: { folder_id: string; node_id: string; harness: string }, view: View): boolean {
  return (view.project === undefined || row.folder_id === view.project)
    && (view.machine === undefined || row.node_id === view.machine)
    && (view.agent === undefined || row.harness === view.agent)
}

// Bucket starts in ms. Day: 24 hours; week (Monday first) and month: local days, so a DST day keeps its own length.
export type Window = { range: Range; back: number; starts: number[]; end: number }

export function windowOf(range: Range, back: number, now: number): Window {
  const today = new Date(now)
  const y = today.getFullYear()
  const m = today.getMonth()
  const d = today.getDate()
  if (range === "day") {
    const starts = Array.from({ length: 24 }, (_, hour) => new Date(y, m, d - back, hour).getTime())
    return { range, back, starts, end: new Date(y, m, d - back + 1).getTime() }
  }
  if (range === "week") {
    const monday = d - ((today.getDay() + 6) % 7) - 7 * back
    return { range, back, starts: Array.from({ length: 7 }, (_, i) => new Date(y, m, monday + i).getTime()), end: new Date(y, m, monday + 7).getTime() }
  }
  const days = new Date(y, m - back + 1, 0).getDate()
  return { range, back, starts: Array.from({ length: days }, (_, i) => new Date(y, m - back, i + 1).getTime()), end: new Date(y, m - back + 1, 1).getTime() }
}

// Index of the bucket holding the time, or -1 outside the window.
export function bucketOf(win: Window, ms: number): number {
  if (ms < win.starts[0]! || ms >= win.end) return -1
  let index = 0
  while (index + 1 < win.starts.length && win.starts[index + 1]! <= ms) index++
  return index
}

export const bucketEnd = (win: Window, index: number) => win.starts[index + 1] ?? win.end

export type Series = { key: string; total: number; values: number[] }

// Per-bucket totals, stacked by a row field and largest series first.
export function bucketize(rows: UsageHour[], win: Window, metric: Metric, stack: (row: UsageHour) => string) {
  const totals = win.starts.map(() => 0)
  const byKey = new Map<string, Series>()
  for (const row of rows) {
    const index = bucketOf(win, row.hour * 1000)
    if (index < 0) continue
    const value = metrics[metric].value(row)
    const key = stack(row)
    let series = byKey.get(key)
    if (!series) byKey.set(key, (series = { key, total: 0, values: win.starts.map(() => 0) }))
    series.values[index]! += value
    series.total += value
    totals[index]! += value
  }
  return { totals, series: [...byKey.values()].filter((series) => series.total > 0).sort((a, b) => b.total - a.total) }
}

export function totalsBy(rows: UsageHour[], metric: Metric, key: (row: UsageHour) => string): Map<string, number> {
  const out = new Map<string, number>()
  for (const row of rows) out.set(key(row), (out.get(key(row)) ?? 0) + metrics[metric].value(row))
  return out
}

// The average spreads over the buckets that have begun, so a Tuesday is not averaged with the rest of the week.
export function averageOf(totals: number[], win: Window, now: number): number {
  const begun = Math.max(1, win.starts.filter((start) => start <= now).length)
  return totals.slice(0, begun).reduce((sum, value) => sum + value, 0) / begun
}

const date = (ms: number, options: Intl.DateTimeFormatOptions) => new Date(ms).toLocaleDateString(undefined, options)
const time = (ms: number) => new Date(ms).toLocaleTimeString(undefined, { hour: "numeric" })

export function rangeName(win: Window): string {
  const { range, back } = win
  if (range === "day") return back === 0 ? "Today" : back === 1 ? "Yesterday" : date(win.starts[0]!, { weekday: "long", month: "short", day: "numeric" })
  if (range === "week") return back === 0 ? "This Week" : back === 1 ? "Last Week" : `${date(win.starts[0]!, { month: "short", day: "numeric" })} – ${date(win.end - 1, { month: "short", day: "numeric" })}`
  return back === 0 ? "This Month" : date(win.starts[0]!, { month: "long", year: "numeric" })
}

// Short labels thin out to fit: every 6 hours, every weekday, and weekly days in a month.
export function bucketLabel(win: Window, index: number, long = false): string {
  const start = win.starts[index]!
  if (win.range === "day") return long ? `${time(start)}–${time(bucketEnd(win, index))}` : index % 6 === 0 ? time(start) : ""
  if (long) return date(start, { weekday: "long", month: "short", day: "numeric" })
  if (win.range === "week") return date(start, { weekday: "short" })
  return index % 7 === 0 ? date(start, { month: "short", day: "numeric" }) : ""
}

export function fmt(value: number, kind: Kind = "tokens"): string {
  if (kind === "cost") return value >= 100 ? `$${Math.round(value).toLocaleString("en-US")}` : `$${value.toFixed(2)}`
  if (kind === "count") return Math.round(value).toLocaleString("en-US")
  if (kind === "time") {
    const minutes = Math.round(value)
    return minutes >= 60 ? `${Math.floor(minutes / 60)} h ${minutes % 60} min` : `${minutes} min`
  }
  return value >= 1e9 ? `${(value / 1e9).toFixed(2)}B` : value >= 1e6 ? `${(value / 1e6).toFixed(1)}M` : value >= 1e3 ? `${Math.round(value / 1e3)}K` : String(Math.round(value))
}

// A session belongs to a bucket when it was active at any point inside it.
export const activeIn = (session: UsageSession, from: number, to: number) => session.started_ms < to && session.last_event_ms >= from

// Change against the period before, in whole percent; undefined when there is nothing to compare with.
export function changeOf(current: number, previous: number): number | undefined {
  return previous > 0 ? Math.round(((current - previous) / previous) * 100) : undefined
}

export type UsageDataState = "loading" | "error" | "empty" | "partial" | "ready"

// Distinguishes loading, failed read, confirmed zero, partial coverage from offline peers, and complete data.
export function usageDataState(input: {
  pending: boolean
  error: boolean
  hasData: boolean
  incompleteCount: number
}): UsageDataState {
  if (input.error) return "error"
  if (input.pending) return "loading"
  if (input.incompleteCount > 0) return "partial"
  return input.hasData ? "ready" : "empty"
}

function timeZoneLabel(ms: number): string {
  const parts = new Intl.DateTimeFormat(undefined, { timeZoneName: "short" }).formatToParts(new Date(ms))
  return parts.find((part) => part.type === "timeZoneName")?.value ?? ""
}

// Exact inclusive calendar dates plus viewer timezone, so "This Week" is unambiguous.
export function exactRangeLabel(win: Window): string {
  const start = win.starts[0]!
  const endInclusive = win.end - 1
  const tz = timeZoneLabel(start)
  const suffix = tz ? ` (${tz})` : ""
  if (win.range === "day") {
    return `${date(start, { month: "short", day: "numeric", year: "numeric" })}${suffix}`
  }
  const sameYear = new Date(start).getFullYear() === new Date(endInclusive).getFullYear()
  const from = date(start, sameYear ? { month: "short", day: "numeric" } : { month: "short", day: "numeric", year: "numeric" })
  const to = date(endInclusive, { month: "short", day: "numeric", year: "numeric" })
  return `${from} – ${to}${suffix}`
}

// Concise label naming the prior comparison window (e.g. "Nov 9 – Nov 15").
export function comparisonWindowLabel(previous: Window): string {
  const start = previous.starts[0]!
  const endInclusive = previous.end - 1
  if (previous.range === "day") return date(start, { month: "short", day: "numeric" })
  return `${date(start, { month: "short", day: "numeric" })} – ${date(endInclusive, { month: "short", day: "numeric" })}`
}
