import { describe, expect, it } from "vitest"
import { compareAgentpackVersions, machinesToUpdate } from "./queries"

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

it("selects only versions older than the newest machine, including offline peers", () => {
  const machines = [
    { id: "old", name: "Old", online: false, version: "0.14.0" },
    { id: "unknown", name: "Unknown", online: true, version: "dev" },
    { id: "dev9", name: "Dev nine", online: true, version: "0.14.1-dev.9" },
    { id: "dev10", name: "Dev ten", online: true, version: "0.14.1-dev.10+hash" },
    { id: "same", name: "Same", online: true, version: "0.14.1-dev.10" },
  ]
  expect(machinesToUpdate(machines).map((machine) => machine.id)).toEqual(["old", "dev9"])
})
