import type { SessionInfo } from "@/lib/api-types"
import { basename, harnessName } from "@/lib/format"

export type GroupMode = "machine" | "project"

// A session with the machine it runs on. Offline rows are last-known and stay listed.
export type ListedSession = SessionInfo & { machine: string; machineName: string; offline: boolean }

export type SessionSubhead = {
  key: string
  label: string
  // Parent folder, shown dim only when another directory on the same machine has the same basename.
  parent: string | null
  sessions: ListedSession[]
}

export type SessionGroup = {
  key: string
  label: string
  // Only meaningful in machine mode, where a group is one machine.
  offline: boolean
  subheads: SessionSubhead[]
}

export function rowKey(session: ListedSession): string {
  return `${session.machine}:${session.id}`
}

export function sessionLabel(session: ListedSession): string {
  return session.name || harnessName(session.harness)
}

const UNKNOWN_PROJECT = "Unknown project"

// A trailing separator would otherwise make "/a/x/" and "/a/x" two folders.
function trimSlashes(path: string): string {
  return path.replace(/[\\/]+$/, "")
}

function parentName(directory: string): string {
  const parts = directory.split(/[\\/]/)
  return parts[parts.length - 2] ?? ""
}

function sortByLabel<T extends { label: string; key: string }>(items: T[]): T[] {
  return items.sort((a, b) => a.label.localeCompare(b.label) || a.key.localeCompare(b.key))
}

// Machine mode: machine → project → session. Project mode: project → machine → session.
// Projects merge by folder basename across machines. Two directories with one basename on one
// machine stay separate and show their parent folder.
export function groupSessions(sessions: ListedSession[], group: GroupMode): SessionGroup[] {
  const directoriesByFolder = new Map<string, Set<string>>()
  for (const session of sessions) {
    const directory = trimSlashes(session.directory)
    const key = `${session.machine}\0${basename(directory)}`
    directoriesByFolder.set(key, (directoriesByFolder.get(key) ?? new Set<string>()).add(directory))
  }

  type Pending = Omit<SessionGroup, "subheads"> & { heads: Map<string, SessionSubhead> }
  const groups = new Map<string, Pending>()
  for (const session of sessions) {
    const directory = trimSlashes(session.directory)
    const folder = basename(directory)
    const byMachine = group === "machine"
    const groupKey = byMachine ? `machine\0${session.machine}` : `project\0${folder}`
    let entry = groups.get(groupKey)
    if (!entry) {
      entry = {
        key: groupKey,
        label: byMachine ? session.machineName : folder || UNKNOWN_PROJECT,
        offline: session.offline,
        heads: new Map(),
      }
      groups.set(groupKey, entry)
    }

    const headKey = `${session.machine}\0${directory}`
    let head = entry.heads.get(headKey)
    if (!head) {
      const shared = (directoriesByFolder.get(`${session.machine}\0${folder}`)?.size ?? 0) > 1
      head = {
        key: headKey,
        label: byMachine ? folder || UNKNOWN_PROJECT : session.machineName,
        parent: shared ? parentName(directory) : null,
        sessions: [],
      }
      entry.heads.set(headKey, head)
    }
    head.sessions.push(session)
  }

  return sortByLabel(
    [...groups.values()].map(({ heads, ...rest }) => ({ ...rest, subheads: sortByLabel([...heads.values()]) })),
  )
}
