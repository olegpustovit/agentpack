import { describe, expect, it } from "vitest"
import { stoppedStatus } from "./composer"

describe("composer status line", () => {
  it("says a stopped chat waits for sign-ins while the gateway will resume it", () => {
    expect(stoppedStatus(false, true, false)).toBe("Waiting for sign-ins to sync")
    expect(stoppedStatus(false, undefined, false)).toBe("Stopped") // a peer without the field
    expect(stoppedStatus(true, true, false)).toBe("Starting")
  })
})
