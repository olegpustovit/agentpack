import { LOCAL } from "@/lib/api"
import type { Package, Revision } from "@/lib/api-types"
import type { LibraryInput } from "./model"

export type InstalledRow = { key: string; machineId: string; online: boolean; machine: string; project: string; directory: string; module: string; commit: string }

const sameModule = (a: string, b: string) => a.toLowerCase() === b.toLowerCase()

// One row per project per machine, for every source of the package. A project's harness launches add no rows.
export function installedIn(inputs: LibraryInput[], modules: string[]): InstalledRow[] {
  const rows = inputs.flatMap((input) =>
    input.projects.flatMap((row) =>
      row.packages
        .filter((pkg) => modules.some((module) => sameModule(pkg.module, module)) && pkg.commit !== "")
        .map((pin) => ({ key: `${input.machine.id}\0${row.directory}\0${pin.module}`, machineId: input.machine.id, online: input.machine.online !== false && row.available, machine: input.machine.name, project: row.name || row.directory, directory: row.directory, module: pin.module, commit: pin.commit })),
    ),
  )
  return rows.sort((a, b) => a.machine.localeCompare(b.machine) || a.project.localeCompare(b.project) || a.directory.localeCompare(b.directory))
}

export type VersionRow = { key: string; module: string; commit: string; projects: number; machines: string[]; seen: string }

// Every version any library knows, those in use first and most recently used on top.
export function packageVersions(inputs: LibraryInput[], modules: string[]): VersionRow[] {
  const versions = new Map<string, VersionRow>()
  const version = (module: string, commit: string) => {
    const key = `${module.toLowerCase()}@${commit}`
    let row = versions.get(key)
    if (!row) versions.set(key, (row = { key, module, commit, projects: 0, machines: [], seen: "" }))
    return row
  }
  const included = (module: string) => modules.some((other) => sameModule(other, module))
  for (const input of inputs) {
    for (const pkg of input.packages) if (included(pkg.module) && pkg.commit) version(pkg.module, pkg.commit)
    for (const project of input.projects) {
      for (const pin of project.packages) {
        if (!included(pin.module) || !pin.commit) continue
        const row = version(pin.module, pin.commit)
        row.projects += 1
        if (!row.machines.includes(input.machine.name)) row.machines.push(input.machine.name)
        if ((Date.parse(project.last_used) || 0) > (Date.parse(row.seen) || 0)) row.seen = project.last_used
      }
    }
  }
  return [...versions.values()].sort((a, b) => (Date.parse(b.seen) || 0) - (Date.parse(a.seen) || 0) || b.projects - a.projects || a.key.localeCompare(b.key))
}

// What the gateway needs to ask GitHub about one source. Undefined for a package that is not on GitHub.
export function latestRequest(inputs: LibraryInput[], module: string) {
  const [host, owner, repo, ...rest] = module.split("/")
  if (host?.toLowerCase() !== "github.com" || !owner || !repo) return undefined
  const installed = installedIn(inputs, [module])
  const record = inputs.flatMap((input) => input.packages).find((pkg) => sameModule(pkg.module, module))
  return {
    module,
    owner: record?.owner || owner,
    repo: record?.repo || repo,
    path: record?.path ?? rest.join("/"),
    pins: [...new Set(installed.map((row) => row.commit))].sort(),
    // A machine using the package is the likeliest to hold GitHub credentials for it.
    machine: installed.find((row) => row.online)?.machineId ?? LOCAL,
  }
}

export function packageRecord(inputs: LibraryInput[], module: string, commit: string): Package | undefined {
  for (const input of inputs) {
    const found = input.packages.find((pkg) => sameModule(pkg.module, module) && pkg.commit === commit)
    if (found) return found
  }
  return undefined
}

export type RevisionEntry = { revision: Revision; machine: string; packages: Package[] }

// Revisions of one agentpack, newest first. Package ids are per machine, so each revision
// resolves its packages against the machine that saved it.
export function agentpackRevisions(inputs: LibraryInput[], id: string): RevisionEntry[] {
  const seen = new Map<string, RevisionEntry>()
  for (const input of inputs) {
    for (const { agentpack, revisions } of input.agentpacks) {
      if (agentpack.id !== id) continue
      for (const revision of revisions ?? []) {
        if (seen.has(revision.id)) continue
        const packages = (revision.package_ids ?? []).flatMap((pid) => input.packages.filter((pkg) => pkg.id === pid))
        seen.set(revision.id, { revision, machine: revision.source_node || input.machine.name, packages })
      }
    }
  }
  return [...seen.values()].sort((a, b) => (Date.parse(b.revision.created_at) || 0) - (Date.parse(a.revision.created_at) || 0))
}

export type RevisionChange = { kind: "add" | "update" | "remove"; name: string }

// Module decides identity. A changed commit is an update. No previous revision means no diff.
export function revisionChanges(selected: RevisionEntry, previous: RevisionEntry | undefined): RevisionChange[] {
  if (!previous) return []
  const before = new Map(previous.packages.map((pkg) => [pkg.module, pkg]))
  const after = new Set(selected.packages.map((pkg) => pkg.module))
  const changes: RevisionChange[] = []
  for (const pkg of selected.packages) {
    const old = before.get(pkg.module)
    if (!old) changes.push({ kind: "add", name: pkg.name || pkg.module })
    else if (old.commit !== pkg.commit) changes.push({ kind: "update", name: pkg.name || pkg.module })
  }
  for (const pkg of previous.packages) if (!after.has(pkg.module)) changes.push({ kind: "remove", name: pkg.name || pkg.module })
  return changes
}

export type MCPUse = { machine: string; project: string; directory: string; status: string; integrity: string; version: string }

// Status and integrity belong to each project's own record, so they are read per project.
export function mcpUses(inputs: LibraryInput[], server: { name: string }): MCPUse[] {
  const uses = inputs.flatMap((input) =>
    input.projects.flatMap((row) =>
      row.mcp
        .filter((mcp) => mcp.name === server.name)
        .map((mcp) => ({ machine: input.machine.name, project: row.name || row.directory, directory: row.directory, status: mcp.status, integrity: mcp.integrity, version: mcp.version })),
    ),
  )
  return uses.sort((a, b) => a.machine.localeCompare(b.machine) || a.project.localeCompare(b.project) || a.directory.localeCompare(b.directory))
}
