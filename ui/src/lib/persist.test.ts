import { describe, expect, it, vi } from "vitest"
import { forgetQueries, onSignOut } from "./persist"

describe("forgetQueries", () => {
  it("clears per-account memory and saved drafts", () => {
    const reset = vi.fn()
    onSignOut(reset)
    sessionStorage.setItem("agentpack:draft:local/s1", "half a prompt")
    sessionStorage.setItem("agentpack:draft:local/s1:attachments", "[]")
    sessionStorage.setItem("agentpack:reloaded", "1")
    forgetQueries()
    expect(reset).toHaveBeenCalledOnce()
    expect(sessionStorage.getItem("agentpack:draft:local/s1")).toBeNull()
    expect(sessionStorage.getItem("agentpack:draft:local/s1:attachments")).toBeNull()
    expect(sessionStorage.getItem("agentpack:reloaded")).toBe("1")
  })
})
