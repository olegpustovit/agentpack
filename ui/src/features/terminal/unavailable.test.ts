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

  it("keeps ACP sessions in Chat even when they have no terminal input", () => {
    expect(unavailableMessage({ transport: "acp", state: { interactive: false } }, false)).toBeUndefined()
    expect(unavailableMessage({ transport: "acp", state: { interactive: false } }, true)).toBe("Offline · showing last-known session metadata")
  })

  it("marks a stale session offline even with no info", () => {
    expect(unavailableMessage(undefined, true)).toBe("Offline · showing last-known session metadata")
  })
})
