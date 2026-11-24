import { describe, expect, it } from "vitest"
import type { LimitSnapshot, LimitWindow } from "@/lib/api-types"
import { absoluteResetTime, disagreementSummary, isWindowStale, needsLogin, observedAt, paceText, providerGroups, remainingPercent, resetText, runsOutAt, selectReport, untilText, type LimitReport } from "./model"

const snapshot = (over: Partial<LimitSnapshot>): LimitSnapshot => ({
  harness: "codex",
  state: "available",
  observed_at: "2026-10-30T10:00:00Z",
  windows: [],
  ...over,
})
const window = (over: Partial<LimitWindow>): LimitWindow => ({ id: "w", label: "Session", ...over })
const report = (id: string, over: Partial<LimitSnapshot>): LimitReport => ({ id, name: id, snapshot: snapshot(over) })
const usableWindow = [window({ remaining_percent: 72 })]

describe("selectReport", () => {
  it("prefers the freshest usable report over a stale one", () => {
    const stale = report("atlas", { windows: [window({ remaining_percent: 75 })], observed_at: "2026-10-29T10:00:00Z" })
    const fresh = report("birch", { windows: [window({ remaining_percent: 4 })], observed_at: "2026-10-30T10:00:00Z" })
    expect(selectReport([stale, fresh])).toBe(fresh)
  })

  it("prefers a machine with usable windows over the gateway's unsupported report", () => {
    const gateway = report("local", { state: "unsupported", error: "codex CLI not found" })
    const peer = report("peer-1", { state: "available", windows: usableWindow })
    expect(selectReport([gateway, peer])).toBe(peer)
  })

  it("prefers an error report over an unsupported one when nothing is usable", () => {
    const unsupported = report("local", { state: "unsupported" })
    const failed = report("peer-1", { state: "error", error: "Sign in required" })
    expect(selectReport([unsupported, failed])).toBe(failed)
  })

  it("keeps the gateway's usable report over a peer's error", () => {
    const gateway = report("local", { state: "available", windows: usableWindow })
    const peer = report("peer-1", { state: "error" })
    expect(selectReport([gateway, peer])).toBe(gateway)
  })
})

describe("providerGroups", () => {
  it("keeps one selected account per provider and lists the rest as others", () => {
    const groups = providerGroups([
      report("local", { state: "unsupported" }),
      report("peer-1", { state: "available", windows: usableWindow }),
      report("peer-2", { state: "available", windows: usableWindow }),
    ])
    expect(groups).toHaveLength(1)
    expect(groups[0]?.selected.id).toBe("peer-1")
    expect(groups[0]?.others.map((entry) => entry.id)).toEqual(["local", "peer-2"])
    expect(groups[0]?.usable).toBe(true)
  })

  it("marks a provider with no usable report as not usable", () => {
    const [group] = providerGroups([report("local", { harness: "grok", state: "error", error: "authentication required" })])
    expect(group?.usable).toBe(false)
  })
})

describe("remainingPercent", () => {
  it("derives remaining from used when the provider reports only used", () => {
    expect(remainingPercent(window({ used_percent: 28 }))).toBe(72)
  })

  it("clamps out-of-range values and returns undefined when neither field is reported", () => {
    expect(remainingPercent(window({ remaining_percent: 130 }))).toBe(100)
    expect(remainingPercent(window({ remaining_percent: -4 }))).toBe(0)
    expect(remainingPercent(window({}))).toBeUndefined()
  })
})

describe("untilText", () => {
  it("counts minutes, then hours and minutes, then days and hours", () => {
    expect(untilText(42 * 60_000)).toBe("42 m")
    expect(untilText(72 * 60_000)).toBe("1 h 12 m")
    expect(untilText(50 * 3_600_000)).toBe("2 d 2 h")
  })

  it("rounds up, so a pending reset never reads zero", () => {
    expect(untilText(1)).toBe("1 m")
    expect(untilText(60_001)).toBe("2 m")
  })
})

describe("resetText", () => {
  const now = Date.parse("2026-10-30T12:00:00Z")

  it("counts down to a future reset", () => {
    expect(resetText(window({ reset_at: "2026-10-30T13:12:00Z" }), now)).toEqual({ text: "resets in 1 h 12 m", refreshing: false })
  })

  it("says refreshing once the reset has passed", () => {
    expect(resetText(window({ reset_at: "2026-10-30T11:59:00Z" }), now)).toEqual({ text: "refreshing…", refreshing: true })
  })

  it("shows the provider's own reset text when no reset time is given", () => {
    expect(resetText(window({ reset_text: "Monday at 09:00" }), now)).toEqual({ text: "Monday at 09:00", refreshing: false })
    expect(resetText(window({}), now)).toEqual({ text: "", refreshing: false })
  })
})

