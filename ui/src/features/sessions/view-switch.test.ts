import { describe, expect, it } from "vitest"
import type { SessionInfo } from "@/lib/api-types"
import { viewSwitchNeedsResume } from "./view-switch"

const base: SessionInfo = {
  id: "s1",
  directory: "/work/x",
  harness: "claude",
  mode: "default",
  state: { interactive: true, running: true, restarting: false, rows: 24, columns: 80, exit_code: 0 },
}

describe("viewSwitchNeedsResume", () => {
  it("blocks terminal sessions while running, but only blocks ACP sessions during turns", () => {
    expect(viewSwitchNeedsResume(base)).toBe(true)
    expect(viewSwitchNeedsResume({ ...base, transport: "terminal", state: { ...base.state, running: false } })).toBe(false)
    expect(viewSwitchNeedsResume({ ...base, transport: "acp", conversation_state: "idle" })).toBe(false)
    expect(viewSwitchNeedsResume({ ...base, transport: "acp", conversation_state: "working" })).toBe(true)
    expect(viewSwitchNeedsResume({ ...base, transport: "acp", conversation_state: "needs_you" })).toBe(true)
    expect(viewSwitchNeedsResume({ ...base, transport: "acp", conversation_state: "starting" })).toBe(true)
  })
})
