import type { GoalState } from "@/lib/api-types"

// Mirrors internal/acp LogEntry and ThreadState JSON.

export type LogEntry = {
  seq: number
  ts: number
  kind: string
  payload: unknown
}

export type ThreadItem = {
  kind: string
  data?: unknown
  /** seq and ts (ms) of the log entry that created the item; stable while the log grows. */
  seq?: number
  ts?: number
  /** ts of the latest update to a tool call. */
  endTs?: number
}

export type ThreadState = {
  items: ThreadItem[]
  needs_you: boolean
  working: boolean
  agentConfig?: { configOptions?: unknown; modes?: unknown }
  agentMode?: string
  plan_kind?: string
  context_tokens?: number
  context_limit?: number
  cost_usd?: number
  /** The finished turn's length, and its token and cost growth when the agent reports usage. */
  last_turn?: { ms: number; tokens?: number; cost?: number }
  /** ts of the prompt that started the running turn. */
  turn_started?: number
  queue?: QueuedPrompt[]
  goal?: GoalState
  subagents?: Record<string, Subagent>
}

export type QueuedPrompt = UserMessagePayload & { queueId: string; steer?: boolean }

export type Subagent = {
  id: string
  title?: string
  description?: string
  state: string
  stopReason?: string
  cancellable: boolean
  /** The parent's tool call that spawned it, when known. */
  toolCallId?: string
  startedTs?: number
  endTs?: number
  thread: ThreadState
  tools: number
  tokens?: number
  lastTool?: string
}

export type UserAttachmentPayload = {
  blob?: string
  mime?: string
  width?: number
  height?: number
  name?: string
  kind?: "image" | "resource_link"
}

export type UserMessagePayload = {
  text?: { text?: string }
  image?: UserAttachmentPayload
  resource_link?: UserAttachmentPayload
  images?: UserAttachmentPayload[]
}

export type TextChunkPayload = UserMessagePayload

export type ToolPayload = {
  toolCallId?: string
  title?: string
  kind?: string
  status?: string
  command?: string
  output?: string
  rawInput?: unknown
  rawOutput?: unknown
  content?: ToolDiffBlock[]
}

export type ToolDiffBlock = {
  type?: string
  path?: string
  oldText?: string
  newText?: string
  newTextFromInput?: boolean
  content?: { type?: string; text?: string }
}

export type PermissionPayload = {
  requestId?: string
  tool?: string
  summary?: string
  options?: { id: string; label: string }[]
  resolved?: boolean
  inactive?: boolean
  result?: { optionId?: string }
}

export type NoticePayload = {
  severity?: string
  title?: string
  description?: string | null
  /** "auth_required" when the agent must sign in before the chat can start. */
  code?: string
  harness?: string
  /** The agent's ways to sign in, so the card needs no round trip. */
  methods?: { id: string; label: string }[]
}

export type CompactionPayload = {
  compactionId: string
  status: string
  summary: string
  error?: string
  /** Context tokens before and after, when the agent reports usage. */
  before?: number
  after?: number
}

export type ElicitationField = {
  key: string
  title?: string
  description?: string
  multiple?: boolean
  options?: { value: string; label: string; description?: string }[]
}

export type ElicitationPayload = {
  elicitationId?: string
  prompt?: string
  choices?: string[]
  /** A form: each field is a question with options, or free text. Answered with a JSON object keyed by field. */
  fields?: ElicitationField[]
  /** A URL request (an MCP app sign-in): the person opens it, then answers "done" or "decline". */
  url?: string
  agentElicitationId?: string
  resolved?: boolean
  inactive?: boolean
  result?: { value?: string }
}
