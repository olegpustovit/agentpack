import { describe, expect, it } from "vitest"
import type { AgentpackWithRevisions, Package, ProjectMCP, ProjectPackage, ProjectRow, Revision } from "@/lib/api-types"
import { arrangeLibrary, buildLibrary, parseFilter, parseSort, type LibraryInput, type LibraryMachine, type LibraryView } from "./model"

const oracle: LibraryMachine = { id: "local", name: "oracle-vm" }
const studio: LibraryMachine = { id: "n2", name: "mac-studio" }

function pkg(overrides: Partial<Package>): Package {
  return { id: "r1", module: "github.com/acme/tools", commit: "abcd1234", kind: "skill", url: "", owner: "acme", repo: "tools", cache_key: "k", name: "Tools", ...overrides }
}

function pin(overrides: Partial<ProjectPackage>): ProjectPackage {
  return { module: "github.com/acme/tools", commit: "abcd1234", content_hash: "", cache_key: "", kind: "skill", name: "", url: "", direct: true, ...overrides }
}

function folder(overrides: Partial<ProjectRow>): ProjectRow {
  return {
    directory: "/srv/app",
    name: "app",
    folder_id: "",
    available: true,
    unavailable_reason: "",
    last_used: "2026-01-01T00:00:00Z",
    launches: [],
    packages: [],
    mcp: [],
    error: "",
    ...overrides,
  }
}

function server(overrides: Partial<ProjectMCP>): ProjectMCP {
  return { name: "github", source: "", launcher: "npm", status: "ok", package: "@acme/github-mcp", version: "1.2.3", integrity: "", host: "", reason: "", ...overrides }
}

function revision(overrides: Partial<Revision>): Revision {
  return { id: "rev1", agentpack_id: "p1", manifest: "", lock: "", created_at: "2026-01-01T00:00:00Z", ...overrides }
}

function pack(id: string, name: string, revisions: Revision[]): AgentpackWithRevisions {
  return { agentpack: { id, name, revisions: revisions.map((r) => r.id), heads: [] }, revisions }
}

function input(machine: LibraryMachine, parts: Partial<Omit<LibraryInput, "machine">>): LibraryInput {
  return { machine, packages: [], agentpacks: [], projects: [], ...parts }
}

// Port of gateway.spec.js "Library deduplicates identical pins with different saved record IDs":
// three records, two identical module@commit pins and one older version of the same module.
// The list shows one row per module.
describe("buildLibrary dedupe", () => {
  it("Library deduplicates identical pins", () => {
    const first = pkg({ id: "first-record", name: "Tools", kind: "plugin" })
    const items = buildLibrary([
      input(oracle, {
        packages: [first, { ...first, id: "another-record" }, { ...first, id: "older-version", commit: "older123" }],
      }),
    ])
    expect(items).toHaveLength(1)
    expect(items[0]).toMatchObject({ type: "plugin", module: "github.com/acme/tools", commit: "abcd1234", machines: ["oracle-vm"] })
  })

  it("matches modules case-insensitively", () => {
    const items = buildLibrary([
      input(oracle, { packages: [pkg({ id: "a" })] }),
      input(studio, { projects: [folder({ packages: [pin({ module: "GitHub.com/Acme/Tools" })] })] }),
    ])
    expect(items).toHaveLength(1)
    expect(items[0]?.machines).toEqual(["mac-studio", "oracle-vm"])
  })

  it("shows the pin used most recently, with projects from every version", () => {
    const items = buildLibrary([
      input(oracle, {
        projects: [
          folder({ directory: "/a", name: "a", last_used: "2026-01-01T00:00:00Z", packages: [pin({ commit: "abcd1234" })] }),
          folder({ directory: "/b", name: "b", last_used: "2026-05-01T00:00:00Z", packages: [pin({ commit: "older123" })] }),
        ],
      }),
    ])
    expect(items).toHaveLength(1)
    expect(items[0]).toMatchObject({ commit: "older123", updated: "2026-05-01T00:00:00Z" })
    expect(items[0]?.projects.map((p) => p.name)).toEqual(["a", "b"])
  })

  it("merges MCP servers by name and definition, not by name alone", () => {
    const items = buildLibrary([
      input(oracle, { projects: [folder({ mcp: [server({})] })] }),
      input(studio, { projects: [folder({ directory: "/b", name: "b", mcp: [server({ status: "error" })] })] }),
      input(studio, { projects: [folder({ directory: "/c", name: "c", mcp: [server({ version: "2.0.0" })] })] }),
    ])
    const mcp = items.filter((item) => item.type === "mcp")
    expect(mcp).toHaveLength(2)
    const merged = mcp.find((item) => item.type === "mcp" && item.version === "1.2.3")
    expect(merged).toMatchObject({ name: "github", launcher: "npm", machines: ["mac-studio", "oracle-vm"] })
    expect(merged?.projects).toHaveLength(2)
  })

  it("merges agentpacks by id, merges revisions, and takes the newest revision as the commit", () => {
    const items = buildLibrary([
      input(oracle, {
        agentpacks: [pack("p1", "Gamma", [revision({ id: "r1", created_at: "2026-01-01T00:00:00Z" }), revision({ id: "r2", created_at: "2026-03-01T00:00:00Z" })])],
      }),
      input(studio, {
        agentpacks: [pack("p1", "Gamma", [revision({ id: "r2", created_at: "2026-03-01T00:00:00Z" }), revision({ id: "r3", created_at: "2026-02-01T00:00:00Z" })])],
      }),
    ])
    expect(items).toHaveLength(1)
    expect(items[0]).toMatchObject({ type: "agentpack", id: "p1", name: "Gamma", commit: "r2", revisions: 3, updated: "2026-03-01T00:00:00Z" })
  })
})

