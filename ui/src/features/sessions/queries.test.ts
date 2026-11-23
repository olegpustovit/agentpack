import { describe, expect, it } from "vitest"
import type { SessionInfo } from "@/lib/api-types"
import { mergeProvisional } from "./queries"

const row = (id: string, extra: Partial<SessionInfo> = {}) => ({ id, ...extra }) as SessionInfo

describe("mergeProvisional", () => {
  it("keeps pending starts until listed, and drops an unreplicated accepted start after two minutes", () => {
    const now = Date.parse("2026-01-01T00:10:00Z")
    const current = [
      row("claim", { provisionalStart: "claim", started_at: "2026-01-01T00:00:00Z" }),
      row("fresh", { provisionalStart: "replica", started_at: "2026-01-01T00:09:00Z" }),
      row("stale", { provisionalStart: "replica", started_at: "2026-01-01T00:07:00Z" }),
      row("listed", { provisionalStart: "replica", started_at: "2026-01-01T00:09:30Z" }),
      row("old"),
    ]
    const merged = mergeProvisional(current, [row("listed")], now)
    expect(merged.map((item) => item.id)).toEqual(["claim", "fresh", "listed"])
    expect(merged.at(-1)?.provisionalStart).toBeUndefined()
  })
})
