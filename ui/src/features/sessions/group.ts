import type { SessionInfo } from "@/lib/api-types"
import { basename, harnessName, relativeTime } from "@/lib/format"
import { projectDirectory } from "./git"

export type GroupMode = "machine" | "project"

// A session with the machine it runs on. Offline rows are last-known and stay listed.
export type ListedSession = SessionInfo & { machine: string; machineName: string; offline: boolean }

export type SessionSubhead = {
  key: string
  label: string
  // The one folder on one machine this subhead lists, so New session can start there.
  machine: string
  directory: string
  offline: boolean
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

// An unnamed terminal says what it is, so it never reads like a chat with that agent.
export function sessionLabel(session: ListedSession): string {
  return session.name || session.title || (session.transport === "acp" ? harnessName(session.harness) : `Terminal · ${harnessName(session.harness)}`)
}

// When the row last changed: conversation activity, else the launch.
export function lastActivity(session: ListedSession): string | undefined {
  return session.last_event_at ?? session.last_used ?? session.started_at
}

const UNKNOWN_PROJECT = "Unknown project"

// Row tooltip and terminal subtitle: harness, folder, machine, who started it, when.
export function sessionDetail(session: ListedSession): string {
  const actor = session.started_by
  const started = actor?.agent ? `started by ${harnessName(actor.agent)}` : actor?.user ? `started by ${actor.user} from ${actor.kind}` : undefined
  const when = lastActivity(session)
  return [
    harnessName(session.harness),
    basename(projectDirectory(session)) || UNKNOWN_PROJECT,
    session.machineName,
    started,
    when ? relativeTime(new Date(when)) : undefined,
  ]
    .filter(Boolean)
    .join(" · ")
}

// What the filter field matches: everything the row and its tooltip show.
export function matchesSession(session: ListedSession, query: string): boolean {
  const needle = query.trim().toLowerCase()
  return !needle || [session.name, session.title, harnessName(session.harness), session.harness, session.mode, session.directory, session.machineName].some((part) => part?.toLowerCase().includes(needle))
}

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
    const directory = trimSlashes(projectDirectory(session))
    const key = `${session.machine}\0${basename(directory)}`
    directoriesByFolder.set(key, (directoriesByFolder.get(key) ?? new Set<string>()).add(directory))
  }

  type Pending = Omit<SessionGroup, "subheads"> & { heads: Map<string, SessionSubhead> }
  const groups = new Map<string, Pending>()
  for (const session of sessions) {
    const directory = trimSlashes(projectDirectory(session))
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
        machine: session.machine,
        directory,
        offline: session.offline,
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

// Helper chats follow the chat whose agent started them, when both are in the list.
export function nestHelpers(sessions: ListedSession[]): { session: ListedSession; nested: boolean }[] {
  const listed = new Set(sessions.map((session) => session.id))
  const children = new Map<string, ListedSession[]>()
  for (const session of sessions) {
    if (session.parent_id && listed.has(session.parent_id)) children.set(session.parent_id, [...(children.get(session.parent_id) ?? []), session])
  }
  const out: { session: ListedSession; nested: boolean }[] = []
  const visit = (session: ListedSession, nested: boolean) => {
    out.push({ session, nested })
    for (const child of children.get(session.id) ?? []) visit(child, true)
  }
  for (const session of sessions) if (!session.parent_id || !listed.has(session.parent_id)) visit(session, false)
  // A parent loop has no root; its rows still show.
  const shown = new Set(out.map((entry) => entry.session))
  for (const session of sessions) if (!shown.has(session)) out.push({ session, nested: false })
  return out
}
