import type { AgentpackWithRevisions, Package, ProjectMCP, ProjectRow, Revision } from "@/lib/api-types"

export type LibraryType = "skill" | "plugin" | "mcp" | "agentpack"
export type LibraryFilter = "all" | LibraryType
export type LibrarySort = "type" | "name" | "updated" | "machine"

export type LibraryMachine = { id: string; name: string; online?: boolean }
export type LibraryProject = { machine: string; name: string; directory: string }

// One machine's answers from /api/library/packages, /api/library/agentpacks and /api/projects.
export type LibraryInput = {
  machine: LibraryMachine
  packages: Package[]
  agentpacks: AgentpackWithRevisions[]
  projects: ProjectRow[]
}

type Common = {
  key: string
  name: string
  // Machine display names, sorted.
  machines: string[]
  projects: LibraryProject[]
  // Newest timestamp the data carries for this item, or "" when none.
  updated: string
}

// `module` is the Go module path. `commit` is the version the list shows.
export type PackageItem = Common & {
  type: "skill" | "plugin"
  module: string
  commit: string
  // Every module published under this name: a moved repository or folder is one package to the person using it.
  sources: string[]
  // Distinct module and commit pairs across the sources.
  versions: number
}
export type MCPItem = Common & { type: "mcp"; launcher: string; package: string; version: string }
// `commit` is the newest revision id, so commit-prefix search works the same as for packages.
export type AgentpackItem = Common & { type: "agentpack"; id: string; commit: string; revisions: number; conflict: boolean }
export type LibraryItem = PackageItem | MCPItem | AgentpackItem

export type LibraryGroup = { key: string; label?: string; items: LibraryItem[] }
export type LibraryView = { counts: Record<LibraryFilter, number>; groups: LibraryGroup[] }

export const libraryTypes: LibraryType[] = ["skill", "plugin", "mcp", "agentpack"]
export const libraryTypeLabels: Record<LibraryType, string> = {
  skill: "Skills",
  plugin: "Plugins",
  mcp: "MCP servers",
  agentpack: "Agentpacks",
}
const sorts: LibrarySort[] = ["type", "name", "updated", "machine"]

const time = (value: string) => Date.parse(value) || 0
const newer = (a: string, b: string) => (time(b) > time(a) ? b : a)

// Projects are keyed by machine id, so two machines with the same folder stay apart.
function projectOf(input: LibraryInput, row: { directory: string; name: string }): [string, LibraryProject] {
  return [`${input.machine.id}\0${row.directory}`, { machine: input.machine.name, name: row.name || row.directory, directory: row.directory }]
}

type Gather = { machines: Set<string>; projects: Map<string, LibraryProject>; updated: string }

function gather(): Gather {
  return { machines: new Set(), projects: new Map(), updated: "" }
}

function addProject(g: Gather, [key, project]: [string, LibraryProject]) {
  g.machines.add(project.machine)
  g.projects.set(key, project)
}

function finish(key: string, name: string, g: Gather): Common {
  return { key, name, machines: [...g.machines].sort(), projects: [...g.projects.values()], updated: g.updated }
}

// Stored names are often empty, and a module path says nothing in a narrow list. A folder named after
// its role says nothing either, so those use the repository.
export function packageName(module: string, name?: string): string {
  if (name && !name.includes("/")) return name
  const parts = (name || module).split("/").filter(Boolean)
  const last = parts.at(-1) ?? module
  return ["plugin", "plugins", "skill", "skills"].includes(last.toLowerCase()) && parts.length > 1 ? parts.at(-2)! : last
}

type Pin = { module: string; commit: string; name: string; kind: string; seen: string; project: [string, LibraryProject] }

