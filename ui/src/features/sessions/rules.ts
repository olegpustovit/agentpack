import type { ProjectRow } from "@/lib/api-types"
import { basename } from "@/lib/format"
import { ApiError } from "@/lib/api"
import type { ListedSession } from "./group"
import type { StartChoices } from "./choices"

export const agentIds = ["claude", "codex", "grok", "omp", "opencode", "agent", "agy"]

// What "+" and New session start in a folder: this browser's last choice there, else the folder's newest launch.
export function defaultAgent(remembered: StartChoices, launches: { harness: string; mode: string }[]) {
  const harness = remembered.harness ?? launches[0]?.harness ?? "claude"
  const mode = (remembered.harness === harness && remembered.mode) || launches.find((launch) => launch.harness === harness)?.mode || "default"
  return { harness, mode }
}

// Row action availability. Non-interactive sessions have no browser terminal.
export function sessionRules(session: ListedSession) {
  const pending = session.provisionalStart === "claim"
  const offline = session.offline || Boolean(session.archived_at) || pending
  const nonInteractive = session.state.interactive === false
  return {
    rename: !session.archived_at && !pending,
    restart: !offline && !nonInteractive,
    // A stopped chat starts its agent again on the same conversation.
    resume: !offline && session.transport === "acp" && !session.state.running && Boolean(session.ended_at),
    stop: !offline && session.state.running,
    reconnect: !offline && !nonInteractive,
    install: !offline,
    save: !offline,
    // A starting chat's agent is on its way up; Stop it once it runs.
    archive: !offline && !(session.conversation_state === "starting" && !session.ended_at),
  }
}

// A rejected request (4xx) drops the restart's operation id. A server error or a
// transport failure keeps it, so the retry is the same intent and the gateway can de-duplicate it.
export function reusesOperationId(error: unknown): boolean {
  return !(error instanceof ApiError && error.status >= 400 && error.status < 500)
}

export type SessionView = "chat" | "terminal"

export function startSessionBody(input: {
  directory: string
  harness: string
  mode: string
  view: SessionView
  agentSessionId?: string
  // "new" or the path of an existing worktree of directory; absent runs in the folder itself.
  worktree?: string
  worktreeName?: string
  carry?: boolean
}) {
  return {
    directory: input.directory,
    harness: input.harness,
    mode: input.mode.trim(),
    transport: input.view === "chat" ? "acp" : "terminal",
    ...(input.agentSessionId ? { agent_session_id: input.agentSessionId } : {}),
    ...(input.worktree ? { worktree: input.worktree, ...(input.worktreeName ? { worktree_name: input.worktreeName } : {}), ...(input.carry ? { carry: true } : {}) } : {}),
  }
}

// Modes the project defines, plus any it was launched with that it no longer lists. A CLI launch records "".
export function modeChoices(project: Pick<ProjectRow, "modes" | "launches">): string[] {
  const names = project.modes.map((mode) => mode.name)
  for (const launch of project.launches) names.push(launch.mode || "default")
  if (names.length === 0) names.push("default")
  return [...new Set(names)]
}

// What the chosen mode leaves out. Unknown modes (older peers) turn nothing off.
export function modeEffect(project: Pick<ProjectRow, "modes">, name: string) {
  const mode = project.modes.find((item) => item.name === name)
  return {
    offPackages: new Set(mode?.off_packages ?? []),
    partialPackages: new Set(mode?.partial_packages ?? []),
    offMCP: new Set(mode?.off_mcp ?? []),
  }
}

// A folder whose directory is known while its project contents are still being read.
export function stubRow(directory: string): ProjectRow {
  return { directory, name: basename(directory), folder_id: "", available: true, unavailable_reason: "", last_used: "", launches: [], packages: [], mcp: [], modes: [], error: "" }
}

// One folder on one machine. The key matches the sidebar's machine + folder subheads.
export type ProjectPlace = { key: string; machine: string; machineName: string; online: boolean; row: ProjectRow }
// Folders merge by name across machines, as the sidebar's Project grouping does.
export type MergedProject = { key: string; name: string; lastUsed: number; places: ProjectPlace[] }

