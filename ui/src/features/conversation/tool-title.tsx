import { useQuery } from "@tanstack/react-query"
import {
  Activity, Bell, Bot, Bug, ChartColumn, CircleStop, Folder, Gauge, GitFork, Hourglass, ListChecks, MessageCircleQuestion,
  MessagesSquare, Monitor, Package, PanelTop, Play, Plug, Reply, RotateCcw, Send, ShieldCheck, Square, SquareTerminal, type LucideIcon,
} from "lucide-react"
import { nodesQuery } from "@/features/machines/queries"
import { sessionsQuery } from "@/features/sessions/queries"
import type { ToolPayload } from "./model"

export type McpCall = { server: string; tool: string }

// Claude: `mcp__agentpack__read_conversation`; Antigravity: `Run mcp_agentpack_read_conversation`.
export function mcpCall(tool: ToolPayload): McpCall | undefined {
  const meta = (tool as { _meta?: { claudeCode?: { toolName?: unknown } } })._meta?.claudeCode?.toolName
  const name = (typeof meta === "string" ? meta : tool.title ?? "").replace(/^Run /, "")
  const match = name.match(/^mcp__(.+?)__(.+)$/) ?? name.match(/^mcp_(agentpack)_(.+)$/)
  return match ? { server: match[1]!, tool: match[2]! } : undefined
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

// The verb and its object. Names come from the replicated lists the sidebar already holds.
export function mcpLabel(call: McpCall, input: unknown, sessionName: (id: string) => string, machineName: (id: string) => string): [string, string] {
  if (call.server !== "agentpack" || !agentpack[call.tool]) return [words(call.tool), call.server]
  const fields = input && typeof input === "object" ? (input as Record<string, unknown>) : {}
  const text = (key: string) => (typeof fields[key] === "string" ? (fields[key] as string).trim() : "")
  const folder = text("directory") || text("folder") || text("path")
  const issue = text("issue_id") || text("id")
  const ids = Array.isArray(fields.session_ids) ? fields.session_ids.filter((id): id is string => typeof id === "string") : []
  const target = [
    text("session_id") && sessionName(text("session_id")),
    ids.map(sessionName).join(", "),
    folder && folder.split("/").filter(Boolean).at(-1),
    text("package") || text("module"),
    call.tool.endsWith("issue") && issue,
    call.tool === "show" && text("title"),
    text("node") && `on ${machineName(text("node"))}`,
  ].filter(Boolean).join(" ")
  const quote = text("text") || text("prompt") || text("task")
  return [agentpack[call.tool]![1], [target, quote && `“${quote.split("\n")[0]}”`].filter(Boolean).join(" · ")]
}

// A failed call's reason is short ("conversation unavailable"), so it reads on the row.
export function failureReason(output: string): string {
  return output.split("\n").find((line) => line.trim() && !line.startsWith("```"))?.trim() ?? ""
}

export function McpTitle({ call, input }: { call: McpCall; input: unknown }) {
  const { data: sessions } = useQuery({ ...sessionsQuery(true), enabled: call.server === "agentpack" })
  const { data: nodes } = useQuery({ ...nodesQuery(true), enabled: call.server === "agentpack" })
  const sessionName = (id: string) => {
    const row = sessions?.find((session) => session.id === id || session.id === `session-${id}`)
    return row?.name || id.replace(/^session-/, "").slice(0, 8)
  }
  const machineName = (id: string) => nodes?.find((node) => node.id === id)?.name ?? id
  const [verb, detail] = mcpLabel(call, input, sessionName, machineName)
  return (
    <span className="flex min-w-0 flex-1 items-baseline gap-(--s2)">
      <span className="shrink-0 text-(--fg)">{verb}</span>
      {detail && <span className="min-w-0 truncate text-(--fg2)">{detail}</span>}
    </span>
  )
}
