import { describe, expect, it } from "vitest"
import { applyHint, filterAtHints, filterSlashHints, moveHint } from "./composer-hints"

describe("composer hints", () => {
  it("filters slash commands by prefix", () => {
    const hits = filterSlashHints("/mod")
    expect(hits.some((item) => item.value === "/model")).toBe(true)
    expect(hits.some((item) => item.value === "/mode")).toBe(true)
  })

  it("filters @ file hints", () => {
    const hits = filterAtHints("see @term", ["internal/network/terminal_client.go"])
    expect(hits[0]?.value).toBe("@internal/network/terminal_client.go")
  })

  it("replaces the @ fragment when a hint is chosen", () => {
    expect(applyHint("look @ter", { value: "@internal/a.go", description: "" })).toBe("look @internal/a.go ")
  })

  it("triggers @ only at the start or after whitespace", () => {
    const paths = ["a/term.go"]
    expect(filterAtHints("me@term", paths)).toEqual([])
    expect(filterAtHints("@term", paths)).toHaveLength(1)
    expect(filterAtHints("see\n@term", paths)).toHaveLength(1)
  })

  it("wraps the active hint index", () => {
    expect(moveHint(0, -1, 3)).toBe(2)
    expect(moveHint(2, 1, 3)).toBe(0)
    expect(moveHint(0, 1, 0)).toBe(0)
  })
})
