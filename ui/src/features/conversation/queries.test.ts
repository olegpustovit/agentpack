import { describe, expect, it } from "vitest"
import { ApiError, LOCAL } from "@/lib/api"
import { conversationErrorMessage, conversationNeedsUpdate } from "./queries"

describe("conversation peer compatibility", () => {
  it("maps missing conversation endpoints to Needs update", () => {
    const missing = new ApiError("missing", 404)
    expect(conversationNeedsUpdate(missing)).toBe(true)
    expect(conversationErrorMessage("remote", "show conversations", missing)).toBe("Peer needs an update to show conversations.")
    expect(conversationNeedsUpdate(new ApiError("failed", 500), new Error("network"))).toBe(false)
    expect(conversationNeedsUpdate(undefined)).toBe(false)
    expect(conversationErrorMessage(LOCAL, "send a message", new ApiError("worker failed", 500))).toBe("Couldn't send a message: worker failed")
  })
})
