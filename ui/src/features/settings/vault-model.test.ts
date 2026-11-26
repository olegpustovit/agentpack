import { describe, expect, test } from "vitest"
import { readConnections } from "./connections-model"
import { AGENTS_MODULE, changesAgent, readSharedSignIns, refusedForAgents, secretReach, signInStatus, vaultBadge } from "./vault-model"

const now = Date.parse("2026-11-26T12:00:00Z")
const at = (hours: number) => new Date(now + hours * 3_600_000).toISOString()
const name = (node: string) => ({ mini: "olegs-mac-mini" })[node] ?? node
const rows = readSharedSignIns({ agents: [
  { harness: "claude", state: "shared", publisher_node: "mini", renewer_node: "mini", expires_at: at(6) },
  { harness: "codex", state: "shared", publisher_node: "mini" },
  { harness: "cursor", state: "none" },
  { harness: "grok", state: "stopped" },
  { harness: "opencode", state: "expired", expires_at: at(-48) },
  { harness: "agy", state: "expired", expires_at: at(3), refresh_error_at: at(-2) },
] })
const status = (harness: string) => signInStatus(harness, rows.find((row) => row.harness === harness), name, now)

describe("vault", () => {
  test("every row state reads as the plan words it", () => {
    expect(status("claude")).toBe("Shared · renewed by olegs-mac-mini · expires in 6 h")
    expect(status("codex")).toBe("Shared from olegs-mac-mini")
    expect(status("cursor")).toBe("Not shared yet")
    expect(status("grok")).toBe("Sharing stopped")
    expect(status("opencode")).toBe("Expired 2 d ago · Sign in again")
    expect(status("agy")).toBe("Expired 2 h ago · Sign in again")
    expect(status("omp")).toBe("Kept in each machine’s own store")
  })

  // The gateway omits expires_at for Codex, whose sign-in only says when it was last renewed.
  test("a row without an expiry still reads sensibly", () => {
    const read = (row: object) => signInStatus("codex", readSharedSignIns({ agents: [{ harness: "codex", publisher_node: "mini", ...row }] })[0], name, now)
    expect(read({ state: "shared", renewer_node: "mini" })).toBe("Shared · renewed by olegs-mac-mini")
    expect(read({ state: "expired", renewer_node: "mini", refresh_error_at: at(-2) })).toBe("Expired 2 h ago · Sign in again")
  })

  test("the badge counts expired sign-ins and MCP logins, not none or stopped", () => {
    const connections = readConnections({ credentials: [
      { id: "a", kind: "oauth", label: "a", status: "needs_login" },
      { id: "b", kind: "oauth", label: "b", status: "connected" },
    ] }).credentials
    expect(vaultBadge(rows, connections)).toBe(3)
    expect(vaultBadge(rows.filter((row) => row.state === "none" || row.state === "stopped"), [])).toBe(0)
  })

  test("a secret row says who receives it", () => {
    expect(secretReach([])).toBe("MCP servers")
    expect(secretReach(["github.com/acme/plugin"])).toBe("MCP servers")
    expect(secretReach([AGENTS_MODULE])).toBe("MCP servers · agents")
  })

  test("variables that change what runs are refused or warned about", () => {
    for (const name of ["PATH", "HOME", "SHELL", "LD_PRELOAD", "DYLD_INSERT_LIBRARIES", "AGENTPACK_HOME"]) expect(refusedForAgents(name), name).toBe(true)
    for (const name of ["PATHS", "MY_HOME", "SHELL_OPTS", "OLD_LD_PRELOAD", "DATABASE_URL", "NODE_OPTIONS"]) expect(refusedForAgents(name), name).toBe(false)
    for (const name of ["ANTHROPIC_API_KEY", "OPENAI_API_KEY", "NODE_OPTIONS"]) expect(changesAgent(name), name).toBe(true)
    expect(changesAgent("GITHUB_TOKEN")).toBe(false)
  })
})
