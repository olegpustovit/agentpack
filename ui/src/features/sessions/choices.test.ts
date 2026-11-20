import { afterEach, describe, expect, it } from "vitest"
import { clearFirstMessage, firstMessage, loadAgentChoices, loadStartChoices, markNewChat, rememberAgentChoice, saveStartChoices, takeNewChat } from "./choices"

describe("remembered new-session choices", () => {
  afterEach(() => {
    localStorage.clear()
    sessionStorage.clear()
  })

  it("keeps agent, mode and view per folder and ignores junk", () => {
    saveStartChoices("/a", { harness: "codex", mode: "review", view: "terminal" })
    expect(loadStartChoices("/a")).toEqual({ harness: "codex", mode: "review", view: "terminal" })
    expect(loadStartChoices("/b")).toEqual({ harness: undefined, mode: undefined, view: undefined })
    localStorage.setItem("agentpack.newSession./c", JSON.stringify({ harness: 3, view: "tv" }))
    expect(loadStartChoices("/c")).toEqual({ harness: undefined, mode: undefined, view: undefined })
  })

  it("keeps model and effort per folder and agent", () => {
    rememberAgentChoice("/a", "claude", "model", "opus")
    rememberAgentChoice("/a", "claude", "effort", "high")
    expect(loadAgentChoices("/a", "claude")).toEqual({ model: "opus", effort: "high" })
    expect(loadAgentChoices("/a", "codex")).toEqual({})
  })

  it("applies to a new chat once", () => {
    markNewChat("s1")
    expect(takeNewChat("s1")).toBe(true)
    expect(takeNewChat("s1")).toBe(false)
  })

  it("keeps a first message until it is sent, and still applies choices after", () => {
    markNewChat("s2", "fix the tests")
    expect(firstMessage("s2")).toBe("fix the tests")
    expect(firstMessage("s2")).toBe("fix the tests")
    clearFirstMessage("s2")
    expect(firstMessage("s2")).toBe("")
    expect(takeNewChat("s2")).toBe(true)
    markNewChat("s3")
    expect(firstMessage("s3")).toBe("")
  })
})
