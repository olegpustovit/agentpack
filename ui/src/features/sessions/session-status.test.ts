import { describe, expect, it } from "vitest"
import type { ListedSession } from "./group"
import { sessionGlyphState, sessionStatusWord } from "./session-status"

const base: ListedSession = {
  id: "s1",
  machine: "local",
  machineName: "Local",
  directory: "/p",
  harness: "claude",
  mode: "default",
  state: { running: true, restarting: false, interactive: true, rows: 24, columns: 80, exit_code: -1 },
  offline: false,
}

describe("session status", () => {
  it("shows idle glyph and label for live acp sessions", () => {
    const session = { ...base, conversation_state: "idle" }
    expect(sessionGlyphState(session)).toBe("idle")
    expect(sessionStatusWord(session, false)).toBe("Idle")
  })

  it("prefers needs you over idle", () => {
    const session = { ...base, conversation_state: "needs_you" }
    expect(sessionGlyphState(session)).toBe("needsYou")
    expect(sessionStatusWord(session, false)).toBe("Needs you")
  })
})

it("archived and offline state take precedence over stale live requests",()=>{
 const pending={...base,conversation_state:"needs_you"}
 expect(sessionStatusWord({...pending,offline:true},false)).toBe("Offline")
 expect(sessionGlyphState({...pending,offline:true})).toBe("offline")
 expect(sessionStatusWord({...pending,archived_at:"2026-11-15T00:00:00Z"},false)).toBe("Archived")
 expect(sessionGlyphState({...pending,archived_at:"2026-11-15T00:00:00Z"})).toBe("stopped")
})
