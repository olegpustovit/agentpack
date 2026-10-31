import type { Package, Revision } from "@/lib/api-types"
import type { LibraryInput } from "./model"

export type InstalledRow = { key: string; machineId: string; online: boolean; machine: string; project: string; directory: string; commit: string }

const sameModule = (a: string, b: string) => a.toLowerCase() === b.toLowerCase()

// One row per project per machine. A project's harness launches add no rows.
export function installedIn(inputs: LibraryInput[], module: string): InstalledRow[] {
  const rows = inputs.flatMap((input) =>
    input.projects.flatMap((row) => {
      const pin = row.packages.find((pkg) => sameModule(pkg.module, module) && pkg.commit !== "")
      if (!pin) return []
      return [{ key: `${input.machine.id}\0${row.directory}`, machineId: input.machine.id, online: input.machine.online !== false && row.available, machine: input.machine.name, project: row.name || row.directory, directory: row.directory, commit: pin.commit }]
    }),
  )
  return rows.sort((a, b) => a.machine.localeCompare(b.machine) || a.project.localeCompare(b.project) || a.directory.localeCompare(b.directory))
}

// The shown commit first, then every commit of this module in any library. Several entries open the version popup.
export function packageVersions(inputs: LibraryInput[], module: string, current: string): string[] {
  const commits = [current]
  for (const input of inputs) for (const pkg of input.packages) if (sameModule(pkg.module, module)) commits.push(pkg.commit)
  return [...new Set(commits.filter((commit) => commit !== ""))]
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
        seen.set(revision.id, { revision, machine: input.machine.name, packages })
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

export type MCPUse = { machine: string; project: string; directory: string; status: string; integrity: string }

// Status and integrity belong to each project's own record, so they are read per project.
export function mcpUses(inputs: LibraryInput[], server: { name: string; launcher: string; package: string; version: string }): MCPUse[] {
  const uses = inputs.flatMap((input) =>
    input.projects.flatMap((row) =>
      row.mcp
        .filter((mcp) => mcp.name === server.name && mcp.launcher === server.launcher && mcp.package === server.package && mcp.version === server.version)
        .map((mcp) => ({ machine: input.machine.name, project: row.name || row.directory, directory: row.directory, status: mcp.status, integrity: mcp.integrity })),
    ),
  )
  return uses.sort((a, b) => a.machine.localeCompare(b.machine) || a.project.localeCompare(b.project) || a.directory.localeCompare(b.directory))
}
