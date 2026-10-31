import { describe, expect, it } from "vitest"
import type { ProjectPackage, ProjectRow } from "@/lib/api-types"
import { buildMatrix } from "./matrix"

function pkg(module: string, name: string, commit: string): ProjectPackage {
  return { module, commit, content_hash: "", cache_key: "", kind: "skill", name, url: "", direct: true }
}

function project(directory: string, packages: ProjectPackage[]): ProjectRow {
  return {
    directory,
    name: "",
    folder_id: "",
    available: true,
    unavailable_reason: "",
    last_used: "",
    launches: [],
    packages,
    mcp: [],
    error: "",
  }
}

const alpha = pkg("github.com/acme/alpha", "alpha", "aaaaaaa1111")
const beta = pkg("github.com/acme/beta", "", "bbbbbbb2222")

describe("buildMatrix", () => {
  it("gives one row per project and leaves unpinned cells empty", () => {
    const { columns, rows } = buildMatrix([project("/a", [alpha, beta]), project("/b", [beta])])

    expect(rows.map((row) => row.project.directory)).toEqual(["/a", "/b"])
    expect(columns.map((column) => column.module)).toEqual(["github.com/acme/alpha", "github.com/acme/beta"])
    expect(rows.map((row) => row.pins.has(alpha.module))).toEqual([true, false])
    expect(rows.map((row) => row.pins.get(beta.module))).toEqual([beta.commit, beta.commit])
  })

  it("names a column from its module when the package has no name, and sorts columns by that name", () => {
    const { columns } = buildMatrix([project("/a", [beta, alpha])])
    expect(columns).toEqual([
      { module: "github.com/acme/alpha", name: "alpha" },
      { module: "github.com/acme/beta", name: "beta" },
    ])
  })
})
