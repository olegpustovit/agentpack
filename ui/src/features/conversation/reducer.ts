import type { CompactionPayload, LogEntry, ThreadItem, ThreadState, UserMessagePayload } from "./model"

function toolID(payload: unknown): string {
  const body = payload as { toolCallId?: string }
  return body.toolCallId ?? ""
}

function mergeTextChunks(a: unknown, b: unknown): unknown {
  const left = a as UserMessagePayload
  const right = b as UserMessagePayload
  const merged: UserMessagePayload = {
    text: { text: (left.text?.text ?? "") + (right.text?.text ?? "") },
  }
  if (right.image) merged.image = right.image
  else if (left.image) merged.image = left.image
  if (right.resource_link) merged.resource_link = right.resource_link
  else if (left.resource_link) merged.resource_link = left.resource_link
  if (right.images) merged.images = right.images
  else if (left.images) merged.images = left.images
  return merged
}

function appendChunk(state: ThreadState, entry: LogEntry) {
  const items = state.items
  const last = items[items.length - 1]
  if (!last || last.kind !== entry.kind) {
    state.items = [...items, { kind: entry.kind, data: entry.payload }]
    return
  }
  const next = items.slice(0, -1)
  try {
    next.push({ kind: entry.kind, data: mergeTextChunks(last.data, entry.payload) })
    state.items = next
  } catch {
    state.items = [...items, { kind: entry.kind, data: entry.payload }]
  }
}
function hasOutstandingRequests(items: ThreadItem[]): boolean {
  return items.some((item) => {
    const data = item.data as { requestId?: string; elicitationId?: string; resolved?: boolean; inactive?: boolean } | undefined
    if (item.kind !== "permission" && item.kind !== "elicitation") return false
    if (data?.resolved || data?.inactive) return false
    return item.kind === "permission" ? !!data?.requestId : !!data?.elicitationId
  })
}

function resolveRequest(state: ThreadState, kind: "permission" | "elicitation", idKey: "requestId" | "elicitationId", payload: unknown) {
  const result = payload as Record<string, unknown>
  const id = result[idKey]
  if (typeof id !== "string") return
  const items = state.items.slice()
  for (let i = items.length - 1; i >= 0; i--) {
    const item = items[i]!
    const data = item.data as Record<string, unknown> | undefined
    if (item.kind === kind && data?.[idKey] === id && !data.resolved && !data.inactive) {
      items[i] = { ...item, data: { ...data, resolved: true, result: payload } }
      break
    }
  }
  state.items = items
  state.needs_you = hasOutstandingRequests(state.items)

}
type CompactionPatch = {
  status?: string
  summary?: string
  appendSummary?: string
  error?: string | undefined
}

function compactionText(content: unknown): string {
  const blocks = Array.isArray(content) ? content : [content]
  let text = ""
  for (const block of blocks) {
    if (!block || typeof block !== "object") continue
    const value = block as { type?: unknown; text?: unknown }
    if (value.type === "text" && typeof value.text === "string") text += value.text
  }
  return text
}

function updateCompaction(state: ThreadState, compactionId: string, patch: CompactionPatch) {
  const index = state.items.findIndex((item) => {
    const data = item.data
    return item.kind === "compaction" && data !== null && typeof data === "object" &&
      "compactionId" in data && data.compactionId === compactionId
  })
  const current = index >= 0 ? state.items[index]!.data as CompactionPayload : undefined
  if (!current && typeof patch.status !== "string") return
  const status = typeof patch.status === "string" ? patch.status : current?.status
  if (status === undefined) return
  const summary =
    patch.appendSummary !== undefined
      ? `${current?.summary ?? ""}${patch.appendSummary}`
      : patch.summary ?? current?.summary ?? ""
  const error = Object.prototype.hasOwnProperty.call(patch, "error") ? patch.error : current?.error
  const data = { compactionId, status, summary, ...(error === undefined ? {} : { error }) }
  if (index < 0) state.items = [...state.items, { kind: "compaction", data }]
  else {
    const items = state.items.slice()
    items[index] = { kind: "compaction", data }
    state.items = items
  }
}

