import { afterAll, beforeAll, describe, expect, it } from "vitest"
import { averageOf, bucketOf, bucketize, byProject, inView, comparisonWindowLabel, costCoverage, exactRangeLabel, fmt, metrics, reportsCost, usageDataState, windowOf, type UsageHour, type UsageSession } from "./model"

// New York: 4 h behind UTC until DST ends on Sunday, Nov 1 2026, 5 h after.
const zone = process.env.TZ
beforeAll(() => {
  process.env.TZ = "America/New_York"
})
afterAll(() => {
  if (zone === undefined) delete process.env.TZ
  else process.env.TZ = zone
})

const at = (iso: string) => Date.parse(iso)
const row = (iso: string, harness: string, output_tokens: number): UsageHour => ({
  hour: at(iso) / 1000,
  node_id: "n1",
  harness,
  folder_id: "f1",
  model: "",
  turns: 1,
  sessions: 0,
  input_tokens: 0,
  output_tokens,
  cache_read_tokens: 0,
  cache_write_tokens: 0,
  cost_micros: 0,
  agent_ms: 0,
})

describe("local day buckets", () => {
  const wednesday = at("2026-11-04T15:00:00Z")

  it("starts the week on the viewer's Monday and files UTC hours under local days", () => {
    const week = windowOf("week", 0, wednesday)
    expect(new Date(week.starts[0]!).toString()).toContain("Mon Nov 02 2026 00:00:00")
    expect(bucketOf(week, at("2026-11-03T03:00:00Z"))).toBe(0) // Monday 22:00 local
    expect(bucketOf(week, at("2026-11-03T05:00:00Z"))).toBe(1) // Tuesday 00:00 local
    expect(bucketOf(week, at("2026-11-02T04:00:00Z"))).toBe(-1) // Sunday 23:00 local: last week
  })

  it("keeps the 25-hour day when daylight saving ends", () => {
    const last = windowOf("week", 1, wednesday)
    const sunday = 6
    expect(new Date(last.starts[sunday]!).toString()).toContain("Sun Nov 01 2026 00:00:00")
    expect(last.end - last.starts[sunday]!).toBe(25 * 3_600_000)
    expect(bucketOf(last, at("2026-11-02T04:00:00Z"))).toBe(sunday)
  })

  it("stacks series per bucket, largest first, and averages only the days begun", () => {
    const week = windowOf("week", 0, wednesday)
    const { totals, series } = bucketize(
      [row("2026-11-03T03:00:00Z", "claude", 100), row("2026-11-03T05:00:00Z", "codex", 300), row("2026-11-03T06:00:00Z", "claude", 50)],
      week,
      "out",
      (r) => r.harness,
    )
    expect(totals.slice(0, 3)).toEqual([100, 350, 0])
    expect(series.map((s) => [s.key, s.total])).toEqual([["codex", 300], ["claude", 150]])
    expect(averageOf(totals, week, wednesday)).toBe(150) // Monday to Wednesday
  })

  it("has a bucket per day of the month and per hour of the day", () => {
    expect(windowOf("month", 0, wednesday).starts).toHaveLength(30)
    expect(windowOf("month", 1, wednesday).starts).toHaveLength(31)
    expect(windowOf("day", 0, wednesday).starts).toHaveLength(24)
  })
})

describe("fmt", () => {
  it("formats the way macOS does", () => {
    expect([fmt(1_340_000), fmt(980_400), fmt(12), fmt(1772.4, "cost"), fmt(3.5, "cost"), fmt(125, "time")]).toEqual(["1.3M", "980K", "12", "$1,772", "$3.50", "2 h 5 min"])
  })
})

it("names the sessions metric for what it counts: sessions whose first turn falls in the bucket", () => {
  const row = { sessions: 1, turns: 3 } as UsageHour
  expect(metrics.sessions.label).toBe("New sessions")
  expect(metrics.sessions.value(row)).toBe(1)
})

describe("unreported cost", () => {
  it("keeps unreported apart from zero and names partial coverage", () => {
    expect(reportsCost([{ cost_micros: -1 }, { cost_micros: -1 }])).toBe(false)
    expect(reportsCost([{ cost_micros: -1 }, { cost_micros: 0 }])).toBe(true)
    expect(costCoverage([{ cost_micros: 5 }, { cost_micros: -1 }, { cost_micros: 0 }])).toBe("2 of 3 sessions report cost")
    expect(costCoverage([{ cost_micros: 5 }])).toBe("")
  })
})

describe("usageDataState and range labels", () => {
  it("distinguishes error, loading, partial, empty, and ready", () => {
    expect(usageDataState({ pending: false, error: true, hasData: false, incompleteCount: 0 })).toBe("error")
    expect(usageDataState({ pending: true, error: false, hasData: false, incompleteCount: 0 })).toBe("loading")
    expect(usageDataState({ pending: false, error: false, hasData: false, incompleteCount: 1 })).toBe("partial")
    expect(usageDataState({ pending: false, error: false, hasData: false, incompleteCount: 0 })).toBe("empty")
    expect(usageDataState({ pending: false, error: false, hasData: true, incompleteCount: 0 })).toBe("ready")
  })

  it("formats exact inclusive dates with timezone and comparison window", () => {
    const wednesday = at("2026-11-04T15:00:00Z")
    const week = windowOf("week", 0, wednesday)
    const prev = windowOf("week", 1, wednesday)
    expect(exactRangeLabel(week)).toMatch(/Nov 2 – Nov 8, 2026 \(/)
    expect(comparisonWindowLabel(prev)).toBe("Oct 26 – Nov 1")
  })
})

describe("projects and filters", () => {
  const hourRow = (node_id: string, folder_id: string, harness = "claude") => ({ ...row("2026-11-04T10:00:00Z", harness, 1), node_id, folder_id })
  const session = (node_id: string, folder_id: string, directory: string) => ({ node_id, folder_id, directory }) as UsageSession

  it("names a project by its folder, merging across machines and keeping equal IDs apart", () => {
    const { hourly } = byProject({
      samples: [],
      sessions: [session("a", "1", "/work/agentpack"), session("b", "2", "/srv/agentpack/"), session("b", "1", "/srv/rays"), session("w", "9", "C:\\code\\agentpack")],
      hourly: [hourRow("a", "1"), hourRow("b", "2"), hourRow("b", "1"), hourRow("w", "9"), hourRow("a", "404"), hourRow("a", "")],
    })
    expect(hourly.map((entry) => entry.folder_id)).toEqual(["agentpack", "agentpack", "rays", "agentpack", "Unknown folder", ""])
  })

  it("requires every filter to match", () => {
    const entry = hourRow("a", "agentpack", "codex")
    expect(inView(entry, {})).toBe(true)
    expect(inView(entry, { project: "agentpack", machine: "a", agent: "codex" })).toBe(true)
    expect(inView(entry, { project: "agentpack", agent: "claude" })).toBe(false)
    expect(inView(entry, { project: "" })).toBe(false)
  })
})