describe("needsLogin", () => {
  it("is true for an error that names a missing login", () => {
    expect(needsLogin(snapshot({ state: "error", error: "grok billing: authentication required" }))).toBe(true)
    expect(needsLogin(snapshot({ state: "error", error: "Grok login expired. Sign in with agentpack grok login." }))).toBe(true)
  })

  it("is false for other errors and for available providers", () => {
    expect(needsLogin(snapshot({ state: "error", error: "codex reported no rate-limit windows" }))).toBe(false)
    expect(needsLogin(snapshot({ state: "unsupported", error: "codex CLI not found" }))).toBe(false)
    expect(needsLogin(snapshot({ state: "available" }))).toBe(false)
  })
})

describe("machines without the CLI", () => {
  const report = (id: string, state: string, error?: string): LimitReport => ({ id, name: id, snapshot: { harness: "opencode", state, error, observed_at: "", windows: [] } })
  const absent = report("vm", "unsupported", "opencode CLI not found")
  const present = report("main", "unsupported", "no OpenCode Go usage limits for this account")

  it("never outrank a machine that has it, and are not listed as others", () => {
    const [group] = providerGroups([absent, present])
    expect(group?.selected.id).toBe("main")
    expect(group?.others).toEqual([])
  })

  it("hide the provider when no machine has it", () => {
    expect(providerGroups([absent])).toEqual([])
  })
})

describe("observedAt", () => {
  it("is the newest measurement, and the fallback when a peer reports none", () => {
    const older = snapshot({ observed_at: "2026-10-30T09:00:00Z" })
    expect(observedAt([older, snapshot({})], 1)).toBe(Date.parse("2026-10-30T10:00:00Z"))
    expect(observedAt([], 1)).toBe(1)
    expect(observedAt([snapshot({ observed_at: "0001-01-01T00:00:00Z" })], 1)).toBe(1)
  })
})

describe("runsOutAt", () => {
  const now = Date.parse("2026-11-19T12:00:00Z")
  const fiveHour = (remaining: number, resetMinutes: number) =>
    window({ remaining_percent: remaining, duration_seconds: 5 * 3600, reset_at: new Date(now + resetMinutes * 60_000).toISOString() })

  it("projects the pace since the window opened to its end", () => {
    // 1 h in with half used: the other half lasts 1 h, well before the reset in 4 h.
    expect(runsOutAt(fiveHour(50, 240), now)).toBe(now + 3_600_000)
    expect(paceText(fiveHour(50, 240), now)).toEqual({ text: "Runs out in about 1 h 0 m at this pace", warn: true })
    // 1 h in with 10% used lasts past the reset.
    expect(runsOutAt(fiveHour(90, 240), now)).toBeUndefined()
    expect(paceText(fiveHour(90, 240), now)).toEqual({ text: "On pace", warn: false })
  })

  it("says nothing without a window length", () => {
    expect(paceText(window({ remaining_percent: 10 }), now)).toEqual({ text: "", warn: false })
  })
})

describe("staleness, disagreement, and absolute reset time", () => {
  const now = Date.parse("2026-10-30T12:00:00Z")

  it("ranks a current report ahead of one whose reset already passed and flags stale windows", () => {
    const expired = report("atlas", {
      observed_at: "2026-10-30T11:55:00Z",
      windows: [window({ id: "w", remaining_percent: 75, reset_at: "2026-10-30T11:50:00Z" })],
    })
    const fresh = report("birch", {
      observed_at: "2026-10-30T11:50:00Z",
      windows: [window({ id: "w", remaining_percent: 4, reset_at: "2026-10-30T15:00:00Z" })],
    })
    expect(selectReport([expired, fresh], now)).toBe(fresh)
    expect(isWindowStale(expired.snapshot.windows[0]!, Date.parse(expired.snapshot.observed_at), now)).toBe(true)
    expect(isWindowStale(fresh.snapshot.windows[0]!, Date.parse(fresh.snapshot.observed_at), now)).toBe(false)
  })

  it("summarizes material disagreement between machines", () => {
    const stale = report("atlas", {
      observed_at: "2026-10-29T12:00:00Z",
      windows: [window({ id: "w", remaining_percent: 75, reset_at: "2026-10-30T15:00:00Z" })],
    })
    const fresh = report("birch", {
      observed_at: "2026-10-30T11:59:00Z",
      windows: [window({ id: "w", remaining_percent: 4, reset_at: "2026-10-30T15:00:00Z" })],
    })
    const [group] = providerGroups([stale, fresh], now)
    expect(disagreementSummary(group!, now)).toContain("birch reports 4% left")
    expect(disagreementSummary(group!, now)).toContain("atlas reported 75%")
  })

  it("formats an absolute reset time only for future resets", () => {
    expect(absoluteResetTime(window({ reset_at: "2026-10-30T14:30:00Z" }), now)).toMatch(/\d+:\d+/)
    expect(absoluteResetTime(window({ reset_at: "2026-10-30T11:00:00Z" }), now)).toBe("")
  })
})
