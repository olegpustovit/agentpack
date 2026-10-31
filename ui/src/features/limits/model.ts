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
  return [...byProvider].map(([provider, list]) => {
    const selected = selectReport(list)
    return { provider, selected, others: list.filter((report) => report !== selected && !missing(report)), usable: usable(selected.snapshot) }
  })
}

// Null when the provider reports neither field; clamped so the meter and label agree.
export function remainingPercent(window: LimitWindow): number | undefined {
  const left = window.remaining_percent ?? (window.used_percent == null ? undefined : 100 - window.used_percent)
  return left === undefined ? undefined : Math.min(100, Math.max(0, left))
}

export function relativeUntil(ms: number): string {
  const minutes = Math.round(ms / 60_000)
  if (minutes < 60) return `in ${minutes}m`
  const hours = Math.floor(minutes / 60)
  return hours < 48 ? `in ${hours}h ${minutes % 60}m` : `in ${Math.round(hours / 24)}d`
}

export function resetLabel(window: LimitWindow, now: number): { text: string; due: boolean } {
  if (!window.reset_at) return { text: window.reset_text || "reset not exposed", due: false }
  const at = new Date(window.reset_at)
  const absolute = at.toLocaleString(undefined, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    timeZoneName: "short",
  })
  const remaining = at.getTime() - now
  if (remaining <= 0) return { text: `reset due · rechecking · was ${absolute}`, due: true }
  return { text: `resets ${relativeUntil(remaining)} · ${absolute}`, due: false }
}
