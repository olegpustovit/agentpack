import type { GoalState, SessionInfo } from "@/lib/api-types"
import { harnessName } from "@/lib/format"
import type { CompactionPayload, LogEntry, QueuedPrompt, Subagent, ThreadItem, ThreadState, ToolPayload, UserAttachmentPayload, UserMessagePayload } from "./model"
import { onSignOut } from "@/lib/persist"

// Entries from a subagent carry its session id (or "tool:<id>" for agents that only tag the parent tool call).
function subagentOf(payload: unknown): string | undefined {
  const id = (payload as { _subagent?: unknown } | null)?._subagent
  return typeof id === "string" && id ? id : undefined
}

type SubagentUpdate = {
  sessionId?: string
  title?: string | null
  description?: string | null
  capabilities?: { cancel?: object | null } | null
  state?: { state?: string; stopReason?: string | null } | null
}

// The child a parent tool call spawned: an RFD session link in its content.
function linkedSession(payload: unknown): string | undefined {
  const content = (payload as { content?: { type?: string; sessionId?: string }[] } | null)?.content
  return Array.isArray(content) ? content.find((block) => block?.type === "session" && block.sessionId)?.sessionId : undefined
}

function chunkText(data: unknown): string {
  return (data as UserMessagePayload).text?.text ?? ""
}

function toolID(payload: unknown): string {
  const body = payload as { toolCallId?: string }
  return body.toolCallId ?? ""
}

function expandToolDiffs(tool: ToolPayload): ToolPayload {
  const text = (tool.rawInput as { content?: unknown } | undefined)?.content
  if (typeof text !== "string" || !Array.isArray(tool.content) || !tool.content.some((block) => block?.newTextFromInput)) return tool
  return {
    ...tool,
    content: tool.content.map((block) => block.type === "diff" && block.newTextFromInput
      ? { ...block, newTextFromInput: undefined, newText: text }
      : block),
  }
}

/** A chunk's images and links: an `images` list, or the single `image` and `resource_link` (as internal/acp/log.go merges them). */
export function chunkAttachments(payload: UserMessagePayload): UserAttachmentPayload[] {
  return payload.images ?? [...(payload.image ? [payload.image] : []), ...(payload.resource_link ? [payload.resource_link] : [])]
}

function mergeTextChunks(a: unknown, b: unknown): unknown {
  const left = a as UserMessagePayload
  const right = b as UserMessagePayload
  const merged: UserMessagePayload = {
    text: { text: (left.text?.text ?? "") + (right.text?.text ?? "") },
  }
  const images = [...chunkAttachments(left), ...chunkAttachments(right)]
  if (images.length) merged.images = images
  return merged
}

function appendChunk(state: ThreadState, entry: LogEntry) {
  const items = state.items
  const last = items[items.length - 1]
  if (!last || last.kind !== entry.kind) {
    items.push({ kind: entry.kind, data: entry.payload })
    return
  }
  try {
    items[items.length - 1] = { ...last, data: mergeTextChunks(last.data, entry.payload) }
  } catch {
    items.push({ kind: entry.kind, data: entry.payload })
  }
}
/** A permission or question the agent is still waiting on. */
export function isOpenRequest(item: ThreadItem): boolean {
  const data = item.data as { requestId?: string; elicitationId?: string; resolved?: boolean; inactive?: boolean } | undefined
  if (item.kind !== "permission" && item.kind !== "elicitation") return false
  if (data?.resolved || data?.inactive) return false
  return item.kind === "permission" ? !!data?.requestId : !!data?.elicitationId
}

function hasOutstandingRequests(items: ThreadItem[]): boolean {
  return items.some(isOpenRequest)
}

