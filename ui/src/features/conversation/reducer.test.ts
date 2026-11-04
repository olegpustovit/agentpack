import { readFileSync } from "node:fs"
import { dirname, join } from "node:path"
import { fileURLToPath } from "node:url"
import { describe, expect, it } from "vitest"
import type { LogEntry } from "./model"
import { reduceLog } from "./reducer"

const fixture = join(dirname(fileURLToPath(import.meta.url)), "..", "..", "..", "..", "testdata", "acp", "full.jsonl")

function loadFixture(): LogEntry[] {
  const entries: LogEntry[] = []
  for (const line of readFileSync(fixture, "utf8").split("\n")) {
    if (!line) continue
    entries.push(JSON.parse(line) as LogEntry)
  }
  return entries
}

describe("reduceLog", () => {
  it("shows a prompt once even when the agent echoes it", () => {
    const state = reduceLog([
      { seq: 1, ts: 1, kind: "prompt", payload: { text: { text: "hi" } } },
      { seq: 2, ts: 2, kind: "user_message_chunk", payload: { text: { text: "hi" } } },
      { seq: 3, ts: 3, kind: "agent_message_chunk", payload: { text: { text: "ok" } } },
    ])
    expect(state.items.map((item) => item.kind)).toEqual(["user_message_chunk", "agent_message_chunk"])
  })
  it("folds the full fixture into a rich thread with usage, notices, compaction, and permission", () => {
    const state = reduceLog(loadFixture())
    expect(state.items.length).toBeGreaterThanOrEqual(8)
    expect(state.context_limit).toBe(200_000)
    expect(state.plan_kind).toBe("Publish the branch")
    expect(state.items.some((item) => item.kind === "permission")).toBe(true)
    expect(state.items.some((item) => item.kind === "elicitation")).toBe(true)
    const notice = state.items.find((item) => item.kind === "divider")
    expect(notice?.data).toMatchObject({ title: "Context compacted", description: "Older messages were summarized." })
    const compactions = state.items.filter((item) => item.kind === "compaction")
    expect(compactions).toHaveLength(1)
    expect(compactions[0]?.data).toMatchObject({
      compactionId: "cmp_001",
      status: "completed",
      summary: "Compaction summary retained.",
    })
    const editTool = state.items.find(
      (item) =>
        item.kind === "tool" &&
        Array.isArray((item.data as { content?: { type?: string }[] }).content) &&
        (item.data as { content?: { type?: string }[] }).content?.some((block) => block.type === "diff"),
    )
    expect(editTool).toBeTruthy()
  })

  it("normalizes raw ACP usage from the shared fixture without rewriting it", () => {
    const entry = loadFixture().find((item) => item.kind === "usage_update")!
    expect(entry.payload).toEqual({
      sessionUpdate: "usage_update",
      used: 120_000,
      size: 200_000,
      cost: { amount: 0.42, currency: "USD" },
    })
    const originalPayload = entry.payload
    const state = reduceLog([entry])
    expect(state).toMatchObject({ context_tokens: 120_000, context_limit: 200_000, cost_usd: 0.42 })
    expect(entry.payload).toBe(originalPayload)
    expect(reduceLog([{ seq: 2, ts: 2, kind: "usage_update", payload: { used: 1, size: 2 } }]).cost_usd).toBeUndefined()
    expect(reduceLog([{ seq: 3, ts: 3, kind: "usage_update", payload: { used: 1, size: 2, cost: { amount: 3, currency: "EUR" } } }]).cost_usd).toBeUndefined()
  })
  it("keeps compactions at their original position while appending and replacing summaries", () => {
    const entries: LogEntry[] = [
      { seq: 1, ts: 1, kind: "compaction_update", payload: { compactionId: "cmp_1", status: "in_progress" } },
      { seq: 2, ts: 2, kind: "agent_message_chunk", payload: { text: { text: "Between" } } },
      { seq: 3, ts: 3, kind: "compaction_summary_chunk", payload: { compactionId: "cmp_1", content: { type: "text", text: "first " } } },
      { seq: 4, ts: 4, kind: "compaction_summary_chunk", payload: { compactionId: "cmp_1", content: { type: "text", text: "summary" } } },
      { seq: 5, ts: 5, kind: "compaction_update", payload: { compactionId: "cmp_2", status: "in_progress" } },
      {
        seq: 6,
        ts: 6,
        kind: "compaction_update",
        payload: { compactionId: "cmp_1", status: "completed", summary: [{ type: "text", text: "replacement" }] },
      },
    ]
    const chunked = reduceLog(entries.slice(0, 4))
    expect(chunked.items[0]?.data).toMatchObject({ compactionId: "cmp_1", summary: "first summary" })

    const rawPatch = entries[5]!.payload
    const replaced = reduceLog(entries)
    expect(replaced.items.map((item) => item.kind)).toEqual(["compaction", "agent_message_chunk", "compaction"])
    expect(replaced.items[0]?.data).toMatchObject({ compactionId: "cmp_1", status: "completed", summary: "replacement" })
    expect(entries[5]!.payload).toBe(rawPatch)

    entries.push({
      seq: 7,
      ts: 7,
      kind: "compaction_update",
      payload: { compactionId: "cmp_1", status: "completed", summary: null },
    })
    expect(reduceLog(entries).items[0]?.data).toMatchObject({ compactionId: "cmp_1", summary: "" })
  })
  it("preserves logged multi-image references while merging user text chunks", () => {
    const images = [
      { blob: "sha256:one", name: "one.png", width: 10, height: 20, kind: "image" as const },
      { blob: "sha256:two", name: "two.webp", width: 30, height: 40, kind: "resource_link" as const },
    ]
    const state = reduceLog([
      { seq: 1, ts: 1, kind: "user_message_chunk", payload: { text: { text: "Review" }, images } },
      { seq: 2, ts: 2, kind: "user_message_chunk", payload: { text: { text: " these" } } },
    ])
    expect(state.items).toEqual([{ kind: "user_message_chunk", data: { text: { text: "Review these" }, images } }])
  })



  it("resolves only the matching request among overlapping permission and elicitation cards", () => {
    const entries: LogEntry[] = [
      { seq: 1, ts: 1, kind: "permission_request", payload: { requestId: "p1" } },
      { seq: 2, ts: 2, kind: "elicitation", payload: { elicitationId: "e1" } },
      { seq: 3, ts: 3, kind: "permission_request", payload: { requestId: "p2" } },
      { seq: 4, ts: 4, kind: "permission_result", payload: { requestId: "p1", optionId: "allow" } },
      { seq: 5, ts: 5, kind: "elicitation_result", payload: { elicitationId: "e1", value: "Direct" } },
    ]
    const state = reduceLog(entries)
    expect(state.needs_you).toBe(true)
    expect(state.items).toEqual([
      {
        kind: "permission",
        data: { requestId: "p1", resolved: true, result: { requestId: "p1", optionId: "allow" } },
      },
      {
        kind: "elicitation",
        data: { elicitationId: "e1", resolved: true, result: { elicitationId: "e1", value: "Direct" } },
      },
      { kind: "permission", data: { requestId: "p2" } },
    ])
  })

  it("preserves outstanding requests after turn_end and makes them inactive after agent_exit", () => {
    const entries: LogEntry[] = [
      { seq: 1, ts: 1, kind: "permission_request", payload: { requestId: "p1" } },
      { seq: 2, ts: 2, kind: "turn_end", payload: {} },
    ]
    const ended = reduceLog(entries)
    expect(ended.working).toBe(false)
    expect(ended.needs_you).toBe(true)
    const exited = reduceLog([...entries, { seq: 3, ts: 3, kind: "agent_exit", payload: {} }])
    expect(exited.needs_you).toBe(false)
    const inactiveItem = exited.items[0]!.data as { inactive?: boolean; result?: unknown }
    expect(inactiveItem.inactive).toBe(true)
    expect(inactiveItem).not.toHaveProperty("result")
  })

  it("clears working after turn_end", () => {
    const entries: LogEntry[] = [
      { seq: 1, ts: 1, kind: "prompt", payload: { text: "hi" } },
      { seq: 2, ts: 2, kind: "turn_end", payload: { status: "completed" } },
    ]
    expect(reduceLog(entries).working).toBe(false)
  })

  it("clears working after agent_exit", () => {
    const entries: LogEntry[] = [
      { seq: 1, ts: 1, kind: "prompt", payload: { text: "hi" } },
      { seq: 2, ts: 2, kind: "agent_exit", payload: {} },
    ]
    const state = reduceLog(entries)
    expect(state.working).toBe(false)
    expect(state.needs_you).toBe(false)
  })
})
