import { afterEach, describe, expect, it, vi } from "vitest"
import { api, ApiError, errorText, request, responseError, routeMissing } from "./api"

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
      "The gateway didn't answer. It may be restarting; try again in a moment.",
    )
    respond(page, 524)
    await expect(api("/api/projects")).rejects.toThrow(
      "The machine took too long to answer. Try again in a moment.",
    )
  })

  it("keeps the gateway's own messages", async () => {
    vi.stubGlobal("location", { origin: "https://gateway.test" })
    respond("remote node connection failed\n", 502)
    await expect(api("/api/projects")).rejects.toThrow("remote node connection failed")
    respond(JSON.stringify({ error: "not found" }), 404)
    await expect(api("/api/projects")).rejects.toThrow("not found")
  })

  it("reads only a router's 404 as a missing route", () => {
    expect(routeMissing(new ApiError("not found", 404))).toBe(true)
    expect(routeMissing(new ApiError("404 page not found", 404))).toBe(true)
    expect(routeMissing(new ApiError("session unavailable", 404))).toBe(false)
    expect(routeMissing(new ApiError("not found", 500))).toBe(false)
  })

  it("replaces Go and network errors with plain words", async () => {
    vi.stubGlobal("location", { origin: "https://gateway.test" })
    respond('Post "http://127.0.0.1:4242/prompt": dial tcp 127.0.0.1:4242: connect: connection refused\n', 502)
    await expect(api("/api/projects")).rejects.toThrow("The machine didn't answer. It may be offline or restarting.")
    respond("context canceled", 500)
    await expect(api("/api/projects")).rejects.toThrow("Something went wrong on the machine. Try again.")
  })
})

describe("responseError", () => {
  it("replaces a proxy's HTML error page with a sentence", () => {
    const error = responseError(new Response(null, { status: 502 }), "<!DOCTYPE html><html><body>Cloudflare</body></html>")
    expect(error.message).not.toContain("<")
    expect(error.status).toBe(502)
  })
})

describe("one fetch core", () => {
  afterEach(() => vi.unstubAllGlobals())

  it("routes to the node, sends an upload with its type and keeps the failing response's headers", async () => {
    vi.stubGlobal("location", { origin: "https://gateway.test" })
    const sent: { url: string; init: RequestInit }[] = []
    vi.stubGlobal("fetch", async (url: URL, init: RequestInit) => {
      sent.push({ url: String(url), init })
      return new Response("archive unavailable", { status: 503, headers: { "X-Agentpack-Archive-Main": "main-1" } })
    })
    const file = new Blob(["png"], { type: "image/png" })
    const error = await request("/api/sessions/s1/conversation/blobs", { machine: "peer-1", upload: file }).catch((caught: unknown) => caught)
    expect(sent[0]!.url).toBe("https://gateway.test/api/sessions/s1/conversation/blobs?node=peer-1")
    expect(sent[0]!.init.method).toBe("POST")
    expect(sent[0]!.init.body).toBe(file)
    expect(new Headers(sent[0]!.init.headers).get("Content-Type")).toBe("image/png")
    expect(error).toBeInstanceOf(ApiError)
    expect((error as ApiError).headers?.get("X-Agentpack-Archive-Main")).toBe("main-1")
  })

  it("says Needs update for a peer without the route, and the message otherwise", () => {
    expect(errorText(new ApiError("404 page not found", 404))).toBe("Needs update")
    expect(errorText(new ApiError("session unavailable", 404))).toBe("session unavailable")
    expect(errorText("boom", "Couldn't commit.")).toBe("Couldn't commit.")
  })
})