function resolveRequest(state: ThreadState, kind: "permission" | "elicitation", idKey: "requestId" | "elicitationId", payload: unknown) {
  const result = payload as Record<string, unknown>
  const id = result[idKey]
  if (typeof id !== "string") return
  const items = state.items
  for (let i = items.length - 1; i >= 0; i--) {
    const item = items[i]!
    const data = item.data as Record<string, unknown> | undefined
    if (item.kind === kind && data?.[idKey] === id && !data.resolved && !data.inactive) {
      items[i] = { ...item, data: result.cancelled === true ? { ...data, inactive: true } : { ...data, resolved: true, result: payload } }
      break
    }
  }
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
  // A summary chunk can arrive before any status: the compaction is then still running.
  const status = patch.status ?? current?.status ?? "in_progress"
  const summary =
    patch.appendSummary !== undefined
      ? `${current?.summary ?? ""}${patch.appendSummary}`
      : patch.summary ?? current?.summary ?? ""
  const error = Object.prototype.hasOwnProperty.call(patch, "error") ? patch.error : current?.error
  const { error: _previous, ...kept } = current ?? ({} as Partial<CompactionPayload>)
  const data = { ...kept, compactionId, status, summary, ...(error === undefined ? {} : { error }) }
  if (index < 0) state.items.push({ kind: "compaction", data })
  else state.items[index] = { ...state.items[index]!, data }
}

// ThreadReducer folds log entries one at a time. Items are updated in place and replaced, never mutated, so a
// snapshot's copy of the list stays valid while later entries arrive: each new entry costs O(1) plus one copy.
const EMPTY_THREAD: ThreadState = { items: [], needs_you: false, working: false }

class ThreadReducer {
  private state: ThreadState = { items: [], needs_you: false, working: false }
  private tools: Record<string, number> = {}
  private children = new Map<string, ThreadReducer>()
  private meta = new Map<string, Omit<Subagent, "thread" | "tools">>()
  private queue = new Map<string, QueuedPrompt>()
  private turn: { ts: number; tokens?: number; cost?: number } | undefined
  private compacting: string | undefined
  private goal: GoalState | undefined
  private turnTask: string | undefined
  private afterPrompt = false
  // The last snapshot while no entry has arrived since; a parent re-snapshots only the children an entry touched.
  private last: ThreadState | undefined
  private summaries = new Map<string, { source: ThreadState; brief?: string; thread: ThreadState; tools: number; lastTool?: string }>()

  constructor(private readonly nested = false) {}

  // The first sight of a subagent places its card in the thread; a linked tool call takes the card's place.
  private child(id: string, entry: LogEntry) {
    let found = this.meta.get(id)
    if (!found) {
      found = { id, state: "running", cancellable: false, startedTs: entry.ts }
      if (id.startsWith("tool:")) found.toolCallId = id.slice(5)
      this.meta.set(id, found)
      this.state.items.push({ kind: "subagent", data: { id }, seq: entry.seq, ts: entry.ts })
    }
    return found
  }

