import { describe, expect, it } from "vitest"
import { unavailableMessage } from "./unavailable"

const interactive = { state: { interactive: true } }

describe("unavailableMessage", () => {
  it("is undefined for an interactive local session", () => {
    expect(unavailableMessage(interactive, false)).toBeUndefined()
    expect(unavailableMessage(undefined, false)).toBeUndefined()
  })

  it("uses the native stdio copy when the session is not interactive", () => {
    expect(unavailableMessage({ state: { interactive: false } }, false)).toBe("Native stdio · browser input unavailable")
  })

  it("marks a stale session offline even with no info", () => {
    expect(unavailableMessage(undefined, true)).toBe("Offline · showing last-known session metadata")
  })
})
