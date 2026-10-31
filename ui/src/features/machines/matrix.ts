import type { ProjectRow } from "@/lib/api-types"
import { basename } from "@/lib/format"

export type MatrixColumn = { module: string; name: string }
export type MatrixRow = { project: ProjectRow; pins: Map<string, string> }

// One row per project, one column per package module any project pins, sorted by name.
// Pins map module to commit; a module a project does not pin has no entry.
export function buildMatrix(projects: ProjectRow[]): { columns: MatrixColumn[]; rows: MatrixRow[] } {
  const columns = new Map<string, MatrixColumn>()
  const rows = projects.map((project) => {
    const pins = new Map<string, string>()
    for (const pkg of project.packages) {
      pins.set(pkg.module, pkg.commit)
      columns.set(pkg.module, { module: pkg.module, name: pkg.name || basename(pkg.module) })
    }
    return { project, pins }
  })
  const sorted = [...columns.values()].sort((a, b) => a.name.localeCompare(b.name) || a.module.localeCompare(b.module))
  return { columns: sorted, rows }
}
