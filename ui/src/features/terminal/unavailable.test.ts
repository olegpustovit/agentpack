import { describe, expect, it } from "vitest"
import { unavailableMessage } from "./unavailable"

const interactive = { harness: "claude", state: { interactive: true } }

describe("unavailableMessage", () => {
  it("is undefined for an interactive local session", () => {
    expect(unavailableMessage(interactive, false)).toBeUndefined()
    expect(unavailableMessage(undefined, false)).toBeUndefined()
  })

  it("uses the legacy copy for agy desktop sessions", () => {
    expect(unavailableMessage({ harness: "agy", state: { interactive: true } }, false)).toBe(
      "Desktop launcher presence · start and interact locally. Detached desktop windows are not tracked.",
    )
  })

  it("uses the native stdio copy when the session is not interactive", () => {
    expect(unavailableMessage({ harness: "codex", state: { interactive: false } }, false)).toBe("Native stdio · browser input unavailable")
  })

  it("marks a stale session offline even with no info", () => {
    expect(unavailableMessage(undefined, true)).toBe("Offline · showing last-known session metadata")
  })
})