// Fixture with one item of each type, shared by the arrangement tests.
function fixture() {
  return buildLibrary([
    input(oracle, {
      packages: [pkg({ id: "alpha", module: "github.com/acme/alpha", commit: "1111111", name: "alpha" })],
      projects: [folder({ directory: "/srv/app", name: "app", last_used: "2026-01-10T00:00:00Z", packages: [pin({ module: "github.com/acme/alpha", commit: "1111111", name: "alpha" })] })],
    }),
    input(studio, {
      packages: [pkg({ id: "beta", module: "github.com/acme/beta", commit: "2222222", name: "Beta", kind: "plugin" })],
      projects: [
        folder({
          directory: "/srv/site",
          name: "site",
          last_used: "2026-03-01T00:00:00Z",
          packages: [pin({ module: "github.com/acme/beta", commit: "2222222", kind: "plugin", name: "Beta" })],
        }),
      ],
      agentpacks: [pack("g1", "Gamma", [revision({ id: "3333333aaa", agentpack_id: "g1", created_at: "2026-02-01T00:00:00Z" })])],
    }),
    input(oracle, {
      projects: [folder({ last_used: "2026-05-01T00:00:00Z", mcp: [server({ name: "zeta", package: "@acme/zeta-mcp", version: "1.0.0" })] })],
    }),
  ])
}

function names(view: LibraryView) {
  return view.groups.flatMap((group) => group.items.map((item) => item.name))
}

describe("parseFilter and parseSort", () => {
  it("falls back to the defaults for unknown values", () => {
    expect(parseFilter("bogus")).toBe("all")
    expect(parseFilter("mcp")).toBe("mcp")
    expect(parseSort(undefined)).toBe("type")
    expect(parseSort("machine")).toBe("machine")
  })
})

describe("arrangeLibrary types", () => {
  it("builds all four types", () => {
    expect(fixture().map((item) => item.type).sort()).toEqual(["agentpack", "mcp", "plugin", "skill"])
  })

  it("filters to one type with a flat list and no header", () => {
    const view = arrangeLibrary(fixture(), { filter: "mcp", sort: "type", query: "" })
    expect(view.groups).toHaveLength(1)
    expect(view.groups[0]?.label).toBeUndefined()
    expect(names(view)).toEqual(["zeta"])
  })

  it("groups All rows under type headers with counts", () => {
    const view = arrangeLibrary(fixture(), { filter: "all", sort: "type", query: "" })
    expect(view.groups.map((group) => [group.label, group.items.length])).toEqual([
      ["Skills", 1],
      ["Plugins", 1],
      ["MCP servers", 1],
      ["Agentpacks", 1],
    ])
    expect(view.counts).toEqual({ all: 4, skill: 1, plugin: 1, mcp: 1, agentpack: 1 })
  })
})

describe("arrangeLibrary sorts", () => {
  it("type sort orders groups by type and rows by name inside each group", () => {
    const items = buildLibrary([
      input(oracle, {
        packages: [pkg({ id: "b", module: "github.com/acme/b", name: "b" }), pkg({ id: "a", module: "github.com/acme/a", name: "A" })],
      }),
    ])
    expect(names(arrangeLibrary(items, { filter: "all", sort: "type", query: "" }))).toEqual(["A", "b"])
  })

  it("name sort is one flat list", () => {
    const view = arrangeLibrary(fixture(), { filter: "all", sort: "name", query: "" })
    expect(view.groups).toHaveLength(1)
    expect(names(view)).toEqual(["alpha", "Beta", "Gamma", "zeta"])
  })

  it("recently updated sorts newest first", () => {
    expect(names(arrangeLibrary(fixture(), { filter: "all", sort: "updated", query: "" }))).toEqual(["zeta", "Beta", "Gamma", "alpha"])
  })

  it("machine sort orders by machine name, then by name", () => {
    expect(names(arrangeLibrary(fixture(), { filter: "all", sort: "machine", query: "" }))).toEqual(["Beta", "Gamma", "alpha", "zeta"])
  })
})

describe("arrangeLibrary search", () => {
  const search = (query: string) => names(arrangeLibrary(fixture(), { filter: "all", sort: "name", query }))

  it("matches name", () => {
    expect(search("alp")).toEqual(["alpha"])
  })

  it("matches module, lowercase, and the MCP npm package", () => {
    expect(search("ACME/BETA")).toEqual(["Beta"])
    expect(search("zeta-mcp")).toEqual(["zeta"])
  })

  it("matches type", () => {
    expect(search("agentpack")).toEqual(["Gamma"])
  })

  it("matches the commit by prefix only", () => {
    expect(search("2222")).toEqual(["Beta"])
    expect(search("222")).toEqual(["Beta"])
    expect(search("1111111aa")).toEqual([])
  })

  it("matches machine", () => {
    expect(search("mac-studio")).toEqual(["Beta", "Gamma"])
  })

  it("matches project name and folder", () => {
    expect(search("site")).toEqual(["Beta"])
    expect(search("/srv/app")).toEqual(["alpha", "zeta"])
  })

  it("counts reflect the search", () => {
    expect(arrangeLibrary(fixture(), { filter: "all", sort: "type", query: "zeta" }).counts).toEqual({ all: 1, skill: 0, plugin: 0, mcp: 1, agentpack: 0 })
  })
})