// One row per module. The pin used most recently wins; a module
// with no pins shows its first stored record. Identical module@commit records are one record.
function packageItems(inputs: LibraryInput[]): PackageItem[] {
  const stored = new Map<string, { pkg: Package; machines: Set<string> }>()
  const pins: Pin[] = []
  for (const input of inputs) {
    for (const pkg of input.packages) {
      const key = pkg.module && pkg.commit ? `${pkg.module.toLowerCase()}@${pkg.commit}` : pkg.id
      const hit = stored.get(key)
      // Replication gives every machine every record, so where it came from is what says something.
      const origin = pkg.source_node || input.machine.name
      if (hit) hit.machines.add(origin)
      else stored.set(key, { pkg, machines: new Set([origin]) })
    }
    for (const row of input.projects) {
      for (const pin of row.packages) {
        if (!pin.module || !pin.commit) continue
        pins.push({ module: pin.module, commit: pin.commit, name: pin.name, kind: pin.kind, seen: row.last_used, project: projectOf(input, row) })
      }
    }
  }

  const modules = new Map<string, { stored: { pkg: Package; machines: Set<string> }[]; pins: Pin[] }>()
  const groupOf = (module: string) => {
    const key = module.toLowerCase()
    let group = modules.get(key)
    if (!group) modules.set(key, (group = { stored: [], pins: [] }))
    return group
  }
  for (const entry of stored.values()) groupOf(entry.pkg.module).stored.push(entry)
  for (const pin of pins) groupOf(pin.module).pins.push(pin)

  const items: PackageItem[] = []
  for (const [module, group] of modules) {
    let chosen: Pin | undefined
    for (const pin of group.pins) if (!chosen || time(pin.seen) >= time(chosen.seen)) chosen = pin
    const first = group.stored[0]?.pkg
    const record = chosen ? stored.get(`${module}@${chosen.commit}`)?.pkg : undefined
    const picked = chosen
      ? { module: chosen.module, commit: chosen.commit, name: packageName(chosen.module, record?.name || chosen.name), kind: record?.kind || chosen.kind }
      : first && { module: first.module, commit: first.commit, name: packageName(first.module, first.name), kind: first.kind }
    if (!picked) continue

    const g = gather()
    for (const entry of group.stored) for (const machine of entry.machines) g.machines.add(machine)
    for (const pin of group.pins) {
      addProject(g, pin.project)
      g.updated = newer(g.updated, pin.seen)
    }
    items.push({
      ...finish(`pkg:${module}`, picked.name, g),
      type: picked.kind === "plugin" ? "plugin" : "skill",
      module: picked.module,
      commit: picked.commit,
      sources: [picked.module],
      versions: new Set([...group.stored.map((entry) => entry.pkg.commit), ...group.pins.map((pin) => pin.commit)]).size,
    })
  }
  return mergeSources(items)
}

// One row per name and kind. The most recently used source speaks for the row; the inspector lists them all.
function mergeSources(items: PackageItem[]): PackageItem[] {
  const groups = new Map<string, PackageItem[]>()
  for (const item of items) {
    const key = `pkg:${item.type}:${item.name.toLowerCase()}`
    groups.set(key, [...(groups.get(key) ?? []), item])
  }
  return [...groups].map(([key, group]) => {
    const [primary] = group.sort((a, b) => time(b.updated) - time(a.updated) || a.module.localeCompare(b.module)) as [PackageItem, ...PackageItem[]]
    const projects = new Map(group.flatMap((item) => item.projects.map((project) => [`${project.machine}\0${project.directory}`, project] as const)))
    return {
      ...primary,
      key,
      machines: [...new Set(group.flatMap((item) => item.machines))].sort(),
      projects: [...projects.values()],
      sources: group.flatMap((item) => item.sources),
      versions: group.reduce((sum, item) => sum + item.versions, 0),
    }
  })
}

// One item per agentpack id. Revisions merge by id.
function agentpackItems(inputs: LibraryInput[]): AgentpackItem[] {
  const packs = new Map<string, { name: string; revisions: Map<string, Revision>; conflict: boolean; g: Gather }>()
  for (const input of inputs) {
    for (const { agentpack, revisions } of input.agentpacks) {
      let pack = packs.get(agentpack.id)
      if (!pack) packs.set(agentpack.id, (pack = { name: agentpack.name, revisions: new Map(), conflict: false, g: gather() }))
      pack.conflict ||= (agentpack.heads?.length ?? 0) > 1
      for (const revision of revisions ?? []) {
        if (!pack.revisions.has(revision.id)) pack.revisions.set(revision.id, revision)
        pack.g.updated = newer(pack.g.updated, revision.created_at)
        // Replicated revisions reach every machine; the one that saved it is the one to name.
        const origin = revision.source_node || input.machine.name
        pack.g.machines.add(origin)
        if (revision.project) addProject(pack.g, [`${origin}\0${revision.project}`, { machine: origin, name: revision.project, directory: revision.project }])
      }
      if (!revisions?.length) pack.g.machines.add(input.machine.name)
    }
  }
  return [...packs].map(([id, pack]): AgentpackItem => {
    const newest = [...pack.revisions.values()].sort((a, b) => time(b.created_at) - time(a.created_at))[0]
    return {
      ...finish(`agentpack:${id}`, pack.name, pack.g),
      type: "agentpack",
      id,
      commit: newest?.id ?? "",
      revisions: pack.revisions.size,
      conflict: pack.conflict,
    }
  })
}

