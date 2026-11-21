import { afterEach, describe, expect, it } from "vitest"
import { loadStartChoices, saveStartChoices } from "./choices"

describe("remembered new-session choices", () => {
  afterEach(() => {
    localStorage.clear()
  })

  it("keeps agent, mode and view per folder and ignores junk", () => {
    saveStartChoices("/a", { harness: "codex", mode: "review", view: "terminal" })
    expect(loadStartChoices("/a")).toEqual({ harness: "codex", mode: "review", view: "terminal" })
    expect(loadStartChoices("/b")).toEqual({ harness: undefined, mode: undefined, view: undefined })
    localStorage.setItem("agentpack.newSession./c", JSON.stringify({ harness: 3, view: "tv" }))
    expect(loadStartChoices("/c")).toEqual({ harness: undefined, mode: undefined, view: undefined })
  })
})