export function placeKey(machine: string, directory: string): string {
  return `${machine}\0${directory.replace(/[\\/]+$/, "")}`
}

export function canStart(place: ProjectPlace): boolean {
  return place.online && place.row.available
}

// Machines in list order, each with the folders it reported. Startable places come first, newest first.
export function mergeProjects(machines: { id: string; name: string; online: boolean }[], rows: Record<string, ProjectRow[] | undefined>): MergedProject[] {
  const merged = new Map<string, MergedProject>()
  for (const machine of machines) {
    for (const row of rows[machine.id] ?? []) {
      const name = basename(row.directory) || row.directory
      const project = merged.get(name) ?? { key: name, name, lastUsed: 0, places: [] }
      merged.set(name, project)
      const key = placeKey(machine.id, row.directory)
      if (project.places.some((place) => place.key === key)) continue
      project.places.push({ key, machine: machine.id, machineName: machine.name, online: machine.online, row })
      project.lastUsed = Math.max(project.lastUsed, Date.parse(row.last_used) || 0)
    }
  }
  const used = (place: ProjectPlace) => Date.parse(place.row.last_used) || 0
  for (const project of merged.values()) {
    project.places.sort((a, b) => Number(canStart(b)) - Number(canStart(a)) || used(b) - used(a) || a.machineName.localeCompare(b.machineName))
  }
  return [...merged.values()].sort((a, b) => b.lastUsed - a.lastUsed || a.name.localeCompare(b.name))
}

// Dropdown label: the machine, its parent folder when that machine has two folders of this name, and why it cannot start.
export function placeLabel(place: ProjectPlace, places: ProjectPlace[]): string {
  const twins = places.filter((other) => other.machine === place.machine).length > 1
  const parent = place.row.directory.replace(/[\\/]+$/, "").split(/[\\/]/).slice(-2, -1)[0]
  return [place.machineName, twins && parent, placeProblem(place)].filter(Boolean).join(" · ")
}

// Why a session cannot start there; empty when it can.
export function placeProblem(place: ProjectPlace): string {
  if (!place.online) return "offline"
  return place.row.available ? "" : place.row.unavailable_reason || "folder missing"
}

const DAY = 24 * 60 * 60 * 1000

export function projectGroups(projects: MergedProject[], found: MergedProject[], machine: string, agent: string, query: string, now: number) {
  const needle = query.trim().toLowerCase()
  const scoped = (rows: MergedProject[], agent: string) => rows.flatMap((item) => {
    const places = item.places.filter((place) =>
      (machine === "all" || machine === place.machine) &&
      (agent === "all" || place.row.launches.some((launch) => launch.harness === agent)) &&
      [item.name, place.row.directory, place.machineName].some((part) => part.toLowerCase().includes(needle)),
    )
    if (!places.length) return []
    const lastUsed = Math.max(0, ...places.map((place) => Date.parse(place.row.last_used) || 0))
    return [{ ...item, places, lastUsed }]
  }).sort((a, b) => b.lastUsed - a.lastUsed || a.name.localeCompare(b.name))
  const shown = scoped(projects, agent)
  return [
    { label: "Today", rows: shown.filter((item) => now - item.lastUsed < DAY) },
    { label: "This week", rows: shown.filter((item) => now - item.lastUsed >= DAY && now - item.lastUsed < 7 * DAY) },
    { label: "Older", rows: shown.filter((item) => now - item.lastUsed >= 7 * DAY) },
    { label: "Found on machines", rows: agent === "all" ? scoped(found, agent) : [] },
  ]
}

export function projectLocation(item: MergedProject, machine: string): string {
  if (machine !== "all") return item.places[0]?.row.directory ?? ""
  const names = [...new Set(item.places.map((place) => place.machineName))]
  return [...names.slice(0, 2), ...(names.length > 2 ? [`+${names.length - 2}`] : [])].join(" · ")
}
