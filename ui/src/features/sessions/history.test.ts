import { describe, expect, it } from "vitest"
import { historySection } from "./history"
import { pastedServer, serverFrom, sourceSpec } from "./project-page"

describe("historySection", () => {
  const now = new Date(2026, 10, 27, 15, 0).getTime() // Friday 27 November 2026
  const at = (month: number, day: number, year = 2026) => new Date(year, month, day, 9, 0).getTime()
  it("names recent days, then months", () => {
    expect(historySection(at(10, 27), now)).toBe("Today")
    expect(historySection(at(10, 26), now)).toBe("Yesterday")
    expect(historySection(at(10, 23), now)).toBe("Monday")
    expect(historySection(at(10, 2), now)).toBe("Earlier this month")
    expect(historySection(at(9, 6), now)).toBe("October")
    expect(historySection(at(11, 20, 2025), now)).toBe("December 2025")
  })
})

describe("sourceSpec", () => {
  it("accepts owner/repo, module paths and URLs", () => {
    expect(sourceSpec("acme/skills")).toBe("github.com/acme/skills")
    expect(sourceSpec("https://github.com/acme/skills.git")).toBe("github.com/acme/skills")
    expect(sourceSpec("github.com/acme/skills/react@v1.2")).toBe("github.com/acme/skills/react@v1.2")
  })
  it("rejects plain words and non-GitHub URLs", () => {
    expect(sourceSpec("https://mcp.sentry.dev/mcp")).toBeUndefined()
    expect(sourceSpec("mcp.sentry.dev/mcp")).toBeUndefined()
    expect(sourceSpec("frontend")).toBeUndefined()
    expect(sourceSpec("a b/c")).toBeUndefined()
  })
})

describe("serverFrom", () => {
  it("reads a URL as HTTP, or SSE by its path, named after the host", () => {
    expect(serverFrom("https://mcp.sentry.dev/mcp", "", [])).toEqual({ name: "sentry", url: "https://mcp.sentry.dev/mcp", type: "http" })
    expect(serverFrom("https://mcp.example.com/sse", "", [])?.type).toBe("sse")
  })
  it("reads a command and its arguments, named after the package", () => {
    expect(serverFrom("npx -y @modelcontextprotocol/server-github", "", [])).toEqual({ name: "github", command: "npx", args: ["-y", "@modelcontextprotocol/server-github"] })
    expect(serverFrom("npx @playwright/mcp@latest", "", [])?.name).toBe("playwright")
    expect(serverFrom("uvx mcp-server-fetch", "", [])?.name).toBe("fetch")
    expect(serverFrom(`node "my server.js" --port 3000`, "", [])?.args).toEqual(["my server.js", "--port", "3000"])
  })
  it("keeps a given name and never repeats one in use", () => {
    expect(serverFrom("https://mcp.linear.app/mcp", "", ["linear"])?.name).toBe("linear-2")
    expect(serverFrom("https://mcp.linear.app/mcp", "tracker", ["linear"])?.name).toBe("tracker")
    expect(serverFrom("  ", "", [])).toBeUndefined()
  })
})

describe("pastedServer", () => {
  it("takes a server URL or a package-manager command, never a GitHub source or a search", () => {
    expect(pastedServer("https://mcp.sentry.dev/mcp", [])?.name).toBe("sentry")
    expect(pastedServer("uvx mcp-server-fetch", [])?.name).toBe("fetch")
    expect(pastedServer("https://github.com/acme/skills", [])).toBeUndefined()
    expect(pastedServer("linear issues", [])).toBeUndefined()
  })
})
