import { describe, expect, it } from "vitest"
import { safeReturnTo } from "./return-to"

describe("safeReturnTo", () => {
  it.each([
    "/",
    "/sessions/local/chat?q=limits#reply",
    "/library/package/github.com/acme/tools?type=skill",
    "/machines/atlas",
    "/usage",
    "/limits",
    "/settings/account",
    "/oauth/authorize?x=1",
  ])("keeps %s", (value) => expect(safeReturnTo(value)).toBe(value))

  it.each([
    undefined,
    "//evil.example/oauth/authorize",
    "https://evil.example/oauth/authorize",
    "/\\evil.example/oauth/authorize",
    `${location.origin}/sessions`,
    "/login?return_to=/sessions",
    "/api/sessions",
    "/mcp",
    "/oauth/token",
    "/oauth/authorize/another",
    "/sessions-archive",
    "/sessions/..%2Fapi/status",
    "/%2f%2fevil.example/sessions",
    "/\t/evil.example/sessions",
    "/\t/[invalid-host",
    "javascript:alert(1)",
    "/oauth/authorize/../../settings",
  ])("ignores %s", (value) => {
    expect(safeReturnTo(value)).toBeUndefined()
  })
})