export function reduceLog(entries: LogEntry[]): ThreadState {
  const state: ThreadState = { items: [], needs_you: false, working: false }
  const tools: Record<string, number> = {}
  let afterPrompt = false
  for (const entry of entries) {
    // Some agents (Antigravity) echo the prompt back as a user chunk; the prompt entry already shows it.
    const echo = afterPrompt && entry.kind === "user_message_chunk"
    afterPrompt = entry.kind === "prompt" || echo
    if (echo) continue
    switch (entry.kind) {
      case "agent_message_chunk":
      case "user_message_chunk":
      case "agent_thought_chunk":
        appendChunk(state, entry)
        if (entry.kind !== "user_message_chunk") state.working = !state.needs_you
        break
      case "tool_call": {
        const id = toolID(entry.payload)
        state.items = [...state.items, { kind: "tool", data: entry.payload }]
        if (id) tools[id] = state.items.length - 1
        if (!state.needs_you) state.working = true
        break
      }
      case "tool_call_update": {
        const id = toolID(entry.payload)
        const idx = tools[id]
        if (idx !== undefined) {
          const items = state.items.slice()
          const prev = items[idx]!
          items[idx] = { kind: prev.kind, data: entry.payload }
          state.items = items
        } else {
          state.items = [...state.items, { kind: "tool", data: entry.payload }]
        }
        break
      }
      case "plan": {
        state.items = [...state.items, { kind: "plan", data: entry.payload }]
        const entries = (entry.payload as { entries?: { status?: string; content?: string }[] }).entries ?? []
        const active =
          entries.find((item) => item.status === "in_progress") ??
          entries.find((item) => item.status === "pending") ??
          entries.at(-1)
        state.plan_kind = active?.content?.trim() || undefined
        break
      }
      case "permission_request":
        state.items = [...state.items, { kind: "permission", data: entry.payload }]
        state.needs_you = hasOutstandingRequests(state.items)
        break
      case "permission_result":
        resolveRequest(state, "permission", "requestId", entry.payload)
        break
      case "elicitation":
        state.items = [...state.items, { kind: "elicitation", data: entry.payload }]
        state.needs_you = hasOutstandingRequests(state.items)
        break
      case "elicitation_result":
        resolveRequest(state, "elicitation", "elicitationId", entry.payload)
        break
      case "notice":
        state.items = [...state.items, { kind: "divider", data: entry.payload }]
        break
      case "compaction_update": {
        const update = entry.payload as {
          compactionId?: unknown
          status?: unknown
          summary?: unknown
          error?: unknown
        }
        if (typeof update.compactionId !== "string" || !update.compactionId) break
        const patch: CompactionPatch = {}
        if (typeof update.status === "string") patch.status = update.status
        if (Object.prototype.hasOwnProperty.call(update, "summary")) patch.summary = compactionText(update.summary)
        if (Object.prototype.hasOwnProperty.call(update, "error")) {
          patch.error = typeof update.error === "string" ? update.error : undefined
        }
        updateCompaction(state, update.compactionId, patch)
        break
      }
      case "compaction_summary_chunk": {
        const chunk = entry.payload as { compactionId?: unknown; content?: unknown }
        if (typeof chunk.compactionId !== "string" || !chunk.compactionId) break
        const summary = compactionText(chunk.content)
        if (summary) updateCompaction(state, chunk.compactionId, { appendSummary: summary })
        break
      }
      case "usage_update": {
        const usage = entry.payload as {
          used?: number
          size?: number
          cost?: { amount?: number; currency?: string }
        }
        state.context_tokens = usage.used
        state.context_limit = usage.size
        state.cost_usd = usage.cost?.currency === "USD" ? usage.cost?.amount : undefined
        break
      }
      case "turn_end":
        state.working = false
        break
      case "agent_exit":
        state.working = false
        state.items = state.items.map((item) => {
          const data = item.data as { requestId?: string; elicitationId?: string; resolved?: boolean; inactive?: boolean } | undefined
          const hasID = item.kind === "permission" ? !!data?.requestId : !!data?.elicitationId
          return (item.kind === "permission" || item.kind === "elicitation") && hasID && !data?.resolved && !data?.inactive
            ? { ...item, data: { ...data, inactive: true } }
            : item
        })
        state.needs_you = hasOutstandingRequests(state.items)
        break
      case "prompt":
        state.working = true
        state.items = [...state.items, { kind: "user_message_chunk", data: entry.payload }]
        break
    }
  }
  return state
}