  apply(entry: LogEntry) {
    this.last = undefined
    const { state, tools, meta, queue, nested } = this
    // Answers are logged by the gateway without a child stamp; each child resolves its own request by id.
    if (!nested && (entry.kind === "permission_result" || entry.kind === "elicitation_result")) for (const reducer of this.children.values()) reducer.apply(entry)
    const sub = nested ? undefined : subagentOf(entry.payload)
    // A settings broadcast is not activity: on its own it would show a finished subagent whose
    // work lies before the loaded tail as an empty card.
    if (sub && entry.kind === "config_option_update" && !this.meta.has(sub)) return
    if (sub) {
      let reducer = this.children.get(sub)
      if (!reducer) this.children.set(sub, (reducer = new ThreadReducer(true)))
      reducer.apply(entry)
      this.child(sub, entry)
      return
    }
    // Some agents (Antigravity) echo the prompt back as a user chunk; the prompt entry already shows it.
    const echo: boolean = this.afterPrompt && entry.kind === "user_message_chunk"
    this.afterPrompt = entry.kind === "prompt" || echo
    if (echo) return
    switch (entry.kind) {
      case "agent_message_chunk":
      case "user_message_chunk":
      case "agent_thought_chunk":
        appendChunk(state, entry)
        if (entry.kind !== "user_message_chunk" && !state.needs_you) state.working = true
        break
      case "tool_call": {
        const id = toolID(entry.payload)
        state.items.push({ kind: "tool", data: expandToolDiffs(entry.payload as ToolPayload) })
        if (id) tools[id] = state.items.length - 1
        if (!state.needs_you) state.working = true
        break
      }
      case "tool_call_update": {
        const id = toolID(entry.payload)
        const idx = tools[id]
        if (idx !== undefined) {
          const prev = state.items[idx]!
          // ACP updates are partial: a status or output update keeps the title and kind from earlier.
          const update = entry.payload as ToolPayload
          const merged = { ...(prev.data as ToolPayload), ...update }
          state.items[idx] = { ...prev, data: update.content ? expandToolDiffs(merged) : merged, endTs: entry.ts }
        } else {
          // The call lies before the loaded window: later updates merge into this row instead of adding bare ones.
          state.items.push({ kind: "tool", data: expandToolDiffs(entry.payload as ToolPayload) })
          if (id) tools[id] = state.items.length - 1
        }
        break
      }
      case "plan": {
        state.items.push({ kind: "plan", data: entry.payload })
        const entries = (entry.payload as { entries?: { status?: string; content?: string }[] }).entries ?? []
        const active =
          entries.find((item) => item.status === "in_progress") ??
          entries.find((item) => item.status === "pending") ??
          entries.at(-1)
        state.plan_kind = active?.content?.trim() || undefined
        break
      }
      case "permission_request":
        state.items.push({ kind: "permission", seq: entry.seq, data: entry.payload })
        state.needs_you = hasOutstandingRequests(state.items)
        break
      case "permission_result":
        resolveRequest(state, "permission", "requestId", entry.payload)
        break
      case "elicitation":
        state.items.push({ kind: "elicitation", seq: entry.seq, data: entry.payload })
        state.needs_you = hasOutstandingRequests(state.items)
        break
      case "elicitation_result":
        resolveRequest(state, "elicitation", "elicitationId", entry.payload)
        break
      case "notice":
        state.items.push({ kind: "divider", data: entry.payload })
        break
      case "rich_block":
        state.items.push({ kind: "rich_block", data: entry.payload })
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
        if (patch.status === "in_progress" && state.context_tokens !== undefined) {
          this.compacting = update.compactionId
          patchCompaction(state, update.compactionId, { before: state.context_tokens })
        }
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
          contextTokens?: number
          contextLimit?: number
          costUsd?: number
          _meta?: { "_claude/origin"?: unknown }
        }
        // Claude's own follow-up cycles (after a background task) have no prompt or turn_end; their
        // origin-stamped usage is the end. Inside a user turn it must not clear "working".
        if (!this.turn && usage._meta?.["_claude/origin"]) state.working = false
        // An update carries only the fields that changed; ACP names win over the flat aliases.
        const used = usage.used ?? usage.contextTokens
        if (used !== undefined) state.context_tokens = used
        state.context_limit = usage.size ?? usage.contextLimit ?? state.context_limit
        if (usage.cost) state.cost_usd = usage.cost.currency === "USD" ? usage.cost.amount : undefined
        else if (usage.costUsd !== undefined) state.cost_usd = usage.costUsd
        const compacting = this.compacting
        if (compacting && used !== undefined) {
          const item = state.items.find((candidate) => candidate.kind === "compaction" && (candidate.data as CompactionPayload).compactionId === compacting)
          if (item && (item.data as CompactionPayload).status !== "in_progress") {
            patchCompaction(state, compacting, { after: used })
            this.compacting = undefined
          }
        }
        break
      }
      case "subagent_update": {
        const update = entry.payload as SubagentUpdate
        if (!update.sessionId) break
        const found = this.child(update.sessionId, entry)
        if (update.title !== undefined) found.title = update.title ?? undefined
        if (update.description !== undefined) found.description = update.description ?? undefined
        if (update.capabilities !== undefined) found.cancellable = Boolean(update.capabilities?.cancel)
        if (update.state !== undefined) {
          found.state = update.state?.state ?? "unknown"
          found.stopReason = update.state?.stopReason ?? undefined
          found.endTs = found.state === "idle" ? entry.ts : undefined
        }
        break
      }
      case "session_message":
      case "session_message_chunk": {
        // Parent stream: the task handed to a child (places its card) or the child's reply.
        const message = entry.payload as { messageId?: string; senderSessionId?: string; recipientSessionId?: string }
        const to = message.recipientSessionId && message.recipientSessionId !== message.senderSessionId ? message.recipientSessionId : undefined
        if (nested) {
          const text = compactionText((entry.payload as { content?: unknown }).content)
          const last = state.items.at(-1)
          if (entry.kind === "session_message_chunk" && last?.kind === "task") {
            state.items[state.items.length - 1] = { ...last, data: { text: { text: chunkText(last.data) + text } } }
          } else if (text) state.items.push({ kind: "task", data: { text: { text } } })
        } else if (to && meta.has(to)) this.child(to, entry)
        break
      }
      case "agent_configuration":
        state.agentConfig = entry.payload as ThreadState["agentConfig"]
        state.agentMode = undefined
        break
      case "config_option_update":
        state.agentConfig = { ...state.agentConfig, configOptions: (entry.payload as { configOptions?: unknown }).configOptions }
        state.agentMode = undefined
        break
      case "current_mode_update":
        state.agentMode = (entry.payload as { currentModeId?: string }).currentModeId
        break
      case "queued_prompt": {
        const queued = entry.payload as QueuedPrompt
        if (queued.queueId) queue.set(queued.queueId, queued)
        break
      }
      case "dequeued_prompt":
        queue.delete((entry.payload as { queueId?: string }).queueId ?? "")
        break
      case "goal_update": {
        const next = entry.payload as GoalState
        if (!next?.state) break
        // The thread records each change of state, not every clock tick.
        if (next.state !== this.goal?.state) state.items.push({ kind: "goal", data: next })
        this.goal = next.state === "cleared" ? undefined : next
        break
      }
      case "session_info_update": {
        // Codex keeps working on a goal with no prompt or turn_end; its thread status is the only edge.
        const status = (entry.payload as { _meta?: { codex?: { threadStatus?: { type?: string } } } })._meta?.codex?.threadStatus?.type
        if (this.turn) break
        if (status === "active") state.working = !state.needs_you
        if (status === "idle") state.working = false
        break
      }
      case "turn_end": {
        state.working = false
        // A flattened subagent (Claude's tool:<id>) gets no end update of its own; the parent's turn
        // waits for it, so the turn's end is its end.
        if (!nested) for (const child of meta.values()) if (child.id.startsWith("tool:") && child.state === "running") Object.assign(child, { state: "idle", endTs: entry.ts })
        const turn = this.turn
        if (turn) {
          const tokens = state.context_tokens !== undefined && turn.tokens !== undefined ? state.context_tokens - turn.tokens : undefined
          const cost = state.cost_usd !== undefined && turn.cost !== undefined ? state.cost_usd - turn.cost : undefined
          state.last_turn = { ms: entry.ts - turn.ts, tokens: tokens !== undefined && tokens > 0 ? tokens : undefined, cost: cost !== undefined && cost > 0 ? cost : undefined }
          this.turn = undefined
        }
        // A failed or cancelled turn leaves its permissions and questions unanswerable; an MCP sign-in URL outlives it.
        if (!["error", "cancelled"].includes((entry.payload as { stopReason?: string }).stopReason ?? "")) break
        this.deactivate((item, data) => item.kind === "permission" ? !!data?.requestId : item.kind === "elicitation" && !!data?.elicitationId && !data.agentElicitationId)
        break
      }
      case "agent_exit":
        state.working = false
        this.turn = undefined
        this.turnTask = undefined
        this.deactivate((item, data) => item.kind === "permission" ? !!data?.requestId : item.kind === "elicitation" && !!data?.elicitationId)
        break
      case "async_task_spawned": {
        // The agent woke itself (a background task finished, a timer fired): that task is a turn of its own.
        const task = entry.payload as { asyncTaskId?: string; taskType?: string }
        if (task.taskType !== "turn" || !task.asyncTaskId) break
        this.turnTask = task.asyncTaskId
        state.working = !state.needs_you
        break
      }
      case "async_task_state_update": {
        const task = entry.payload as { asyncTaskId?: string; state?: string }
        if (task.asyncTaskId !== this.turnTask || !["completed", "failed", "stopped"].includes(task.state ?? "")) break
        this.turnTask = undefined
        state.working = false
        break
      }
      case "prompt":
        queue.delete((entry.payload as { queueId?: string }).queueId ?? "")
        state.working = true
        this.turn = { ts: entry.ts, tokens: state.context_tokens, cost: state.cost_usd }
        state.items.push({ kind: "user_message_chunk", data: entry.payload })
        break
    }
    // Fresh and replaced items carry the entry that last changed them; a replaced one keeps its first seq.
    for (let i = state.items.length - 1; i >= 0 && state.items[i]!.seq === undefined; i--) {
      state.items[i] = { ...state.items[i]!, seq: entry.seq, ts: entry.ts }
    }
    if (entry.kind === "tool_call" || entry.kind === "tool_call_update") {
      const linked = linkedSession(entry.payload)
      const id = (entry.payload as { toolCallId?: string }).toolCallId
      if (linked && id) this.child(linked, entry).toolCallId = id
    }
  }

  // The parent's turn ending also ends its subagents' open requests: nothing can answer them any more.
  private deactivate(applies: (item: ThreadItem, data: { requestId?: string; elicitationId?: string; agentElicitationId?: string } | undefined) => boolean) {
    const items = this.state.items
    for (let i = 0; i < items.length; i++) {
      const item = items[i]!
      const data = item.data as { requestId?: string; elicitationId?: string; resolved?: boolean; inactive?: boolean } | undefined
      if (applies(item, data) && !data?.resolved && !data?.inactive) items[i] = { ...item, data: { ...data, inactive: true } }
    }
    this.state.needs_you = hasOutstandingRequests(items)
    for (const child of this.children.values()) child.deactivate(applies)
  }

  // A new state object each call; the reducer's own list keeps changing afterwards.
  snapshot(): ThreadState {
    if (this.last) return this.last
    const state: ThreadState = { ...this.state, items: this.state.items.slice() }
    if (this.queue.size > 0) state.queue = [...this.queue.values()]
    if (this.goal) state.goal = this.goal
    if (state.working) state.turn_started = this.turn?.ts
    if (this.meta.size > 0) {
      state.subagents = {}
      for (const [id, info] of this.meta) {
        const source = this.children.get(id)?.snapshot() ?? EMPTY_THREAD
        // The brief a child works from: Claude (AIR) sends it only as the announcement's description, a
        // flattened child only in its parent tool call's input. An RFD session message already shows it.
        const call = info.toolCallId === undefined ? undefined : this.tools[info.toolCallId]
        const input = (call === undefined ? undefined : (this.state.items[call]?.data as ToolPayload | undefined)?.rawInput) as { prompt?: unknown } | undefined
        const brief = info.description || (typeof input?.prompt === "string" ? input.prompt : undefined)
        let summary = this.summaries.get(id)
        if (summary?.source !== source || summary.brief !== brief) {
          const tools = source.items.filter((item) => item.kind === "tool")
          const thread = brief && !source.items.some((item) => item.kind === "task")
            ? { ...source, items: [{ kind: "task", data: { text: { text: brief } }, ts: info.startedTs }, ...source.items] }
            : source
          summary = { source, brief, thread, tools: tools.length, lastTool: (tools.at(-1)?.data as { title?: string } | undefined)?.title }
          this.summaries.set(id, summary)
        }
        state.subagents[id] = { ...info, thread: summary.thread, tools: summary.tools, lastTool: summary.lastTool, tokens: source.context_tokens }
        // A child waiting on you makes the whole chat wait on you.
        if (source.needs_you) state.needs_you = true
      }
    }
    this.last = state
    return state
  }
}


