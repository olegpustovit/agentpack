import { describe, expect, it } from "vitest"
import { compareAgentpackVersions } from "./queries"

describe("compareAgentpackVersions", () => {
  it("orders release versions and treats invalid build labels as unknown", () => {
    expect(compareAgentpackVersions("0.13.1", "v0.13.2")).toBeLessThan(0)
    expect(compareAgentpackVersions("0.14.0", "0.13.9")).toBeGreaterThan(0)
    expect(compareAgentpackVersions("dev", "0.13.2")).toBe(0)
  })
  it("orders dev builds and ignores build metadata", () => {
    expect(compareAgentpackVersions("0.14.1-dev.88+abc", "0.14.1-dev.90+def")).toBeLessThan(0)
    expect(compareAgentpackVersions("0.14.1-dev.9", "0.14.1-dev.10")).toBeLessThan(0)
    expect(compareAgentpackVersions("0.14.1-dev.88+abc", "0.14.1-dev.88+def")).toBe(0)
    expect(compareAgentpackVersions("0.14.1-dev.88", "0.14.1")).toBeLessThan(0)
  })
})
