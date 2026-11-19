import { afterEach, describe, expect, it, vi } from "vitest"
import { ApiError, LOCAL } from "@/lib/api"
import { archiveUnavailableMessage, conversationEventsQuery, conversationErrorMessage, conversationNeedsUpdate, joinHistory, olderConversationPage } from "./queries"

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

describe("earlier history", () => {
  const entry = (seq: number) => ({ seq, ts: seq, kind: "agent_message_chunk", payload: {} })
  it("reads the truncated flag and asks for the page before a sequence", async () => {
    const fetch = vi.fn().mockResolvedValue(new Response(`${JSON.stringify(entry(3))}\n${JSON.stringify(entry(4))}\n`, { headers: { "X-Agentpack-Truncated": "true" } }))
    vi.stubGlobal("fetch", fetch)
    const page = await olderConversationPage("peer-1", "s1", 5)
    expect(page).toEqual({ entries: [entry(3), entry(4)], truncated: true })
    const url = new URL(fetch.mock.calls[0]![0] as URL)
    expect([url.searchParams.get("before"), url.searchParams.get("node")]).toEqual(["5", "peer-1"])
  })
  it("treats a peer that answers with its tail as Needs update", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(`${JSON.stringify(entry(9))}\n`)))
    const error = await olderConversationPage("peer-1", "s1", 5).catch((error: unknown) => error)
    expect(conversationNeedsUpdate(error)).toBe(true)
  })
  it("keeps older pages below the tail and drops overlap", () => {
    expect(joinHistory([entry(1), entry(2), entry(3)], [entry(3), entry(4)]).map((item) => item.seq)).toEqual([1, 2, 3, 4])
    expect(joinHistory([], [entry(4)]).map((item) => item.seq)).toEqual([4])
  })
})
