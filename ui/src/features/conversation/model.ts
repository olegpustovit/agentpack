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
}

export type ThreadState = {
  items: ThreadItem[]
  needs_you: boolean
  working: boolean
  plan_kind?: string
  context_tokens?: number
  context_limit?: number
  cost_usd?: number
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
  content?: ToolDiffBlock[]
}

export type ToolDiffBlock = {
  type?: string
  path?: string
  oldText?: string
  newText?: string
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
}

export type CompactionPayload = {
  compactionId: string
  status: string
  summary: string
  error?: string
}

export type ElicitationPayload = {
  elicitationId?: string
  prompt?: string
  choices?: string[]
  resolved?: boolean
  inactive?: boolean
  result?: { value?: string }
}