export function reduceLog(entries: LogEntry[], nested = false): ThreadState {
  const reducer = new ThreadReducer(nested)
  for (const entry of entries) reducer.apply(entry)
  return reducer.snapshot()
}

// Per-chat reducers, so a streamed entry applies alone instead of replaying the log. A refetch hands over
// new entry objects, which no longer match the last applied one, and rebuilds once.
const threads = new Map<string, { reducer: ThreadReducer; count: number; last: LogEntry | undefined; first: LogEntry | undefined; state: ThreadState }>()
onSignOut(() => threads.clear())
const THREADS_KEPT = 16

export function reduceThread(key: string, entries: LogEntry[]): ThreadState {
  let cached = threads.get(key)
  if (cached && cached.count === entries.length && cached.last === entries.at(-1) && cached.first === entries[0]) return cached.state
  if (!cached || entries.length < cached.count || cached.first !== entries[0] || (cached.count > 0 && entries[cached.count - 1] !== cached.last)) {
    cached = { reducer: new ThreadReducer(), count: 0, last: undefined, first: entries[0], state: { items: [], needs_you: false, working: false } }
  }
  for (let i = cached.count; i < entries.length; i++) cached.reducer.apply(entries[i]!)
  cached.count = entries.length
  cached.last = entries.at(-1)
  cached.state = cached.reducer.snapshot()
  threads.delete(key)
  threads.set(key, cached)
  if (threads.size > THREADS_KEPT) threads.delete(threads.keys().next().value!)
  return cached.state
}

