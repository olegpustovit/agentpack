import { describe, expect, it } from "vitest"
import { safeReturnTo } from "./return-to"

describe("safeReturnTo", () => {
  it("keeps the OAuth consent path with its query", () => {
    expect(safeReturnTo("/oauth/authorize?x=1")).toBe(`${location.origin}/oauth/authorize?x=1`)
  })

  it.each([
    undefined,
    "//evil.example/oauth/authorize",
    "https://evil.example/oauth/authorize",
    "/\\evil.example/oauth/authorize",
    "/settings",
    "javascript:alert(1)",
    "/oauth/authorize/../../settings",
  ])("ignores %s", (value) => {
    expect(safeReturnTo(value)).toBeUndefined()
  })
})
