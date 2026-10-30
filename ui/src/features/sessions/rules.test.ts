import { describe, expect, it } from "vitest"
import { ApiError } from "@/lib/api"
import { type ListedSession } from "./group"
import { reusesOperationId, sessionRules } from "./rules"

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
  it("disables every action on an offline row", () => {
    const rules = sessionRules(session({ offline: true }))
    expect(Object.values(rules).every((allowed) => !allowed)).toBe(true)
  })

  it("agy has no install and no terminal, but can still be renamed", () => {
    const rules = sessionRules(session({ harness: "agy" }))
    expect(rules.install).toBe(false)
    expect(rules.reconnect).toBe(false)
    expect(rules.rename).toBe(true)
  })

  it("non-interactive sessions cannot restart or reconnect", () => {
    const rules = sessionRules(session({ interactive: false }))
    expect(rules.restart).toBe(false)
    expect(rules.reconnect).toBe(false)
    expect(rules.stop).toBe(true)
  })

  it("stop needs a running session", () => {
    expect(sessionRules(session({ running: false })).stop).toBe(false)
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
