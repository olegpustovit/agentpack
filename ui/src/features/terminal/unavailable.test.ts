import { describe, expect, it } from "vitest"
import { unavailableTerminal } from "./unavailable"

const interactive = { harness: "claude", state: { interactive: true } }

describe("unavailableTerminal", () => {
  it("is undefined for an interactive local session", () => {
    expect(unavailableTerminal(interactive, false)).toBeUndefined()
    expect(unavailableTerminal(undefined, false)).toBeUndefined()
  })

  it("uses the legacy copy for agy desktop sessions", () => {
    expect(unavailableTerminal({ harness: "agy", state: { interactive: true } }, false)).toEqual({
      message: "Desktop launcher presence · start and interact locally. Detached desktop windows are not tracked.",
      status: "Desktop session · local interaction",
    })
  })

  it("uses the native stdio copy when the session is not interactive", () => {
    expect(unavailableTerminal({ harness: "codex", state: { interactive: false } }, false)).toEqual({
      message: "Native stdio · browser input unavailable",
      status: "Native stdio · input unavailable",
    })
  })

  it("marks a stale session offline even with no info", () => {
    expect(unavailableTerminal(undefined, true)).toEqual({
      message: "Offline · showing last-known session metadata",
      status: "Offline · last-known session",
    })
  })
})
