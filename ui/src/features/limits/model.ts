import type { LimitSnapshot, LimitWindow } from "@/lib/api-types"

export type LimitReport = { id: string; name: string; snapshot: LimitSnapshot }

export type ProviderGroup = {
  provider: string
  selected: LimitReport
  others: LimitReport[]
  usable: boolean
}

// Go sends a nil slice as null, so an empty snapshot can arrive without windows.
export function windowsOf(snapshot: LimitSnapshot): LimitWindow[] {
  return snapshot.windows ?? []
}

// When the machine last measured these, in ms since epoch. Older peers may not say.
export function observedAt(snapshots: LimitSnapshot[], fallback: number): number {
  const times = snapshots.map((snapshot) => Date.parse(snapshot.observed_at ?? "")).filter((time) => time > 0)
  return times.length ? Math.max(...times) : fallback
}

export function usable(snapshot: LimitSnapshot): boolean {
  return snapshot.state === "available" && windowsOf(snapshot).length > 0
}

// The CLI is absent on that machine, which says nothing about the provider. Matched on text because
// peers on older versions report it the same way, with no separate state.
function missing(report: LimitReport): boolean {
  return report.snapshot.error?.endsWith("CLI not found") === true
}

// One account per provider: prefer a usable machine, then one whose CLI answered with an error, then
// one that has the CLI at all. Windows are never summed across machines, so the others stay under a disclosure.
export function selectReport(reports: LimitReport[]): LimitReport {
  return (
    reports.find((report) => usable(report.snapshot)) ??
    reports.find((report) => report.snapshot.state === "error" && !missing(report)) ??
    reports.find((report) => !missing(report)) ??
    reports[0]!
  )
}

export function providerGroups(reports: LimitReport[]): ProviderGroup[] {
  const byProvider = new Map<string, LimitReport[]>()
  for (const report of reports) {
    const provider = report.snapshot.harness
    byProvider.set(provider, [...(byProvider.get(provider) ?? []), report])
  }
  // A provider whose CLI no machine has is not a limit worth a card.
  return [...byProvider].filter(([, list]) => list.some((report) => !missing(report))).map(([provider, list]) => {
    const selected = selectReport(list)
    return { provider, selected, others: list.filter((report) => report !== selected && !missing(report)), usable: usable(selected.snapshot) }
  })
}

// Null when the provider reports neither field; clamped so the meter and label agree.
export function remainingPercent(window: LimitWindow): number | undefined {
  const left = window.remaining_percent ?? (window.used_percent == null ? undefined : 100 - window.used_percent)
  return left === undefined ? undefined : Math.min(100, Math.max(0, left))
}

// Countdown in whole minutes, rounded up so a pending reset never reads "0 m".
export function untilText(ms: number): string {
  const minutes = Math.max(1, Math.ceil(ms / 60_000))
  if (minutes < 60) return `${minutes} m`
  const hours = Math.floor(minutes / 60)
  if (hours < 48) return `${hours} h ${minutes % 60} m`
  return `${Math.floor(hours / 24)} d ${hours % 24} h`
}

// A reset that has passed is "refreshing…": the next poll brings the new numbers, so no absolute time is shown.
export function resetText(window: LimitWindow, now: number): { text: string; refreshing: boolean } {
  const at = Date.parse(window.reset_at ?? "")
  if (Number.isNaN(at)) return { text: window.reset_text ?? "", refreshing: false }
  const remaining = at - now
  if (remaining <= 0) return { text: "refreshing…", refreshing: true }
  return { text: `resets in ${untilText(remaining)}`, refreshing: false }
}

// Providers report a missing login as an error message; there is no separate auth state.
const loginError = /login|sign.?in|authentication|signed out/i
export function needsLogin(snapshot: LimitSnapshot): boolean {
  return snapshot.state === "error" && loginError.test(snapshot.error ?? "")
}

// Only the sign-in commands agentpack ships; other providers show the message alone.
export const loginCommands: Record<string, string | undefined> = {
  codex: "agentpack codex login",
  grok: "agentpack grok login",
}

// The usable window with the least left across providers, which the sidebar and the Limits hero lead with.
export function tightestWindow(groups: ProviderGroup[]): { group: ProviderGroup; window: LimitWindow; left: number } | undefined {
  let best: { group: ProviderGroup; window: LimitWindow; left: number } | undefined
  for (const group of groups) {
    if (!group.usable) continue
    for (const window of windowsOf(group.selected.snapshot)) {
      const left = remainingPercent(window)
      if (left !== undefined && (!best || left < best.left)) best = { group, window, left }
    }
  }
  return best
}

// When the window runs out at the pace used since it opened; undefined when that is after its reset or unknown.
export function runsOutAt(window: LimitWindow, now: number): number | undefined {
  const reset = Date.parse(window.reset_at ?? "")
  const left = remainingPercent(window)
  if (!window.duration_seconds || Number.isNaN(reset) || left === undefined || reset <= now) return undefined
  const elapsed = window.duration_seconds * 1000 - (reset - now)
  const used = 100 - left
  if (elapsed <= 0 || used <= 0) return undefined
  const end = now + (left / used) * elapsed
  return end < reset ? end : undefined
}

export function paceText(window: LimitWindow, now: number): { text: string; warn: boolean } {
  const end = runsOutAt(window, now)
  if (end !== undefined) return { text: `Runs out in about ${untilText(end - now)} at this pace`, warn: true }
  return { text: window.duration_seconds && window.reset_at ? "On pace" : "", warn: false }
}
