import { describe, expect, it } from "vitest"
import { ApiError } from "@/lib/api"
import { type ListedSession } from "./group"
import { canStart, mergeProjects, modeChoices, modeEffect, placeLabel, reusesOperationId, sessionRules, startSessionBody } from "./rules"

function session(overrides: Partial<ListedSession> & { interactive?: boolean; running?: boolean } = {}): ListedSession {
  const { interactive = true, running = true, ...rest } = overrides
  return {
    id: "s",
    directory: "/work/agentpack",
    harness: "claude",
    mode: "default",
    state: { interactive, running, restarting: false, rows: 30, columns: 100, exit_code: 0 },
    machine: "local",
    machineName: "host",
    offline: false,
    ...rest,
  }
}

describe("sessionRules", () => {
  it("allows offline rename but refuses live actions", () => {
    const rules = sessionRules(session({ offline: true }))
    expect(rules.rename).toBe(true)
    expect(rules.stop || rules.restart || rules.reconnect || rules.install || rules.save).toBe(false)
  })

  it("archived rows remain read-only even on a reachable machine", () => {
    const rules=sessionRules(session({archived_at:"2026-11-15T00:00:00Z"}))
    expect(Object.values(rules).every((allowed)=>!allowed)).toBe(true)
  })

  it("non-interactive sessions cannot restart or reconnect", () => {
    const rules = sessionRules(session({ interactive: false }))
    expect(rules.restart).toBe(false)
    expect(rules.reconnect).toBe(false)
    expect(rules.stop).toBe(true)
  })

  it("a stopped chat can resume; a running one or a terminal cannot", () => {
    const ended_at = "2026-11-21T10:00:00Z"
    expect(sessionRules(session({ transport: "acp", interactive: false, running: false, ended_at })).resume).toBe(true)
    expect(sessionRules(session({ transport: "acp", interactive: false, running: true })).resume).toBe(false)
    expect(sessionRules(session({ running: false, ended_at })).resume).toBe(false)
  })

  it("stop needs a running session", () => {
    expect(sessionRules(session({ running: false })).stop).toBe(false)
  })
})

describe("startSessionBody", () => {
  it("maps chat to acp transport and terminal to terminal transport", () => {
    const base = { directory: "/work/x", harness: "claude", mode: "default" }
    expect(startSessionBody({ ...base, view: "chat" })).toMatchObject({ transport: "acp" })
    expect(startSessionBody({ ...base, view: "terminal" })).toMatchObject({ transport: "terminal" })
  })

  it("asks for a worktree only when one is chosen", () => {
    const base = { directory: "/work/x", harness: "claude", mode: "default", view: "chat" as const }
    expect(startSessionBody(base)).not.toHaveProperty("worktree")
    expect(startSessionBody({ ...base, worktree: "new", worktreeName: "Fix it", carry: true })).toMatchObject({ worktree: "new", worktree_name: "Fix it", carry: true })
    expect(startSessionBody({ ...base, worktree: "/h/worktrees/0123456789abcdef/x", carry: false })).not.toHaveProperty("carry")
  })
})

describe("reusesOperationId", () => {
  it("drops the id when the gateway rejected the request", () => {
    expect(reusesOperationId(new ApiError("restart was rejected", 409))).toBe(false)
    expect(reusesOperationId(new ApiError("bad id", 400))).toBe(false)
  })

  it("keeps the id on a server error or a transport failure, so the retry is the same intent", () => {
    expect(reusesOperationId(new ApiError("outcome unavailable", 502))).toBe(true)
    expect(reusesOperationId(new TypeError("Failed to fetch"))).toBe(true)
  })
})

describe("new session modes", () => {
  const launches = [{ harness: "claude", mode: "", last_used: "" }, { harness: "codex", mode: "retired", last_used: "" }]
  it("lists defined modes, then launched ones, naming the CLI's empty mode default", () => {
    expect(modeChoices({ modes: [{ name: "default", off_packages: [], partial_packages: [], off_mcp: [] }, { name: "design", off_packages: [], partial_packages: [], off_mcp: [] }], launches })).toEqual(["default", "design", "retired"])
    expect(modeChoices({ modes: [], launches: [] })).toEqual(["default"])
  })
  it("reports what a mode turns off and nothing for an unknown mode", () => {
    const project = { modes: [{ name: "design", off_packages: ["github.com/a/b"], partial_packages: [], off_mcp: ["playwright"] }] }
    expect(modeEffect(project, "design").offMCP.has("playwright")).toBe(true)
    expect(modeEffect(project, "other").offPackages.size).toBe(0)
  })
})

describe("new session projects", () => {
  const row = (directory: string, last_used: string, available = true) =>
    ({ directory, name: "", folder_id: "", available, unavailable_reason: available ? "" : "Folder no longer exists", last_used, launches: [], packages: [], mcp: [], modes: [], error: "" })
  const machines = [{ id: "local", name: "gigacomp", online: true }, { id: "mac", name: "macbook", online: true }, { id: "pi", name: "lab-pi", online: false }]
  const merged = mergeProjects(machines, {
    local: [row("/home/o/agentpack/", "2026-01-02T00:00:00Z"), row("/home/o/old/agentpack", "2026-01-05T00:00:00Z", false), row("/home/o/site", "2026-01-01T00:00:00Z")],
    mac: [row("/Users/o/agentpack", "2026-01-03T00:00:00Z")],
    pi: [row("/srv/agentpack", "2026-01-09T00:00:00Z")],
  })

  it("merges folders by name across machines, newest project first", () => {
    expect(merged.map((project) => project.name)).toEqual(["agentpack", "site"])
    expect(merged[0]!.places.map((place) => place.machineName)).toEqual(["macbook", "gigacomp", "lab-pi", "gigacomp"])
  })

  it("puts places that can start first and explains the rest", () => {
    const places = merged[0]!.places
    expect(places.map(canStart)).toEqual([true, true, false, false])
    expect(places.map((place) => placeLabel(place, places))).toEqual(["macbook", "gigacomp · o", "lab-pi · offline", "gigacomp · old · Folder no longer exists"])
  })
})
