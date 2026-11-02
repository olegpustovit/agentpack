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
    await expect(api("/api/projects")).rejects.toThrow("The machine could not be reached (HTTP 502). Try again.")
    respond(page, 524)
    await expect(api("/api/projects")).rejects.toThrow("The machine took too long to answer. Try again.")
  })

  it("keeps the gateway's own messages", async () => {
    vi.stubGlobal("location", { origin: "https://gateway.test" })
    respond("remote node connection failed\n", 502)
    await expect(api("/api/projects")).rejects.toThrow("remote node connection failed")
    respond(JSON.stringify({ error: "not found" }), 404)
    await expect(api("/api/projects")).rejects.toThrow("not found")
  })
})
