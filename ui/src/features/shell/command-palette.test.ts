import { describe, expect, it } from "vitest"
import { rankEntries, type PaletteEntry } from "./command-palette"

const entry = (id: string, group: string, label: string, extra: Partial<PaletteEntry> = {}): PaletteEntry => ({ id, group, label, run: () => {}, ...extra })

describe("rankEntries", () => {
  const entries = [
    entry("w", "Needs you", "Bump crsqlite pin", { detail: "Claude · agentpack · Build server" }),
    entry("lib", "Go to", "Library"),
    entry("set", "Go to", "Settings"),
    entry("s1", "Sessions", "Refactor the library pane", { typedOnly: true }),
    entry("s2", "Sessions", "Release checks", { detail: "Codex · codemode", typedOnly: true }),
  ]
  const ids = (query: string) => rankEntries(entries, query).map((group) => [group.value, group.items.map((item) => item.id)])

  it("hides typed-only entries until something is typed and keeps group order", () => {
    expect(ids("")).toEqual([["Needs you", ["w"]], ["Go to", ["lib", "set"]]])
  })

  it("ranks label prefix over word start over detail-only matches", () => {
    expect(ids("lib")).toEqual([["Go to", ["lib"]], ["Sessions", ["s1"]]])
    expect(ids("codemode")).toEqual([["Sessions", ["s2"]]])
    expect(ids("build bump")).toEqual([["Needs you", ["w"]]])
    expect(ids("zzz")).toEqual([])
  })
})
