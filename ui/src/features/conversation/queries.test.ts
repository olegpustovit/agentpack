import { afterEach, describe, expect, it, vi } from "vitest"
import { ApiError, LOCAL } from "@/lib/api"
import { archiveUnavailableMessage, conversationEventsQuery, conversationErrorMessage, conversationNeedsUpdate } from "./queries"

afterEach(() => vi.unstubAllGlobals())

describe("conversation peer compatibility", () => {
  it("names the archive main when archived history is unavailable", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("archive unavailable\n", {
      status: 503, headers: { "X-Agentpack-Archive-Main": "Sleeping computer" },
    })))
    const error = await conversationEventsQuery(LOCAL, "session-old").queryFn().catch((error: unknown) => error)
    expect(archiveUnavailableMessage(error)).toBe("Archived on Sleeping computer, unavailable right now")
    expect(conversationNeedsUpdate(error)).toBe(false)
    expect(archiveUnavailableMessage(new ApiError("worker offline", 503))).toBeUndefined()
  })
  it("maps missing conversation endpoints to Needs update", () => {
    const missing = new ApiError("missing", 404)
    expect(conversationNeedsUpdate(missing)).toBe(true)
    expect(conversationErrorMessage("remote", "show conversations", missing)).toBe("Peer needs an update to show conversations.")
    expect(conversationNeedsUpdate(new ApiError("failed", 500), new Error("network"))).toBe(false)
    expect(conversationNeedsUpdate(undefined)).toBe(false)
    expect(conversationErrorMessage(LOCAL, "send a message", new ApiError("worker failed", 500))).toBe("Couldn't send a message: worker failed")
  })
})
