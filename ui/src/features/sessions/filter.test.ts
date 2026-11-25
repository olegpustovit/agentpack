import { describe, expect, it } from "vitest"
import { filterSearch, filterTokens, matchesFilter, readFilter } from "./filter"
import type { ListedSession } from "./group"

function session(id: string, harness: string, machine: string, conversation_state: string, running = true): ListedSession {
  return {
    id, harness, directory: `/work/${id}`, mode: "default", transport: "acp", conversation_state, machine, machineName: machine, offline: false,
    state: { interactive: true, running, restarting: false, rows: 30, columns: 100, exit_code: 0 },
  }
}

const rows = [
  session("a", "claude", "laptop", "needs_you"),
  session("b", "codex", "vm", "working"),
  session("c", "claude", "vm", "idle"),
  session("d", "claude", "laptop", "idle", false),
]

describe("session filter", () => {
  it("round-trips through the URL and drops unknown or empty values", () => {
    const filter = readFilter({ q: "a", agent: "claude,codex", status: "needs,bogus", host: "" })
    expect(filter).toEqual({ q: "a", agents: ["claude", "codex"], statuses: ["needs"], host: undefined })
    expect(filterSearch({ agents: [], statuses: ["idle", "stopped"] })).toEqual({ agent: undefined, status: "idle,stopped" })
  })

  it("dimensions combine with AND, values within one with OR", () => {
    const pick = (search: Record<string, string>) => rows.filter((row) => matchesFilter(row, readFilter(search))).map((row) => row.id)
    expect(pick({})).toEqual(["a", "b", "c", "d"])
    expect(pick({ agent: "claude" })).toEqual(["a", "c", "d"])
    expect(pick({ status: "needs,stopped" })).toEqual(["a", "d"])
    expect(pick({ agent: "claude", host: "vm" })).toEqual(["c"])
  })

  it("one token per active dimension, folding extra values", () => {
    const tokens = filterTokens(readFilter({ q: " x ", agent: "claude,codex", host: "vm" }), (id) => id.toUpperCase())
    expect(tokens.map((token) => `${token.label} ${token.value}`)).toEqual(["Contains “x”", "Agent Claude +1", "Machine VM"])
  })
})
