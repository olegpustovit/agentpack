import { describe, expect, it } from "vitest"
import { connectionStatus, readConnections } from "./connections-model"

describe("readConnections", () => {
  it("reads the gateway list, an older shape without optional fields, and rejects a bad row", () => {
    const read = readConnections({
      callback_url: "https://gw/mcp-oauth/callback",
      credentials: [
        { id: "a", match: "https://x/mcp", kind: "oauth", label: "X", status: "needs_login", plugins: ["github.com/o/p"], names: null },
        { id: "b", kind: "env", label: "gh", status: "connected", names: ["GITHUB_TOKEN"] },
      ],
    })
    expect(read.credentials[0]).toMatchObject({ match: "https://x/mcp", plugins: ["github.com/o/p"], names: [] })
    expect(read.credentials[1]).toMatchObject({ match: "", names: ["GITHUB_TOKEN"], plugins: [] })
    expect(connectionStatus(read.credentials[0]!).problem).toBe(true)
    expect(connectionStatus(read.credentials[1]!).text).toContain("Environment variables")
    expect(() => readConnections({ credentials: [{ id: 1 }] })).toThrow()
  })
})
