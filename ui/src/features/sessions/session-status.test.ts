import { act, createElement } from "react"
import { createRoot } from "react-dom/client"
import { afterEach, describe, expect, it, vi } from "vitest"
import type { ListedSession } from "./group"
import { HOLD_MS, isUnread, nextNeedsYou, openWaits, sessionGlyphState, sessionStatusWord, useSteadyGlyph, waitingDetail, waitingWord, type GlyphState } from "./session-status"

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
    expect(sessionStatusWord(session, false)).toBeUndefined()
  })

  it("shows a stopped chat as stopped even with a stale needs_you", () => {
    const session = { ...base, conversation_state: "needs_you", state: { ...base.state, running: false } }
    expect(sessionGlyphState(session)).toBe("stopped")
    expect(sessionStatusWord(session, false)).toBe("Stopped")
  })

  it("shows a chat that is still starting as starting, until its row ends", () => {
    const session = { ...base, conversation_state: "starting", state: { ...base.state, running: false } }
    expect(sessionGlyphState(session)).toBe("starting")
    expect(sessionStatusWord(session, false)).toBeUndefined()
    expect(sessionGlyphState({ ...session, ended_at: "2026-01-01T00:00:00Z" })).toBe("stopped")
  })

  it("shows an idle chat with open background work as waiting, until its items expire", () => {
    const now = Date.parse("2026-01-01T00:00:00Z")
    vi.setSystemTime(now)
    const session = { ...base, transport: "acp", conversation_state: "idle", waiting: [{ kind: "shell" as const }, { kind: "monitor" as const }] }
    expect(sessionGlyphState(session)).toBe("waiting")
    expect(waitingWord(openWaits(session, now), now)).toBe("2 running")
    const wake = { ...session, waiting: [{ kind: "wake" as const, until: now + 6 * 60_000 }] }
    expect(waitingWord(openWaits(wake, now), now)).toBe("Wakes in 4 m")
    const asked = { ...session, waiting: [{ kind: "reply" as const, peer: "s2", until: now + 60_000 }] }
    expect(waitingWord(openWaits(asked, now), now)).toBe("Awaits reply")
    expect(waitingDetail(openWaits(asked, now), (id) => (id === "s2" ? "migrate archive" : undefined), now)).toBe("a reply from migrate archive")
    expect(sessionGlyphState({ ...asked, waiting: [{ kind: "reply" as const, until: now - 1 }] })).toBe("idle")
    vi.useRealTimers()
  })

  it("shows a terminal without reported state as open, never working or unread", () => {
    const terminal = { ...base, last_used: "2026-01-01T00:00:10Z" }
    expect(sessionGlyphState(terminal)).toBe("idle")
    expect(isUnread(terminal, undefined)).toBe(false)
    expect(sessionGlyphState({ ...terminal, conversation_state: "working" })).toBe("running")
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
 expect(sessionStatusWord({...pending,archived_at:"2026-11-15T00:00:00Z"},false)).toBe("Cold storage")
 expect(sessionGlyphState({...pending,archived_at:"2026-11-15T00:00:00Z"})).toBe("stopped")
})

describe("isUnread", () => {
  const idle = { ...base, conversation_state: "idle", last_used: "2026-01-01T00:00:10Z" }
  const at = (iso: string) => Date.parse(iso)
  it("is unread when idle activity is newer than the last open, or never opened", () => {
    expect(isUnread(idle, undefined)).toBe(true)
    expect(isUnread(idle, at("2026-01-01T00:00:05Z"))).toBe(true)
    expect(isUnread(idle, at("2026-01-01T00:00:10Z"))).toBe(false)
  })
  it("ignores sessions that are not idle or have no activity time", () => {
    expect(isUnread({ ...idle, conversation_state: "working" }, undefined)).toBe(false)
    expect(isUnread({ ...idle, last_used: undefined }, undefined)).toBe(false)
  })
  it("uses conversation activity, not the launch time", () => {
    const active = { ...idle, last_event_at: "2026-01-01T00:01:00Z" }
    expect(isUnread(active, at("2026-01-01T00:00:30Z"))).toBe(true)
    expect(isUnread(active, at("2026-01-01T00:01:00Z"))).toBe(false)
  })
})

it("nextNeedsYou cycles through waiting sessions after the open one", () => {
  const waiting = (id: string) => ({ ...base, id, conversation_state: "needs_you" })
  const list = [waiting("a"), { ...base, id: "b", conversation_state: "working" }, waiting("c")]
  expect(nextNeedsYou(list, undefined)?.id).toBe("a")
  expect(nextNeedsYou(list, "local:b")?.id).toBe("a")
  expect(nextNeedsYou(list, "local:a")?.id).toBe("c")
  expect(nextNeedsYou(list, "local:c")?.id).toBe("a")
  expect(nextNeedsYou([list[1]!], undefined)).toBeUndefined()
})

describe("useSteadyGlyph", () => {
  afterEach(() => vi.useRealTimers())
  it("shows a finished turn as idle at once and a stop only once it lasts", () => {
    vi.useFakeTimers()
    ;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
    const shown: GlyphState[] = []
    const Row = ({ state }: { state: GlyphState }) => (shown.push(useSteadyGlyph(state, true)), null)
    const root = createRoot(document.createElement("div"))
    const show = (state: GlyphState) => act(() => root.render(createElement(Row, { state })))
    show("running")
    show("idle")
    expect(shown.at(-1)).toBe("idle")
    act(() => vi.advanceTimersByTime(0))
    show("stopped")
    expect(shown.at(-1)).toBe("idle")
    act(() => vi.advanceTimersByTime(HOLD_MS))
    expect(shown.at(-1)).toBe("stopped")
    act(() => root.unmount())
  })
})
