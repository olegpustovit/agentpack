import { readFileSync } from "node:fs"
import { dirname, join } from "node:path"
import { fileURLToPath } from "node:url"
import { describe, expect, it } from "vitest"
import type { LogEntry } from "./model"
import { reduceLog, reduceThread } from "./reducer"

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
    expect(state.items).toMatchObject([{ kind: "user_message_chunk", data: { text: { text: "Review these" }, images } }])
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
    expect(state.items).toMatchObject([
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

  it("keeps a question past turn_end and makes it inactive after agent_exit", () => {
    const entries: LogEntry[] = [
      { seq: 1, ts: 1, kind: "elicitation", payload: { elicitationId: "e1" } },
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

  it("releases a question when its turn is cancelled or the agent withdraws it", () => {
    const entries: LogEntry[] = [
      { seq: 1, ts: 1, kind: "elicitation", payload: { elicitationId: "e1" } },
      { seq: 2, ts: 2, kind: "elicitation", payload: { elicitationId: "e2", agentElicitationId: "oauth" } },
      { seq: 3, ts: 3, kind: "turn_end", payload: { stopReason: "cancelled" } },
    ]
    const ended = reduceLog(entries)
    expect(ended.items.map((item) => (item.data as { inactive?: boolean }).inactive)).toEqual([true, undefined])
    expect(ended.needs_you).toBe(true)
    const withdrawn = reduceLog([...entries, { seq: 4, ts: 4, kind: "elicitation_result", payload: { elicitationId: "e2", cancelled: true } }])
    expect(withdrawn.needs_you).toBe(false)
    expect(withdrawn.items[1]!.data).not.toHaveProperty("resolved")
  })

  it("clears working after turn_end", () => {
    const entries: LogEntry[] = [
      { seq: 1, ts: 1, kind: "prompt", payload: { text: "hi" } },
      { seq: 2, ts: 2, kind: "turn_end", payload: { status: "completed" } },
    ]
    expect(reduceLog(entries).working).toBe(false)
  })

  it("ends Claude's own follow-up cycle on its origin-stamped usage, but not inside a user turn", () => {
    const origin = { _meta: { "_claude/origin": { kind: "task-notification" } }, used: 10 }
    const cycle: LogEntry[] = [
      { seq: 1, ts: 1, kind: "prompt", payload: { text: { text: "go" } } },
      { seq: 2, ts: 2, kind: "turn_end", payload: {} },
      { seq: 3, ts: 3, kind: "agent_message_chunk", payload: { text: { text: "summary" } } },
    ]
    expect(reduceLog(cycle).working).toBe(true)
    expect(reduceLog([...cycle, { seq: 4, ts: 4, kind: "usage_update", payload: origin }]).working).toBe(false)
    expect(reduceLog([...cycle, { seq: 4, ts: 4, kind: "prompt", payload: {} }, { seq: 5, ts: 5, kind: "usage_update", payload: origin }]).working).toBe(true)
  })

  it("bounds Codex's unprompted goal work by its thread status, but not inside a user turn", () => {
    const thread = (seq: number, type: string): LogEntry => ({ seq, ts: seq, kind: "session_info_update", payload: { _meta: { codex: { threadStatus: { type } } } } })
    const ended: LogEntry[] = [{ seq: 1, ts: 1, kind: "prompt", payload: { text: { text: "/goal ship" } } }, { seq: 2, ts: 2, kind: "turn_end", payload: {} }]
    expect(reduceLog([...ended, thread(3, "active")]).working).toBe(true)
    expect(reduceLog([...ended, thread(3, "active"), thread(4, "idle")]).working).toBe(false)
    expect(reduceLog([ended[0]!, thread(2, "idle")]).working).toBe(true)
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

  it("gives items a stable seq as the log grows", () => {
    const log: LogEntry[] = [
      { seq: 5, ts: 50, kind: "tool_call", payload: { toolCallId: "t" } },
      { seq: 6, ts: 60, kind: "tool_call_update", payload: { toolCallId: "t", status: "completed" } },
      { seq: 7, ts: 70, kind: "agent_message_chunk", payload: { text: { text: "a" } } },
      { seq: 8, ts: 80, kind: "agent_message_chunk", payload: { text: { text: "b" } } },
    ]
    const short = reduceLog(log.slice(0, 3)).items
    const full = reduceLog(log).items
    expect(short.map((i) => i.seq)).toEqual([5, 7])
    expect(full.map((i) => i.seq)).toEqual([5, 7])
    expect(full[0]).toMatchObject({ ts: 50, endTs: 60 })
  })

  it("keeps a tool's title and kind through partial updates", () => {
    const { items } = reduceLog([
      { seq: 1, ts: 1, kind: "tool_call", payload: { toolCallId: "t", kind: "execute", title: "Terminal", status: "pending" } },
      { seq: 2, ts: 2, kind: "tool_call_update", payload: { toolCallId: "t", title: "go test ./..." } },
      { seq: 3, ts: 3, kind: "tool_call_update", payload: { toolCallId: "t", status: "completed", rawOutput: "ok" } },
    ])
    expect(items[0]!.data).toMatchObject({ kind: "execute", title: "go test ./...", status: "completed", rawOutput: "ok" })
  })

  it("retires a permission left behind by a failed turn", () => {
    const state = reduceLog([
      { seq: 1, ts: 1, kind: "permission_request", payload: { requestId: "p1", options: [] } },
      { seq: 2, ts: 2, kind: "turn_end", payload: { stopReason: "error" } },
    ])
    expect(state.needs_you).toBe(false)
    expect(state.items[0]!.data).toMatchObject({ inactive: true })
  })
})

function loadJSONL(name: string): LogEntry[] {
  const path = join(dirname(fileURLToPath(import.meta.url)), "..", "..", "..", "..", "testdata", "acp", name)
  return readFileSync(path, "utf8").split("\n").filter(Boolean).map((line) => JSON.parse(line) as LogEntry)
}

describe("reduceLog subagents", () => {
  it("routes child entries into their own threads and links the spawning tool", () => {
    const state = reduceLog(loadJSONL("subagents.jsonl"))
    const subagents = state.subagents ?? {}
    expect(Object.keys(subagents).sort()).toEqual(["sub-explore", "sub-review"])
    const explore = subagents["sub-explore"]!
    expect(explore).toMatchObject({ title: "Explore", toolCallId: "toolu_explore", cancellable: true, state: "idle", stopReason: "end_turn" })
    expect(explore.thread.items[0]?.kind).toBe("task")
    expect(explore.tools).toBeGreaterThan(0)
    // No child text leaks into the parent thread.
    const parentText = state.items.filter((item) => item.kind === "agent_message_chunk").map((item) => JSON.stringify(item.data)).join(" ")
    expect(parentText).not.toContain("Searching the worker routes")
    expect(state.needs_you).toBe(false)
  })

  it("a pending child permission makes the chat need you", () => {
    const entries = loadJSONL("subagents.jsonl")
    const cut = entries.findIndex((entry) => entry.kind === "permission_result")
    expect(reduceLog(entries.slice(0, cut)).needs_you).toBe(true)
  })

  it("groups Claude's flat parentToolUseId stamping under the Task tool", () => {
    const state = reduceLog([
      { seq: 1, ts: 1, kind: "tool_call", payload: { toolCallId: "t1", title: "Task", status: "in_progress" } },
      { seq: 2, ts: 2, kind: "tool_call", payload: { _subagent: "tool:t1", toolCallId: "c1", title: "grep" } },
      { seq: 3, ts: 3, kind: "agent_message_chunk", payload: { _subagent: "tool:t1", text: { text: "child" } } },
    ])
    expect(state.subagents?.["tool:t1"]).toMatchObject({ toolCallId: "t1", tools: 1 })
    expect(state.items.map((item) => item.kind)).toEqual(["tool", "subagent"])
  })

  it("does not show a subagent from a settings broadcast alone", () => {
    // Its work lies before the loaded tail; only a worker restart's config update is in it.
    const state = reduceLog([
      { seq: 6140, ts: 1, kind: "config_option_update", payload: { _subagent: "a1", configOptions: [] } },
      { seq: 6141, ts: 2, kind: "agent_message_chunk", payload: { text: { text: "parent" } } },
    ])
    expect(state.subagents).toBeUndefined()
    expect(state.items.map((item) => item.kind)).toEqual(["agent_message_chunk"])
  })
})

describe("reduceLog queue, goal and turns", () => {
  it("tracks the queue from queued and dequeued entries, edits replace", () => {
    const state = reduceLog([
      { seq: 1, ts: 1, kind: "queued_prompt", payload: { queueId: "q1", text: { text: "a" } } },
      { seq: 2, ts: 2, kind: "queued_prompt", payload: { queueId: "q2", text: { text: "b" } } },
      { seq: 3, ts: 3, kind: "queued_prompt", payload: { queueId: "q1", text: { text: "a2" } } },
      { seq: 4, ts: 4, kind: "dequeued_prompt", payload: { queueId: "q2", reason: "removed" } },
    ])
    expect(state.queue?.map((item) => [item.queueId, item.text?.text])).toEqual([["q1", "a2"]])
  })

  it("keeps the latest goal and records one thread line per state change", () => {
    const goal = { objective: "Ship", state: "active" as const, active_ms: 0 }
    const state = reduceLog([
      { seq: 1, ts: 1, kind: "goal_update", payload: goal },
      { seq: 2, ts: 2, kind: "goal_update", payload: { ...goal, turns: 1 } },
      { seq: 3, ts: 3, kind: "goal_update", payload: { ...goal, state: "met", turns: 2, active_ms: 5000 } },
    ])
    expect(state.goal).toMatchObject({ state: "met", turns: 2 })
    expect(state.items.filter((item) => item.kind === "goal")).toHaveLength(2)
    expect(reduceLog([{ seq: 1, ts: 1, kind: "goal_update", payload: { ...goal, state: "cleared" } }]).goal).toBeUndefined()
  })

  it("measures the last turn and a compaction's before and after", () => {
    const state = reduceLog([
      { seq: 1, ts: 1000, kind: "usage_update", payload: { used: 1000, size: 10_000, cost: { amount: 0.1, currency: "USD" } } },
      { seq: 2, ts: 2000, kind: "prompt", payload: { text: { text: "go" } } },
      { seq: 3, ts: 3000, kind: "usage_update", payload: { used: 9000, size: 10_000, cost: { amount: 0.25, currency: "USD" } } },
      { seq: 4, ts: 9000, kind: "turn_end", payload: { stopReason: "end_turn" } },
      { seq: 5, ts: 9100, kind: "compaction_update", payload: { compactionId: "c", status: "in_progress" } },
      { seq: 6, ts: 9200, kind: "compaction_update", payload: { compactionId: "c", status: "completed", summary: [] } },
      { seq: 7, ts: 9300, kind: "usage_update", payload: { used: 2000, size: 10_000 } },
    ])
    expect(state.last_turn?.ms).toBe(7000)
    expect(state.last_turn?.tokens).toBe(8000)
    expect(state.last_turn?.cost).toBeCloseTo(0.15)
    expect(state.items.find((item) => item.kind === "compaction")?.data).toMatchObject({ before: 9000, after: 2000 })
  })
})

describe("reduceThread", () => {
  it("applies streamed entries alone, matches a full replay, and keeps finished items identical", () => {
    const all = loadFixture()
    let shown: LogEntry[] = []
    let previous = reduceThread("fixture", shown)
    for (const entry of all) {
      shown = [...shown, entry]
      const next = reduceThread("fixture", shown)
      expect(next).toEqual(reduceLog(shown))
      // Items before the newest change are the same objects, so React skips them.
      const untouched = previous.items.findIndex((item, index) => next.items[index] !== item)
      for (let i = 0; i < (untouched < 0 ? previous.items.length : untouched); i++) expect(next.items[i]).toBe(previous.items[i])
      previous = next
    }
    // A refetch hands over new objects; the result is rebuilt, not appended twice.
    const refetched = all.map((entry) => ({ ...entry }))
    expect(reduceThread("fixture", refetched)).toEqual(reduceLog(all))
  })
})
