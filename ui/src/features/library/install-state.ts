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

export function destinationState(item: Pick<InstallItem, "module" | "commit">, project?: ProjectRow) {
  if (project?.error) return { disabled: true, text: `Could not check pins · ${project.error}` }
  const pin = project?.packages.find((entry) => entry.module === item.module)
  if (pin?.commit === item.commit) return { disabled: true, text: "Already installed" }
  return {
    disabled: false,
    text: pin?.commit ? `Update ${shortHash(pin.commit)} → ${shortHash(item.commit)}` : "Not installed",
  }
}

// The destination’s last known pins, for checking what changed since the draft was saved.
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

export function canApplyDraft(review: DraftReview): boolean {
  return review.ready && review.folder_match && Boolean(review.diff?.preview_digest)
}

export function affectedSessions(sessions: SessionInfo[], directory: string): SessionInfo[] {
  return sessions.filter((session) => session.directory === directory && session.state.running && (session.state.interactive || session.transport === "acp"))
}

export function affectedAgents(sessions: SessionInfo[], directory: string): number {
  return affectedSessions(sessions, directory).length
}

export function affectedAgentLabels(sessions: SessionInfo[], directory: string): string[] {
  return affectedSessions(sessions, directory).map((session) => session.name || session.harness)
}

export function restartCopy(count: number): string {
  return count === 0 ? "" : `Restarts ${plural(count, "agent")}`
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
