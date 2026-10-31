import { describe, expect, it } from "vitest"
import type { LimitSnapshot, LimitWindow } from "@/lib/api-types"
import { providerGroups, relativeUntil, remainingPercent, resetLabel, selectReport, type LimitReport } from "./model"

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

describe("relativeUntil", () => {
  it("uses minutes, then hours and minutes, then days", () => {
    expect(relativeUntil(5 * 60_000)).toBe("in 5m")
    expect(relativeUntil(90 * 60_000)).toBe("in 1h 30m")
    expect(relativeUntil(50 * 3_600_000)).toBe("in 2d")
  })
})

describe("resetLabel", () => {
  const now = Date.parse("2026-10-30T12:00:00Z")

  it("shows the provider's own reset text when no reset time is given", () => {
    expect(resetLabel(window({ reset_text: "Monday at 09:00" }), now)).toEqual({ text: "Monday at 09:00", due: false })
    expect(resetLabel(window({}), now)).toEqual({ text: "reset not exposed", due: false })
  })

  it("counts down to a future reset", () => {
    const label = resetLabel(window({ reset_at: "2026-10-30T14:00:00Z" }), now)
    expect(label.due).toBe(false)
    expect(label.text.startsWith("resets in 2h 0m · ")).toBe(true)
  })

  it("labels a passed reset as rechecking", () => {
    const label = resetLabel(window({ reset_at: "2026-10-30T11:59:00Z" }), now)
    expect(label.due).toBe(true)
    expect(label.text.startsWith("reset due · rechecking · was ")).toBe(true)
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

  it("still show when no machine has it", () => {
    expect(providerGroups([absent])[0]?.selected.id).toBe("vm")
  })
})
