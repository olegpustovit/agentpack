import type { Machine } from "@/features/machines/queries"
import type { PackagePin, ProjectRow, SessionInfo } from "@/lib/api-types"
import { plural, shortHash } from "@/lib/format"
import type { DraftReview } from "./mutations"

// The package being installed or restored, with the identity a draft must pin.
export type InstallItem = { module: string; commit: string; name: string; cache_key: string; content_hash?: string }

export type Destination = {
  machine: string
  machineName: string
  online: boolean
  project: ProjectRow
}

export type DestinationState = {
  disabled: boolean
  // Non-empty when the row is saved as a draft instead of installed now.
  draftReason: string
  text: string
}

// A row without a folder id or cache key cannot be drafted.
export function destinationState(item: InstallItem, target: Destination): DestinationState {
  const { project } = target
  const draftable = project.folder_id !== "" && item.cache_key !== ""
  if (!target.online) {
    return draftable
      ? { disabled: false, draftReason: "Machine offline", text: "Offline · install as draft" }
      : { disabled: true, draftReason: "", text: "Offline · exact package identity unavailable" }
  }
  if (!project.available) {
    const reason = project.unavailable_reason || "Folder unavailable"
    return draftable
      ? { disabled: false, draftReason: reason, text: `Folder unavailable · ${reason}` }
      : { disabled: true, draftReason: "", text: reason }
  }
  if (project.error) return { disabled: true, draftReason: "", text: `Could not check pins · ${project.error}` }
  const pin = project.packages.find((entry) => entry.module === item.module)
  if (pin?.commit === item.commit) return { disabled: true, draftReason: "", text: "Installed" }
  return {
    disabled: false,
    draftReason: "",
    text: pin?.commit ? `Update ${shortHash(pin.commit)} → ${shortHash(item.commit)}` : "Not installed",
  }
}

// The exact pins a draft must still match. Throws when the identity is incomplete, so the row can say so.
export function expectedPins(items: InstallItem[]): Record<string, PackagePin> {
  const pins: Record<string, PackagePin> = {}
  for (const item of items) {
    if (!item.module || !item.commit || !item.cache_key) {
      throw new Error(`Exact package identity is unavailable for ${item.module || "this revision"}.`)
    }
    pins[item.module] = { commit: item.commit, cache_key: item.cache_key, ...(item.content_hash ? { content_hash: item.content_hash } : {}) }
  }
  return pins
}

// Runs `work` with at most `limit` items in flight per key. Keys run side by side.
export async function runLimited<T>(items: T[], keyOf: (item: T) => string, limit: number, work: (item: T) => Promise<void>): Promise<void> {
  const groups = new Map<string, T[]>()
  for (const item of items) {
    const key = keyOf(item)
    const group = groups.get(key)
    if (group) group.push(item)
    else groups.set(key, [item])
  }
  await Promise.all(
    [...groups.values()].map((group) => {
      const queue = [...group]
      return Promise.all(
        Array.from({ length: Math.min(limit, queue.length) }, async () => {
          for (let item = queue.shift(); item !== undefined; item = queue.shift()) await work(item)
        }),
      )
    }),
  )
}

// A draft applies only if the review taken just before applying is still ready and every pin matches.
export function canApplyDraft(review: DraftReview): boolean {
  return review.ready && review.pins_match && review.folder_match
}

export function affectedAgents(sessions: SessionInfo[], directory: string): number {
  return sessions.filter((session) => session.directory === directory && session.state.running && session.state.interactive).length
}

export function restartCopy(count: number): string {
  return count === 0 ? "" : `Restarts ${plural(count, "agent")}`
}

export type InstallPhase = "pick" | "run" | "done"

export function installLabel(phase: InstallPhase, installs: number, drafts: number): string {
  if (phase === "run") return "Installing…"
  if (phase === "done") return "Done"
  if (installs === 0 && drafts === 0) return "Install"
  if (installs === 0) return `Save ${plural(drafts, "draft")}`
  if (drafts === 0) return `Install in ${plural(installs, "project")}`
  return `Install ${installs} · Draft ${drafts}`
}

// Every project on every machine, in machine order. Offline machines keep their last known projects.
export function destinationsOf(machines: Machine[], byMachine: Record<string, { data: ProjectRow[] | undefined }>): Destination[] {
  return machines.flatMap((machine) =>
    (byMachine[machine.id]?.data ?? []).map((project) => ({ machine: machine.id, machineName: machine.name, online: machine.online, project })),
  )
}

export type RestorePlan = { kind: "restore" | "draft" | "blocked"; text: string; reason: string }

// Only a checked destination can be previewed and restored now.
export function restorePlan(target: Destination, items: InstallItem[]): RestorePlan {
  const { project, online } = target
  if (online && project.available && !project.error) return { kind: "restore", text: "", reason: "" }
  const reason = !online ? "Machine offline" : !project.available ? project.unavailable_reason || "Folder unavailable" : project.error || "Project could not be checked"
  if (project.folder_id !== "" && items.every((item) => item.cache_key !== "")) return { kind: "draft", text: `${reason} · save as draft`, reason }
  return { kind: "blocked", text: online ? reason : "Offline", reason }
}
