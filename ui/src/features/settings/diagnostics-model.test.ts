import { describe, expect, it } from "vitest"
import { issueStatus, resolutionError } from "./diagnostics-model"

describe("diagnostic issue state", () => {
  it("shows regression only while an issue is open", () => {
    expect(issueStatus({ status: "open", regressed: true })).toBe("Regressed")
    expect(issueStatus({ status: "open", regressed: false })).toBe("Open")
    expect(issueStatus({ status: "resolved", regressed: true })).toBe("Resolved")
    expect(issueStatus({ status: "ignored", regressed: false })).toBe("Ignored")
  })

  it("requires a fix version and permits the development builds accepted by the recorder", () => {
    expect(resolutionError(" \n ")).toBe("Enter the version containing the fix.")
    expect(resolutionError("v0.14.2")).toBe("")
    expect(resolutionError("development")).toBe("")
  })
})
