import { describe, expect, it } from "vitest"
import type { Package, ProjectMCP, ProjectPackage, ProjectRow, Revision } from "@/lib/api-types"
import { agentpackRevisions, installedIn, mcpUses, packageRecord, packageVersions, revisionChanges, type RevisionEntry } from "./inspect"
import type { LibraryInput } from "./model"

const alpha = "github.com/acme/alpha"
const oracle = { id: "n2", name: "oracle-vm" }
const local = { id: "local", name: "host-a" }

function pin(module: string, commit: string): ProjectPackage {
  return { module, commit, content_hash: "", cache_key: "", kind: "skill", name: "", url: "", direct: true }
}

function folder(directory: string, packages: ProjectPackage[] = [], mcp: ProjectMCP[] = []): ProjectRow {
  return {
    directory,
    name: directory.split("/").pop() ?? directory,
    folder_id: "",
    available: true,
    unavailable_reason: "",
    last_used: "2026-10-01T00:00:00Z",
    launches: [
      { harness: "claude", mode: "default", last_used: "2026-10-01T00:00:00Z" },
      { harness: "codex", mode: "default", last_used: "2026-10-01T00:00:00Z" },
      { harness: "cursor", mode: "default", last_used: "2026-10-01T00:00:00Z" },
    ],
    packages,
    mcp,
    error: "",
  }
}

function pkg(overrides: Partial<Package>): Package {
  return { id: "p", module: alpha, commit: "abc1234", kind: "skill", url: "", owner: "", repo: "", cache_key: "", name: "alpha", ...overrides }
}

function revision(id: string, createdAt: string, packageIds: string[]): Revision {
  return { id, agentpack_id: "pk", manifest: "", lock: "", created_at: createdAt, package_ids: packageIds }
}

describe("installedIn", () => {
  it("gives one row per project per machine, however many harnesses launched it", () => {
    const inputs: LibraryInput[] = [
      { machine: local, packages: [], agentpacks: [], projects: [folder("/a/one", [pin(alpha, "abc1234")]), folder("/a/two", [pin(alpha, "abc1234")])] },
      { machine: oracle, packages: [], agentpacks: [], projects: [folder("/b/one", [pin(alpha, "abc1234")]), folder("/b/none")] },
    ]
    const rows = installedIn(inputs, alpha)
    expect(rows.map((row) => `${row.machine}:${row.project}`)).toEqual(["host-a:one", "host-a:two", "oracle-vm:one"])
    expect(new Set(rows.map((row) => row.key)).size).toBe(rows.length)
  })

  it("ignores other modules and pins without a commit", () => {
    const inputs: LibraryInput[] = [
      { machine: local, packages: [], agentpacks: [], projects: [folder("/a/one", [pin("github.com/acme/beta", "abc1234"), pin(alpha, "")])] },
    ]
    expect(installedIn(inputs, alpha)).toEqual([])
  })
})

describe("packageVersions", () => {
  it("lists the shown commit first, once each, across machines", () => {
    const inputs: LibraryInput[] = [
      { machine: local, packages: [pkg({ commit: "aaa1111" }), pkg({ module: "github.com/acme/beta", commit: "zzz9999" })], agentpacks: [], projects: [] },
      { machine: oracle, packages: [pkg({ commit: "bbb2222" }), pkg({ commit: "aaa1111" })], agentpacks: [], projects: [] },
    ]
    expect(packageVersions(inputs, alpha, "bbb2222")).toEqual(["bbb2222", "aaa1111"])
    expect(packageRecord(inputs, alpha, "bbb2222")?.commit).toBe("bbb2222")
  })
})

describe("agentpackRevisions", () => {
  it("resolves package ids on the machine that saved each revision, newest first", () => {
    const inputs: LibraryInput[] = [
      {
        machine: local,
        packages: [pkg({ id: "x", commit: "old1111" })],
        agentpacks: [{ agentpack: { id: "pk", name: "p", revisions: [], heads: [] }, revisions: [revision("r1", "2026-01-01T00:00:00Z", ["x"])] }],
        projects: [],
      },
      {
        machine: oracle,
        packages: [pkg({ id: "x", commit: "new2222" })],
        agentpacks: [{ agentpack: { id: "pk", name: "p", revisions: [], heads: [] }, revisions: [revision("r2", "2026-02-01T00:00:00Z", ["x"]), revision("r1", "2026-01-01T00:00:00Z", ["x"])] }],
        projects: [],
      },
    ]
    expect(agentpackRevisions(inputs, "pk").map((entry) => [entry.revision.id, entry.machine, entry.packages.map((pkg) => pkg.commit)])).toEqual([
      ["r2", "oracle-vm", ["new2222"]],
      ["r1", "host-a", ["old1111"]],
    ])
  })
})

describe("revisionChanges", () => {
  const entry = (packages: Package[]): RevisionEntry => ({ revision: revision("r", "2026-01-01T00:00:00Z", []), machine: "host-a", packages })

  it("reports adds, commit updates and removals by module", () => {
    const before = entry([pkg({ module: "m/a", commit: "1" }), pkg({ module: "m/b", commit: "1", name: "B" })])
    const after = entry([pkg({ module: "m/a", commit: "2", name: "A" }), pkg({ module: "m/c", commit: "1", name: "C" })])
    expect(revisionChanges(after, before)).toEqual([
      { kind: "update", name: "A" },
      { kind: "add", name: "C" },
      { kind: "remove", name: "B" },
    ])
  })

  it("is empty for the first revision", () => {
    expect(revisionChanges(entry([pkg({})]), undefined)).toEqual([])
  })
})

describe("mcpUses", () => {
  const server = { name: "foo", launcher: "npm", package: "@acme/foo", version: "1.2.3" }
  const mcp = (status: string, integrity: string): ProjectMCP => ({ ...server, source: "", status, integrity, host: "", reason: "" })

  it("matches the definition and reads status per project", () => {
    const inputs: LibraryInput[] = [
      { machine: local, packages: [], agentpacks: [], projects: [folder("/a/one", [], [mcp("pinned", "sha512-a")]), folder("/a/two", [], [mcp("unpinned", "")])] },
      { machine: oracle, packages: [], agentpacks: [], projects: [folder("/b/one", [], [{ ...mcp("pinned", "sha512-b"), version: "9.9.9" }])] },
    ]
    expect(mcpUses(inputs, server)).toEqual([
      { machine: "host-a", project: "one", directory: "/a/one", status: "pinned", integrity: "sha512-a" },
      { machine: "host-a", project: "two", directory: "/a/two", status: "unpinned", integrity: "" },
    ])
  })
})