function patchCompaction(state: ThreadState, compactionId: string, patch: Partial<CompactionPayload>) {
  const items = state.items
  for (let i = 0; i < items.length; i++) {
    const item = items[i]!
    if (item.kind === "compaction" && (item.data as CompactionPayload).compactionId === compactionId) items[i] = { ...item, data: { ...(item.data as object), ...patch } }
  }
}

// Helper chats this chat's agent started with agentpack tools become cards on the tool call that
// started them: the call whose result names the helper, else (agents such as Antigravity send no
// MCP result) the first unclaimed helper started after a run_helper, start_session or fork_session
// call. Open and Stop act on the helper chat itself, whose id follows "helper:".
// Tool payloads are replaced, never mutated, so a render only serializes the calls that changed.
const toolText = new WeakMap<ToolPayload, string>()

export function helperSubagents(items: ThreadItem[], helpers: SessionInfo[]): Record<string, Subagent> {
  const out: Record<string, Subagent> = {}
  if (helpers.length === 0) return out
  const started = (session: SessionInfo) => (session.started_at ? Date.parse(session.started_at) : 0)
  const byStart = [...helpers].sort((a, b) => started(a) - started(b))
  for (const item of items) {
    const tool = item.kind === "tool" ? (item.data as ToolPayload) : undefined
    if (!tool?.toolCallId) continue
    const free = byStart.filter((session) => !out[`helper:${session.id}`])
    let result = toolText.get(tool)
    if (result === undefined) toolText.set(tool, (result = JSON.stringify([tool.rawOutput, tool.content, tool.output])))
    const helper =
      free.find((session) => result.includes(session.id)) ??
      // The worker and the helper share a clock; a second of slack covers the log's own timestamping.
      (/run_helper|start_session|fork_session/.test(tool.title ?? "") ? free.find((session) => started(session) >= (item.ts ?? 0) - 1000) : undefined)
    if (!helper) continue
    const running = !helper.ended_at && helper.state.running && helper.conversation_state !== "idle"
    const needsYou = running && helper.conversation_state === "needs_you"
    out[`helper:${helper.id}`] = {
      id: `helper:${helper.id}`,
      title: helper.name || helper.title || "Helper chat",
      description: `${harnessName(helper.harness)} helper chat${needsYou ? " · needs you" : ""}`,
      state: running ? "working" : "idle",
      cancellable: running,
      toolCallId: tool.toolCallId,
      startedTs: started(helper) || undefined,
      endTs: running ? undefined : Date.parse(helper.ended_at ?? helper.last_event_at ?? "") || undefined,
      thread: { items: [], needs_you: needsYou, working: running },
      tools: 0,
    }
  }
  return out
}
