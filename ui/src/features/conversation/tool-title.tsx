import { useQuery } from "@tanstack/react-query"
import {
  Activity, Bell, Bot, Bug, ChartColumn, CircleStop, Folder, Gauge, GitFork, Hourglass, ListChecks, MessageCircleQuestion,
  MessagesSquare, Monitor, Package, PanelTop, Play, Plug, Reply, RotateCcw, Send, ShieldCheck, Square, SquareTerminal, type LucideIcon,
} from "lucide-react"
import { useEventsConnected } from "@/features/events/use-events"
import { identityQuery, useMachines, type Machine } from "@/features/machines/queries"
import { sessionsQuery } from "@/features/sessions/queries"
import type { SessionInfo } from "@/lib/api-types"
import type { ToolPayload } from "./model"

export type McpCall = {
  server: string
  tool: string
  args?: unknown
  /** Fallback detail when the call has no structured target (OMP title, Antigravity toolSummary). */
  fallback?: string
}

type ToolMeta = {
  jetbrains?: { air?: { mcp?: { server?: unknown; tool?: unknown } } }
  claudeCode?: { toolName?: unknown }
}

const MCP_NAME = /^[\w.-]+$/

function record(value: unknown): Record<string, unknown> | undefined {
  return value && typeof value === "object" && !Array.isArray(value) ? (value as Record<string, unknown>) : undefined
}

function makeCall(server: string, tool: string, args: unknown, fallback?: string): McpCall {
  return {
    server,
    tool,
    ...(args !== undefined ? { args } : {}),
    ...(fallback ? { fallback } : {}),
  }
}

function parseMcpUnderscore(name: string): { server: string; tool: string } | undefined {
  const dbl = name.match(/^mcp__([\w.-]+?)__([\w.-]+)$/)
  if (dbl) return { server: dbl[1]!, tool: dbl[2]! }
  const single = name.match(/^mcp__([A-Za-z0-9.-]+)_([\w.-]+)$/)
  if (single) return { server: single[1]!, tool: single[2]! }
  return undefined
}

// Parse MCP tool calls across harnesses:
// - Claude: `_meta.jetbrains.air.mcp`, else `_meta.claudeCode.toolName` or title `mcp__<server>__<tool>`
// - OMP: `rawInput = { path: "xd://mcp__<server>_<tool>", content: "<JSON>" }` or title `mcp__<server>_<tool>`
// - Codex: `rawInput = { server, tool, arguments }` and/or title `mcp.<server>.<tool>`
// - Antigravity: title `Run mcp_<server>_<tool>`, args = rawInput minus `toolSummary`
export function mcpCall(tool: ToolPayload): McpCall | undefined {
  const meta = (tool as { _meta?: ToolMeta })._meta
  const input = record(tool.rawInput)
  const title = tool.title?.trim() ?? ""

  const air = meta?.jetbrains?.air?.mcp
  if (typeof air?.server === "string" && typeof air?.tool === "string" && MCP_NAME.test(air.server) && MCP_NAME.test(air.tool)) {
    return makeCall(air.server, air.tool, tool.rawInput)
  }

  const claudeName = typeof meta?.claudeCode?.toolName === "string" ? meta.claudeCode.toolName.trim() : ""
  if (claudeName) {
    const parsed = parseMcpUnderscore(claudeName)
    if (parsed) return makeCall(parsed.server, parsed.tool, tool.rawInput)
  }

  if (typeof input?.path === "string" && input.path.startsWith("xd://")) {
    const parsed = parseMcpUnderscore(input.path.slice("xd://".length))
    if (parsed) {
      let args: unknown
      if (typeof input.content === "string") {
        try { args = JSON.parse(input.content) } catch { args = undefined }
      }
      return makeCall(parsed.server, parsed.tool, args, title || undefined)
    }
  }

  if (
    typeof input?.server === "string" &&
    typeof input?.tool === "string" &&
    MCP_NAME.test(input.server) &&
    MCP_NAME.test(input.tool) &&
    ("arguments" in input || title.startsWith("mcp."))
  ) {
    return makeCall(input.server, input.tool, input.arguments)
  }
  const codex = title.match(/^mcp\.([\w-]+)\.([\w.-]+)$/)
  if (codex) {
    return makeCall(codex[1]!, codex[2]!, input && "arguments" in input ? input.arguments : tool.rawInput)
  }

  const fromTitle = parseMcpUnderscore(title)
  if (fromTitle) return makeCall(fromTitle.server, fromTitle.tool, tool.rawInput)

  const agy = title.match(/^Run mcp_([A-Za-z0-9.-]+)_([\w.-]+)$/)
  if (agy) {
    const { toolSummary, ...args } = input ?? {}
    const fallback = typeof toolSummary === "string" && toolSummary.trim() ? toolSummary.trim() : undefined
    return makeCall(agy[1]!, agy[2]!, input ? args : tool.rawInput, fallback)
  }

  return undefined
}

