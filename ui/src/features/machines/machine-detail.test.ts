import { describe, expect, it } from "vitest"
import type { SessionInfo } from "@/lib/api-types"
import { formatBytes, runningText } from "./machine-detail"

const session = (patch: Partial<SessionInfo>): SessionInfo =>
  ({ id: "s", directory: "/p", harness: "claude", mode: "", state: { running: true }, ...patch }) as SessionInfo

describe("runningText", () => {
  it("leads with sessions that need you and ignores stopped or archived ones", () => {
    expect(runningText([])).toBeUndefined()
    expect(runningText([session({ state: { running: false } as SessionInfo["state"] }), session({ archived_at: "x" })])).toBeUndefined()
    expect(runningText([session({ conversation_state: "working" }), session({})])).toBe("1 working")
    expect(runningText([session({ conversation_state: "idle" }), session({})])).toBe("2 open")
    expect(runningText([session({ conversation_state: "needs_you" }), session({})])).toBe("1 needs you")
  })
})

describe("formatBytes", () => {
  it("rounds large sizes and keeps one decimal below 10 GB", () => {
    expect(formatBytes(31.4 * 1024 ** 3)).toBe("31 GB")
    expect(formatBytes(2.25 * 1024 ** 3)).toBe("2.3 GB")
  })
})
