import { describe, expect, it } from "vitest"
import { applyHint, filterAtHints, filterSlashHints } from "./composer-hints"

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
})