// What each agentpack tool did, as a verb the target reads after.
const agentpack: Record<string, [LucideIcon, string]> = {
  get_attention: [Bell, "Checked what needs you"],
  wait: [Hourglass, "Waited for"],
  read_conversation: [MessagesSquare, "Read chat"],
  send_prompt: [Send, "Sent a message to"],
  respond_permission: [ShieldCheck, "Answered a permission in"],
  answer_question: [MessageCircleQuestion, "Answered a question in"],
  cancel: [CircleStop, "Cancelled the turn in"],
  fork_session: [GitFork, "Forked"],
  run_helper: [Bot, "Started a helper"],
  get_usage: [ChartColumn, "Read usage"],
  get_limits: [Gauge, "Read plan limits"],
  show: [PanelTop, "Showed a card"],
  ask: [Send, "Asked"],
  reply: [Reply, "Replied"],
  cancel_task: [CircleStop, "Cancelled a task"],
  tasks: [ListChecks, "Read tasks"],
  list_issues: [Bug, "Listed issues"],
  get_issue: [Bug, "Read issue"],
  get_trace: [Activity, "Read a trace"],
  resolve_issue: [Bug, "Resolved issue"],
  list_nodes: [Monitor, "Listed machines"],
  list_sessions: [MessagesSquare, "Listed chats"],
  list_recent_folders: [Folder, "Listed recent folders"],
  start_session: [Play, "Started a chat in"],
  stop_session: [Square, "Stopped"],
  restart_session: [RotateCcw, "Restarted"],
  read_terminal: [SquareTerminal, "Read the terminal of"],
  write_terminal: [SquareTerminal, "Typed into"],
  list_packages: [Package, "Listed packages in"],
  install_package: [Package, "Installed"],
}

export function mcpIcon(call: McpCall): LucideIcon {
  return call.server === "agentpack" ? agentpack[call.tool]?.[0] ?? Plug : Plug
}

const words = (name: string) => name.replace(/[_-]+/g, " ").trim()
const base = (path: string) => path.split("/").filter(Boolean).at(-1) ?? path

// The verb and its object. Names come from the replicated lists the sidebar already holds.
export function mcpLabel(call: McpCall, input: unknown, sessionName: (id: string) => string, machineName: (id: string) => string): [string, string] {
  if (call.server !== "agentpack" || !agentpack[call.tool]) {
    return [words(call.tool), call.fallback ? `${call.server} · ${call.fallback}` : call.server]
  }
  const fields = record(input) ?? {}
  const text = (key: string) => (typeof fields[key] === "string" ? (fields[key] as string).trim() : "")
  const strings = (key: string) => (Array.isArray(fields[key]) ? (fields[key] as unknown[]).filter((id): id is string => typeof id === "string" && id.trim() !== "") : [])
  const folder = text("directory") || text("folder") || text("path")
  const sessionIds = strings("session_ids")
  const taskIds = strings("task_ids")
  const waitTargets = [
    ...sessionIds.map(sessionName),
    ...(taskIds.length ? [taskIds.length === 1 ? "1 task" : `${taskIds.length} tasks`] : []),
  ]
  const spec = call.tool === "install_package" ? text("spec") : ""
  const issue = text("fingerprint") || text("issue_id") || text("id")
  const target = [
    text("session_id") && sessionName(text("session_id")),
    waitTargets.join(", "),
    spec,
    folder && (spec ? `in ${base(folder)}` : base(folder)),
    call.tool.endsWith("issue") && issue,
    call.tool === "get_trace" && text("trace_id"),
    call.tool === "cancel_task" && text("task_id"),
    call.tool === "list_issues" && text("status"),
    call.tool === "show" && text("title"),
    text("node") && `on ${machineName(text("node"))}`,
  ].filter(Boolean).join(" ")
  const quote = text("text") || text("prompt") || text("answer") || text("task")
  const detail = [target, quote && `“${quote.split("\n")[0]}”`].filter(Boolean).join(" · ")
  return [agentpack[call.tool]![1], detail || call.fallback || ""]
}

