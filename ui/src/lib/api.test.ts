import { afterEach, describe, expect, it, vi } from "vitest"
import { api } from "./api"

const page = '<!DOCTYPE html> <!--[if lt IE 7]> <html class="no-js ie6 oldie" lang="en-US"> <![endif]-->'

function respond(body: string, status: number) {
  vi.stubGlobal("fetch", async () => new Response(body, { status }))
}

describe("api errors", () => {
  afterEach(() => vi.unstubAllGlobals())

  it("never shows a proxy's HTML error page", async () => {
    vi.stubGlobal("location", { origin: "https://gateway.test" })
    respond(page, 502)
    await expect(api("/api/agent-sessions", { machine: "node-123" })).rejects.toThrow(
      "The gateway's reverse proxy returned an HTML error during GET /api/agent-sessions on node node-123 (HTTP 502). The response had no Agentpack error detail; check the gateway or reverse-proxy logs, then retry.",
    )
    respond(page, 524)
    await expect(api("/api/projects")).rejects.toThrow(
      "The gateway timed out during GET /api/projects (HTTP 524). Check the gateway and machine, then retry.",
    )
  })

  it("keeps the gateway's own messages", async () => {
    vi.stubGlobal("location", { origin: "https://gateway.test" })
    respond("remote node connection failed\n", 502)
    await expect(api("/api/projects")).rejects.toThrow("remote node connection failed")
    respond(JSON.stringify({ error: "not found" }), 404)
    await expect(api("/api/projects")).rejects.toThrow("not found")
  })
})
