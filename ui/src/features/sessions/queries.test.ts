import { afterEach, describe, expect, it, vi } from "vitest"
import { LOCAL } from "@/lib/api"
import type { SessionInfo } from "@/lib/api-types"
import { mergeProvisional, projectFilesQuery } from "./queries"

afterEach(() => vi.unstubAllGlobals())

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

describe("projectFilesQuery", () => {
  it("loads project-relative tracked files for local and peer nodes", async () => {
    const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify({ files: ["README.md", "src/app.ts"], truncated: true }), {
      headers: { "Content-Type": "application/json" },
    }))
    vi.stubGlobal("fetch", fetch)
    const query = projectFilesQuery("birch", "/srv/repo")
    expect(query.queryKey).toEqual(["m", "birch", "project-files", "/srv/repo"])
    const result = await query.queryFn!({ signal: new AbortController().signal } as never)
    expect(result).toEqual({ files: ["README.md", "src/app.ts"], truncated: true })
    const url = new URL(fetch.mock.calls[0]![0] as URL)
    expect(url.pathname).toBe("/api/projects/files")
    expect(url.searchParams.get("node")).toBe("birch")
    expect(url.searchParams.get("directory")).toBe("/srv/repo")
  })

  it("treats missing routes on older peers as an empty file list", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("404 page not found\n", { status: 404 })))
    const result = await projectFilesQuery(LOCAL, "/work/repo").queryFn!({ signal: new AbortController().signal } as never)
    expect(result).toEqual({ files: [], truncated: false })
  })
})
