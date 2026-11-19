import { describe, expect, it } from "vitest"
import { connectionStatus, readConnections, serverLogin } from "./connections-model"

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

describe("serverLogin", () => {
  it("matches a server to the login with its URL, never an env credential", () => {
    const row = (match: string, kind: string, status = "connected") => ({ id: match, match, kind, label: "", status, names: [], expiresAt: "", plugins: [] })
    const credentials = [row("https://a/mcp", "oauth"), row("https://b/mcp", "oauth", "needs_login"), row("", "env")]
    expect(serverLogin("https://a/mcp", credentials)).toBe("connected")
    expect(serverLogin("https://b/mcp", credentials)).toBe("needs_login")
    expect(serverLogin("https://c/mcp", credentials)).toBe("none")
    expect(serverLogin("", credentials)).toBe("none")
  })
})