// Text carried in rawOutput: a plain string, or Codex's `{error}` / `{result: {content: [{text}]}}`.
export function rawOutputText(rawOutput: unknown): string {
  if (typeof rawOutput === "string") return rawOutput.trim()
  const body = record(rawOutput)
  if (!body) return ""
  if (typeof body.error === "string" && body.error.trim()) return body.error.trim()
  const errorObj = record(body.error)
  if (typeof errorObj?.message === "string" && errorObj.message.trim()) return errorObj.message.trim()
  const result = record(body.result)
  if (Array.isArray(result?.content)) {
    const lines = result.content
      .map((block) => record(block)?.text)
      .filter((text): text is string => typeof text === "string" && text.trim() !== "")
    if (lines.length) return lines.join("\n").trim()
  }
  return ""
}

// A failed call's reason is short ("conversation unavailable"), so it reads on the row.
export function failureReason(output: string, rawOutput?: unknown): string {
  const source = output.trim() ? output : rawOutputText(rawOutput)
  return source.split("\n").find((line) => line.trim() && !line.startsWith("```"))?.trim() ?? ""
}

export function sessionNameMap(sessions: readonly SessionInfo[] = []): Map<string, string> {
  const map = new Map<string, string>()
  for (const session of sessions) {
    const label = session.name || session.title
    if (label) map.set(session.id, label).set(session.id.replace(/^session-/, ""), label)
  }
  return map
}

export function machineNameMap(machines: readonly Pick<Machine, "id" | "name">[], localNode?: { id: string; name: string }): Map<string, string> {
  const map = new Map<string, string>()
  for (const machine of machines) map.set(machine.id, machine.name)
  if (localNode?.id && localNode.name) map.set(localNode.id, localNode.name)
  return map
}

function McpTitleText({ verb, detail }: { verb: string; detail: string }) {
  return (
    <span className="flex min-w-0 flex-1 items-baseline gap-(--s2)">
      <span className="shrink-0 text-(--fg)">{verb}</span>
      {detail && <span className="min-w-0 truncate text-(--fg2)"><span className="sr-only"> </span>{detail}</span>}
    </span>
  )
}

function AgentpackMcpTitle({ call, input }: { call: McpCall; input: unknown }) {
  const live = useEventsConnected()
  const { data: sessions } = useQuery(sessionsQuery(live))
  const machines = useMachines()
  const localNode = useQuery(identityQuery).data?.node
  const sessionNames = sessionNameMap(sessions)
  const machineNames = machineNameMap(machines, localNode)
  const sessionName = (id: string) => sessionNames.get(id) ?? sessionNames.get(id.replace(/^session-/, "")) ?? id.replace(/^session-/, "").slice(0, 8)
  const machineName = (id: string) => machineNames.get(id) ?? id
  const [verb, detail] = mcpLabel(call, input, sessionName, machineName)
  return <McpTitleText verb={verb} detail={detail} />
}

export function McpTitle({ call, input }: { call: McpCall; input?: unknown }) {
  const args = input ?? call.args
  if (call.server === "agentpack") return <AgentpackMcpTitle call={call} input={args} />
  const [verb, detail] = mcpLabel(call, args, String, String)
  return <McpTitleText verb={verb} detail={detail} />
}