// One row per server name. The definition used most recently speaks for it; the inspector shows each project's own.
function mcpItems(inputs: LibraryInput[]): MCPItem[] {
  const servers = new Map<string, { server: ProjectMCP; seen: string; g: Gather }>()
  for (const input of inputs) {
    for (const row of input.projects) {
      for (const server of row.mcp) {
        const key = `mcp:${server.name}`
        let entry = servers.get(key)
        if (!entry) servers.set(key, (entry = { server, seen: row.last_used, g: gather() }))
        if (time(row.last_used) > time(entry.seen)) Object.assign(entry, { server, seen: row.last_used })
        addProject(entry.g, projectOf(input, row))
        entry.g.updated = newer(entry.g.updated, row.last_used)
      }
    }
  }
  return [...servers].map(([key, { server, g }]): MCPItem => ({
    ...finish(key, server.name, g),
    type: "mcp",
    launcher: server.launcher,
    package: server.package,
    version: server.version,
  }))
}

export function buildLibrary(inputs: LibraryInput[]): LibraryItem[] {
  return [...packageItems(inputs), ...mcpItems(inputs), ...agentpackItems(inputs)]
}

export function parseFilter(value: string | undefined): LibraryFilter {
  return value === "skill" || value === "plugin" || value === "mcp" || value === "agentpack" ? value : "all"
}

export function parseSort(value: string | undefined): LibrarySort {
  return sorts.find((sort) => sort === value) ?? "type"
}

// `q` is lowercase and trimmed. Module matches the npm package for MCP servers.
function matches(item: LibraryItem, q: string): boolean {
  const module = item.type === "mcp" ? item.package : item.type === "agentpack" ? "" : item.sources.join(" ")
  const commit = item.type === "mcp" ? "" : item.commit
  return (
    item.name.toLowerCase().includes(q) ||
    item.type.includes(q) ||
    module.toLowerCase().includes(q) ||
    commit.toLowerCase().startsWith(q) ||
    item.machines.some((machine) => machine.toLowerCase().includes(q)) ||
    item.projects.some((project) => project.name.toLowerCase().includes(q) || project.directory.toLowerCase().includes(q))
  )
}

const byName = (a: LibraryItem, b: LibraryItem) => a.name.localeCompare(b.name) || a.key.localeCompare(b.key)

const comparators: Record<LibrarySort, (a: LibraryItem, b: LibraryItem) => number> = {
  type: (a, b) => libraryTypes.indexOf(a.type) - libraryTypes.indexOf(b.type) || byName(a, b),
  name: byName,
  updated: (a, b) => time(b.updated) - time(a.updated) || byName(a, b),
  machine: (a, b) => (a.machines[0] ?? "").localeCompare(b.machines[0] ?? "") || byName(a, b),
}

// Counts reflect the search, so a tab shows how many matches it would hold.
// Only "Type" sort with All groups rows under headers; every other sort is one flat list.
export function arrangeLibrary(
  items: LibraryItem[],
  view: { filter: LibraryFilter; sort: LibrarySort; query: string },
): LibraryView {
  const q = view.query.trim().toLowerCase()
  const searched = q ? items.filter((item) => matches(item, q)) : items
  const counts: Record<LibraryFilter, number> = { all: searched.length, skill: 0, plugin: 0, mcp: 0, agentpack: 0 }
  for (const item of searched) counts[item.type] += 1

  const shown = searched.filter((item) => view.filter === "all" || item.type === view.filter).sort(comparators[view.sort])
  const groups: LibraryGroup[] =
    view.sort === "type" && view.filter === "all"
      ? libraryTypes
          .map((type) => ({ key: type, label: libraryTypeLabels[type], items: shown.filter((item) => item.type === type) }))
          .filter((group) => group.items.length > 0)
      : shown.length > 0
        ? [{ key: "all", items: shown }]
        : []
  return { counts, groups }
}
