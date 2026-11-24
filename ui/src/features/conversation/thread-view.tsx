import { lazy, Suspense, useLayoutEffect, useRef, useState, type ReactNode, type RefObject } from "react"
// virtual-core is pinned exactly: React Compiler refuses @tanstack/react-virtual, so this drives its private _didMount/_willUpdate (virtualizer.test.ts).
import { elementScroll, observeElementOffset, observeElementRect, Virtualizer, type VirtualItem } from "@tanstack/virtual-core"
import { BookOpen, Check, ChevronRight, Copy, ExternalLink, Flag, Folder, GitFork, Globe, Pencil, Lock, Quote, RotateCcw, Search, Sparkles, SquareTerminal, Wrench } from "lucide-react"
import { RowContextMenu, RowContextMenuContent, RowContextMenuItem, RowContextMenuTrigger } from "@/components/kit/overlays"
import { Button } from "@/components/ui/button"
import { Spinner } from "@/components/kit/feedback"
import { LOCAL } from "@/lib/api"
import { copyText, useCopied } from "@/lib/hud"
import { SignInCard } from "@/features/agents/sign-in"
import { cn } from "@/lib/utils"
import type { GoalState } from "@/lib/api-types"
import { clock, goalStateWords } from "@/lib/goal"
import { useNow } from "@/lib/use-now"
import { promptSender, promptText, type CompactionPayload, type NoticePayload, type PromptFrom, type Subagent, type TextChunkPayload, type ThreadItem, type ThreadState, type RichBlockPayload, type ToolPayload, type UserAttachmentPayload, type UserMessagePayload } from "./model"
import { compactCount, SubagentCard, subagentRunning } from "./subagent-card"
import { AgentMessageBody } from "./agent-message"
import { ToolOutput } from "./tool-output"
import { chunkAttachments, isOpenRequest } from "./reducer"
import { ElicitationCard, PermissionCard } from "./request-cards"
import { QuickLook } from "./quick-look"
import { listDiffChanges } from "./diff-summary"
import { failureReason, mcpCall, mcpIcon, McpTitle, rawOutputText } from "./tool-title"

// The diff library loads with the first opened diff, not with the chat.
const ToolDiffBody = lazy(() => import("./tool-diff").then((module) => ({ default: module.ToolDiffBody })))

function chunkText(data: unknown): string {
  return (data as TextChunkPayload).text?.text ?? ""
}

export function userImageSrc(machine: string, sessionId: string, blob?: string) {
  if (!blob) return ""
  const sha = blob.replace(/^sha256:/i, "")
  const url = new URL(`/api/sessions/${encodeURIComponent(sessionId)}/blobs/${sha}`, location.origin)
  if (machine && machine !== LOCAL) url.searchParams.set("node", machine)
  return url.pathname + url.search
}

function UserMessageBody({
  data,
  machine,
  sessionId,
  onQuote,
  onFork,
  onOpenChat,
}: {
  data: unknown
  machine: string
  sessionId: string
  onQuote?: (text: string) => void
  onFork?: () => void
  onOpenChat?: (from: PromptFrom) => void
}) {
  const body = data as UserMessagePayload
  const text = promptText(body)
  const sender = body.from && (
    <div className="flex items-center gap-(--s2)">
      <p aria-hidden className="text-(length:--fs-xs) font-medium text-(--fg3)">{promptSender(body.from)}</p>
      {onOpenChat && (
        <Button type="button" variant="ghost" size="sm" className="h-auto px-(--s1) py-0 text-(length:--fs-xs)" aria-label={`Open ${body.from.name || "the other chat"}`} onClick={() => onOpenChat(body.from!)}>
          Open chat
        </Button>
      )}
    </div>
  )
  // Another agent's answer or question reads as agent output: a card with its markdown rendered.
  if (body.from?.kind === "reply") {
    return (
      <div data-slot="reply-card" className="flex w-full flex-col gap-(--s2) rounded-(--r-sheet) border-[0.5px] border-(--sep) px-(--s4) py-(--s3)">
        {sender}
        <MessageContextMenu text={text} onQuote={onQuote} onFork={onFork}>
          <div className="min-w-0"><AgentMessageBody text={text} /></div>
        </MessageContextMenu>
      </div>
    )
  }
  return (
    <div className="flex w-full flex-col items-start gap-(--s2)">
      {sender || <p aria-hidden className="text-(length:--fs-xs) font-medium text-(--fg3)">You</p>}
      <Attachments slot="user-attachments" items={chunkAttachments(body)} machine={machine} sessionId={sessionId} />
      {text ? (
        <MessageContextMenu text={text} onQuote={onQuote} onFork={onFork}>
          <p data-slot="user-bubble" className={cn("w-full rounded-(--r-sheet) px-(--s4) py-(--s3) text-(length:--fs) text-(--fg) whitespace-pre-wrap [overflow-wrap:anywhere]", body.from ? "border-[0.5px] border-(--sep)" : "bg-(--track)")}>
            {text}
          </p>
        </MessageContextMenu>
      ) : null}
    </div>
  )
}

function safeLink(uri?: string): string | undefined {
  try {
    return uri && ["http:", "https:"].includes(new URL(uri).protocol) ? uri : undefined
  } catch {
    return undefined
  }
}

/** Images (from the session's blob store, previewed in QuickLook) and links that a person or agent attached. */
function Attachments({ slot, items, machine, sessionId }: { slot: string; items: UserAttachmentPayload[]; machine: string; sessionId: string }) {
  const [quickLook, setQuickLook] = useState<number | null>(null)
  if (items.length === 0) return null
  const open = quickLook === null ? undefined : items[quickLook]
  return (
    <div data-slot={slot} className="flex max-w-full flex-wrap items-start gap-(--s3)">
      {items.map((item, index) => {
        const src = userImageSrc(machine, sessionId, item.blob)
        const label = item.name || `image ${index + 1}`
        if (src) {
          return (
            <button key={`${item.blob}-${index}`} type="button" aria-label={`Preview ${label}`} className="rounded-(--r-pop) border-[0.5px] border-(--sep) p-0" onClick={() => setQuickLook(index)}>
              <img src={src} alt={item.name ?? ""} width={item.width} height={item.height} className="max-h-48 max-w-full rounded-(--r-pop) object-contain" />
            </button>
          )
        }
        const href = safeLink(item.uri)
        const name = item.name || item.uri
        if (!name) return null
        const chip = "inline-flex max-w-full items-center gap-(--s2) rounded-(--r-ctl) border-[0.5px] border-(--sep) px-(--s3) py-(--s1) text-(length:--fs-sm) text-(--fg)"
        return href ? (
          <a key={`${item.uri}-${index}`} data-slot="attachment-link" href={href} target="_blank" rel="noopener noreferrer" className={cn(chip, "hover:bg-(--hover)")}>
            <ExternalLink aria-hidden className="size-3.5 shrink-0 text-(--fg2)" />
            <span className="truncate">{name}</span>
          </a>
        ) : (
          <span key={`${item.uri}-${index}`} data-slot="attachment-link" title={item.uri} className={chip}>
            <span className="truncate">{name}</span>
          </span>
        )
      })}
      {open?.blob && <QuickLook src={userImageSrc(machine, sessionId, open.blob)} label={open.name || "Attached image"} onClose={() => setQuickLook(null)} />}
    </div>
  )
}

/** A card an agent showed with the show tool: title, markdown, a table and an image. */
function RichCard({ data, machine, sessionId }: { data: unknown; machine: string; sessionId: string }) {
  const card = data as RichBlockPayload
  const columns = card.table?.columns ?? []
  return (
    <div data-slot="rich-card" className="flex w-full max-w-[40rem] flex-col overflow-hidden rounded-(--r-sheet) border-[0.5px] border-(--sep) bg-(--card) shadow-(--shadow-card)">
      <p className="border-b-[0.5px] border-(--sep-soft) px-(--s4) py-(--s3) text-(length:--fs-sm) font-semibold text-(--fg)">{card.title}</p>
      {card.markdown && <div className="px-(--s4) py-(--s3)"><AgentMessageBody text={card.markdown} /></div>}
      {columns.length > 0 && (
        <div className="overflow-x-auto">
          <table className="w-full border-collapse text-(length:--fs-sm)">
            <thead>
              <tr>{columns.map((column, index) => <th key={index} scope="col" className="border-b-[0.5px] border-(--sep-soft) px-(--s4) py-(--s2) text-left font-medium text-(--fg2)">{column}</th>)}</tr>
            </thead>
            <tbody>
              {(card.table?.rows ?? []).map((row, rowIndex) => (
                <tr key={rowIndex}>{columns.map((_, index) => <td key={index} className="border-b-[0.5px] border-(--sep-soft) px-(--s4) py-(--s2) tabular-nums text-(--fg)">{row[index] ?? ""}</td>)}</tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {card.image && <div className="p-(--s4)"><Attachments slot="rich-card-image" items={[card.image]} machine={machine} sessionId={sessionId} /></div>}
    </div>
  )
}

// Agent errors arrive as JSON-RPC error objects; show their words, not the JSON.
export function noticeText(description: string): string {
  try {
    const error = JSON.parse(description) as { message?: unknown; data?: { error?: unknown; details?: unknown } }
    const detail = error.data?.details ?? error.data?.error
    if (typeof error.message === "string") return typeof detail === "string" && detail !== error.message ? `${error.message}: ${detail}` : error.message
  } catch {
    // Plain text.
  }
  return description
}

export function quoteMessage(text: string): string {
  return text.split("\n").map((line) => line ? `> ${line}` : ">").join("\n")
}

export function latestUserPrompt(items: ThreadItem[]): UserMessagePayload | undefined {
  for (let index = items.length - 1; index >= 0; index--) {
    const item = items[index]!
    if (item.kind === "user_message_chunk") return item.data as UserMessagePayload
  }
  return undefined
}

function toolIcon(kind?: string) {
  switch (kind) {
    case "read": return BookOpen
    case "search": return Search
    case "list": return Folder
    case "edit": return Pencil
    case "execute":
    case "terminal": return SquareTerminal
    case "fetch": return Globe
    case "think": return Sparkles
    default: return Wrench
  }
}

function statusGlyph(status: string) {
  if (status === "in_progress") return <span role="img" aria-label={status} title={status} className="shrink-0"><Spinner /></span>
  const glyph = status === "completed" ? "✓" : status === "failed" ? "✕" : "…"
  const tone = status === "failed" ? "text-(--danger-word)" : status === "completed" ? "text-(--ok-word)" : "text-(--fg3)"
  return (
    <span role="img" aria-label={status} title={status} className={`shrink-0 ${tone}`}>
      {glyph}
    </span>
  )
}

// One-line form of a tool's input: a string as is, an object as `key: value` pairs.
function shortInput(input: unknown): string {
  if (input === undefined || input === null) return ""
  if (typeof input !== "object") return String(input)
  return Object.entries(input).map(([key, value]) => `${key}: ${typeof value === "string" ? value : JSON.stringify(value)}`).join(", ").replace(/\s+/g, " ")
}

// Shell output: the fixture's `output`, the adapter's rawOutput (string or Codex object), or text content without the console fence.
export function toolOutput(tool: ToolPayload): string {
  if (tool.output) return tool.output.trim()
  const raw = rawOutputText(tool.rawOutput)
  if (raw) return raw
  const text = (tool.content ?? []).flatMap((block) => block.type === "content" && block.content?.type === "text" && block.content.text ? [block.content.text] : []).join("\n")
  return text.replace(/^```[a-z]*\n?/, "").replace(/\n?```\s*$/, "").trim()
}


// The file a tool read or wrote, which names the output's language.
function toolPath(input: unknown): string | undefined {
  const fields = input && typeof input === "object" ? (input as Record<string, unknown>) : {}
  const path = fields.file_path ?? fields.path ?? fields.notebook_path
  return typeof path === "string" ? path : undefined
}

// A <details> whose body mounts only while open: closed tool rows cost nothing to render.
function useOpen(initial: boolean) {
  const [open, setOpen] = useState(initial)
  return { open, onToggle: (event: { currentTarget: HTMLDetailsElement }) => setOpen(event.currentTarget.open) }
}

// Images and links a tool returned, e.g. a browser screenshot from an MCP server.
function toolAttachments(tool: ToolPayload): UserAttachmentPayload[] {
  return (tool.content ?? []).flatMap((block): UserAttachmentPayload[] => {
    const content = block.type === "content" ? block.content : undefined
    if (content?.type === "image" && content.blob) return [{ blob: content.blob, mime: content.mimeType, name: tool.title }]
    if (content?.type === "resource_link" && content.uri) return [{ uri: content.uri, name: content.name }]
    return []
  })
}

// An update whose call lies before the loaded window has no title or kind: name it from what it carries.
function toolTitle(tool: ToolPayload, none = "Tool"): string {
  const input = tool.rawInput as { command?: unknown; description?: unknown } | undefined
  const meta = (tool as { _meta?: { claudeCode?: { toolName?: unknown } } })._meta?.claudeCode?.toolName
  const text = (value: unknown) => (typeof value === "string" && value.trim() ? value : undefined)
  return text(tool.title) ?? text(meta) ?? text(input?.command) ?? text(input?.description) ?? tool.kind ?? none
}

// `/nix/…/bash -lc "cmd"` reads as `cmd`, only when the whole string is one shell -c command.
export function unwrapShell(command: string): string {
  const dbl = command.match(/^\S*\b(?:ba|z)?sh -l?c "((?:\\.|[^"\\])*)"$/)
  if (dbl) return dbl[1]!
  const single = command.match(/^\S*\b(?:ba|z)?sh -l?c '([^']*)'$/)
  return single ? single[1]! : command
}

// Inside a group, the group shows the images instead, so they stay visible while it is closed.
function ToolRow({ item, fresh, machine, sessionId }: { item: ThreadItem; fresh?: boolean; machine?: string; sessionId?: string }) {
  const tool = item.data as ToolPayload
  const mcp = mcpCall(tool)
  const shell = !mcp && (tool.kind === "execute" || tool.kind === "terminal")
  const command = shell ? (tool.command ?? (tool.rawInput as { command?: string } | undefined)?.command) : undefined
  const title = command ? unwrapShell(command) : toolTitle(tool)
  const status = tool.status ?? "pending"
  const Icon = mcp ? mcpIcon(mcp) : toolIcon(tool.kind)
  const hasDiff = (tool.content ?? []).some((block) => block.type === "diff" && (block.oldText || block.newText))
  const output = toolOutput(tool)
  const reason = mcp && status === "failed" ? failureReason(output, tool.rawOutput) : ""
  const terminalBody = shell ? output || (tool.kind === "terminal" ? command ?? "" : "") : ""
  const took = item.ts !== undefined && item.endTs !== undefined && item.endTs - item.ts >= 1000 ? formatDuration(item.endTs - item.ts) : ""
  const { open, onToggle } = useOpen(Boolean(fresh && (hasDiff || terminalBody)))
  const input = open && !hasDiff && !terminalBody ? shortInput(mcp ? mcp.args : tool.rawInput) : ""
  const row = (
    <details
      open={open}
      onToggle={onToggle}
      data-slot={hasDiff ? "tool-diff-block" : terminalBody ? "tool-terminal-block" : "tool-row"}
      data-tool-row=""
      className="text-(length:--fs-sm)"
    >
      <summary className="-mx-(--s2) flex min-h-(--row-h) cursor-default list-none items-center gap-(--s3) rounded-(--r-row) px-(--s2) marker:content-none hover:bg-(--hover) [&::-webkit-details-marker]:hidden">
        <Icon aria-hidden className="size-3.5 shrink-0 text-(--fg2)" />
        {mcp ? <McpTitle call={mcp} /> : <span className={`min-w-0 flex-1 truncate text-(--fg) ${shell ? "font-(family-name:--mono) text-(length:--fs-xs)" : ""}`}>{title}</span>}
        {reason && <span className="max-w-[40%] shrink-0 truncate text-(--danger-word)">{reason}</span>}
        {took && <span className="shrink-0 tabular-nums text-(length:--fs-xs) text-(--fg3)">{took}</span>}
        {statusGlyph(status)}
        <ChevronRight aria-hidden className="size-3.5 shrink-0 text-(--fg3) in-open:rotate-90" />
      </summary>
      {!open ? null : hasDiff ? (
        <Suspense><ToolDiffBody content={tool.content} toolId={tool.toolCallId} /></Suspense>
      ) : terminalBody ? (
        <ToolOutput slot="tool-terminal" text={terminalBody} command={command} />
      ) : (
        <div className="pl-(--s7)">
          {input && <p data-slot="tool-input" className="truncate font-(family-name:--mono) text-(length:--fs-xs) text-(--fg2)">{input}</p>}
          {output && <ToolOutput text={output} path={toolPath(tool.rawInput)} />}
        </div>
      )}
    </details>
  )
  return sessionId === undefined ? row : (
    <>
      {row}
      <Attachments slot="tool-attachments" items={toolAttachments(tool)} machine={machine ?? LOCAL} sessionId={sessionId} />
    </>
  )
}

// Codex sends only bold step headlines ("**Checking logs**"); Claude sends a sentence or two.
export function thoughtSteps(text: string): string[] {
  const lines = text.split("\n").map((line) => line.trim()).filter(Boolean)
  if (lines.length && lines.every((line) => /^\*\*(?:(?!\*\*).)+\*\*$/.test(line))) return lines.map((line) => line.slice(2, -2))
  return []
}

const listMarker = /^\s*(?:\d+[.)]|[-*+])\s+/

// A thought's one-line gist: its latest headline, or its first sentence.
export function thoughtGist(text: string): string {
  const steps = thoughtSteps(text)
  if (steps.length) return steps.at(-1)!
  const block: string[] = []
  for (const raw of text.split("\n")) {
    const line = raw.replace(/^\s*(?:>+\s*|#+\s*)/, "").trim()
    if (!line) {
      if (block.length) break
      continue
    }
    if (block.length && listMarker.test(line)) break
    block.push(block.length === 0 ? line.replace(listMarker, "") : line)
  }
  const plain = block.join(" ").replace(/[*_`]/g, "").replace(/\s+/g, " ").trim()
  return plain.match(/^.*?[.!?](?=\s|$)/)?.[0] ?? plain
}

// Thoughts read inline as quiet text between steps: a burst of headlines is one wrapped line,
// prose is clamped to two lines until toggled.
function ThoughtBlock({ text }: { text: string }) {
  const [more, setMore] = useState(false)
  const steps = thoughtSteps(text)
  return (
    <div data-slot="thought-block" className="py-(--s1) text-(length:--fs-sm) leading-snug text-(--fg3)">
      {steps.length ? <p>{steps.join(" · ")}</p> : (
        <button
          type="button"
          aria-expanded={more}
          onClick={() => setMore((open) => !open)}
          className="block w-full cursor-default rounded-(--r-xs) text-left focus-visible:outline-2 focus-visible:outline-(--accent) [&_.md]:text-(--fg3) [&_.md_blockquote]:text-(--fg3) [&_p]:text-(--fg3)"
        >
          <span className={cn("block", !more && "line-clamp-2 [&_p]:inline")}>
            <AgentMessageBody text={text} />
          </span>
        </button>
      )}
    </div>
  )
}

function formatDuration(ms: number): string {
  const s = Math.round(ms / 1000)
  return s < 60 ? `${s} s` : `${Math.floor(s / 60)} m ${s % 60} s`
}

function plural(n: number, word: string) {
  return `${n} ${word}${n === 1 ? "" : "s"}`
}

const exploring = new Set(["read", "search", "list"])
const simpleLookCommands = /^(?:rg|grep|cat|ls|head|tail|wc|tree|stat|file|pwd|printenv|which|uname|command -v|git (?:log|show|status|diff|grep|blame)|go (?:version|doc|list))(?:\s|$)/

function splitShellParts(command: string): string[] | null {
  const parts: string[] = []
  let start = 0
  let quote: "'" | '"' | "" = ""
  for (let i = 0; i < command.length; i++) {
    const ch = command[i]!
    if (quote === "'") {
      if (ch === "'") quote = ""
      continue
    }
    if (quote === '"') {
      if (ch === "\\") { i++; continue }
      if (ch === '"') quote = ""
      continue
    }
    if (ch === "'" || ch === '"') { quote = ch; continue }
    if (ch === "\\") { i++; continue }
    if (ch === ";" || ch === "|") {
      parts.push(command.slice(start, i))
      if (ch === "|" && command[i + 1] === "|") i++
      start = i + 1
    } else if (ch === "&" && command[i + 1] === "&") {
      parts.push(command.slice(start, i))
      i++
      start = i + 1
    }
  }
  if (quote) return null
  parts.push(command.slice(start))
  return parts.map((part) => part.trim()).filter(Boolean)
}

function shellWords(part: string): string[] {
  const words: string[] = []
  let current = ""
  let inWord = false
  let quote: "'" | '"' | "" = ""
  for (let i = 0; i < part.length; i++) {
    const ch = part[i]!
    if (quote === "'") {
      if (ch === "'") quote = ""
      else current += ch
      continue
    }
    if (quote === '"') {
      if (ch === "\\" && i + 1 < part.length) { current += part[++i]!; continue }
      if (ch === '"') quote = ""
      else current += ch
      continue
    }
    if (ch === "'" || ch === '"') { quote = ch; inWord = true; continue }
    if (/\s/.test(ch)) {
      if (inWord) { words.push(current); current = ""; inWord = false }
      continue
    }
    if (ch === "\\" && i + 1 < part.length) { current += part[++i]!; inWord = true; continue }
    current += ch
    inWord = true
  }
  if (inWord) words.push(current)
  return words
}

function isReadOnlyFind(words: string[]): boolean {
  return !words.slice(1).some((arg) => /^-(?:delete|exec|ok|fprint|fls)/.test(arg))
}

function isReadOnlySed(words: string[]): boolean {
  let quiet = false
  let hasExpression = false
  let endFlags = false
  const scripts: string[] = []
  for (let i = 1; i < words.length; i++) {
    const arg = words[i]!
    if (!endFlags && arg === "--") { endFlags = true; continue }
    if (!endFlags && arg.startsWith("--")) {
      if (arg === "--quiet" || arg === "--silent") { quiet = true; continue }
      if (arg.startsWith("--in-place") || arg.startsWith("--file")) return false
      if (arg === "--expression") {
        hasExpression = true
        if (++i >= words.length) return false
        scripts.push(words[i]!)
        continue
      }
      if (arg.startsWith("--expression=")) {
        hasExpression = true
        scripts.push(arg.slice("--expression=".length))
        continue
      }
      if (arg !== "--regexp-extended" && arg !== "--posix") return false
      continue
    }
    if (!endFlags && arg.startsWith("-") && arg.length > 1) {
      for (let j = 1; j < arg.length; j++) {
        const flag = arg[j]!
        if (flag === "i" || flag === "f") return false
        if (flag === "n") quiet = true
        else if (flag === "e") {
          hasExpression = true
          const rest = arg.slice(j + 1)
          if (rest) scripts.push(rest)
          else if (++i < words.length) scripts.push(words[i]!)
          else return false
          break
        } else if (flag !== "E" && flag !== "r") return false
      }
      continue
    }
    if (!hasExpression && scripts.length === 0) scripts.push(arg)
  }
  if (!quiet || scripts.length === 0) return false
  return scripts.every((script) => !/[wWe]/.test(script.replace(/(?:^|[;,{\s!])\/(?:\\.|[^/\\])*\//g, "")))
}

function isReadOnlyGitBranch(words: string[]): boolean {
  let listing = false
  for (const arg of words.slice(2)) {
    if (arg === "--list") { listing = true; continue }
    if (/^-(?:[arv]+|-all|-remotes|-verbose|-show-current)$/.test(arg)) continue
    if (listing && !arg.startsWith("-")) continue
    return false
  }
  return true
}

function isLookPart(rawPart: string): boolean {
  const bare = rawPart
    .replace(/'[^']*'|"((?:\\.|[^"\\])*)"/g, "Q")
    .replace(/\s\d?>\s?\/dev\/null(?=\s|$)/g, "")
    .replace(/\s2>&1(?=\s|$)/g, "")
    .trim()
  if (!bare || /[<>]/.test(bare)) return false
  if (/^cd \S+$/.test(bare) || simpleLookCommands.test(bare)) return true
  const words = shellWords(rawPart.replace(/\s\d?>\s?\/dev\/null(?=\s|$)/g, "").replace(/\s2>&1(?=\s|$)/g, ""))
  if (words[0] === "find") return isReadOnlyFind(words)
  if (words[0] === "sed") return isReadOnlySed(words)
  if (words[0] === "git" && words[1] === "branch") return isReadOnlyGitBranch(words)
  return false
}

// Reads, searches and listings, including shell commands that only look (no redirect, mutation or substitution).
export function explores(tool: ToolPayload): boolean {
  if (tool.status === "failed") return false
  if (mcpCall(tool)) return false
  if (exploring.has(tool.kind ?? "")) return true
  if (tool.kind !== "execute" && tool.kind !== "terminal") return false
  const command = unwrapShell(tool.command ?? (tool.rawInput as { command?: string } | undefined)?.command ?? "").trim()
  if (!command) return false
  // Reject newlines, backticks, command substitution, and lone `&` BEFORE stripping quotes.
  const withoutStdErr = command.replace(/\s2>&1(?=\s|$|[;|&])/g, "")
  if (/[\r\n`]|\$\(/.test(withoutStdErr) || withoutStdErr.replace(/&&/g, "").includes("&")) return false
  const parts = splitShellParts(command)
  return !!parts && parts.length > 0 && parts.every(isLookPart)
}

// "9 commands · 4 reads · 2 files changed": what the run did, by verb (each tool bucketed once).
export function activityCounts(tools: ThreadItem[]): string[] {
  let commands = 0
  let reads = 0
  let searches = 0
  let other = 0
  const changedFiles = new Set<string>()
  const servers = new Map<string, number>()
  for (const item of tools) {
    const tool = item.data as ToolPayload
    const call = mcpCall(tool)
    if (call) {
      servers.set(call.server, (servers.get(call.server) ?? 0) + 1)
      continue
    }
    if (tool.kind === "execute" || tool.kind === "terminal") {
      commands++
    } else if (tool.kind === "read") {
      reads++
    } else if (tool.kind === "search" || tool.kind === "list") {
      searches++
    } else if (tool.kind === "edit") {
      const changes = listDiffChanges([item])
      if (changes.length) for (const change of changes) changedFiles.add(change.path)
      else other++
    } else {
      other++
    }
  }
  return [
    ...(commands ? [plural(commands, "command")] : []),
    ...(reads ? [plural(reads, "read")] : []),
    ...(searches ? [plural(searches, "search").replace("searchs", "searches")] : []),
    ...(changedFiles.size ? [`${plural(changedFiles.size, "file")} changed`] : []),
    ...[...servers].map(([server, n]) => `${n} ${server} ${n === 1 ? "call" : "calls"}`),
    ...(other > 0 ? [plural(other, "tool")] : []),
  ]
}

function base(path: string) {
  return path.split("/").filter(Boolean).at(-1) ?? path
}

// Two or more reads, searches and listings in a row fold into one line naming what was looked at.
function ExploreRow({ tools, fresh }: { tools: ThreadItem[]; fresh: (item: ThreadItem) => boolean }) {
  const { open, onToggle } = useOpen(false)
  const names = [...new Set(tools.map((item) => {
    const tool = item.data as ToolPayload
    const path = toolPath(tool.rawInput)
    if (path) return base(path)
    const command = tool.kind === "execute" || tool.kind === "terminal" ? unwrapShell(tool.command ?? (tool.rawInput as { command?: string } | undefined)?.command ?? "") : ""
    const files = command.match(/[\w./-]+\.[a-z]{1,5}\b/g)
    if (command) return files ? files.map(base).join(", ") : command.split(" ")[0]!
    return toolTitle(tool).replace(/^(Read file|Search for|List files in) /i, "").replace(/^'|'$/g, "")
  }))]
  return (
    <details data-slot="explore-row" open={open} onToggle={onToggle} className="text-(length:--fs-sm)">
      <summary className="-mx-(--s2) flex min-h-(--row-h) cursor-default list-none items-center gap-(--s3) rounded-(--r-row) px-(--s2) marker:content-none hover:bg-(--hover) [&::-webkit-details-marker]:hidden">
        <BookOpen aria-hidden className="size-3.5 shrink-0 text-(--fg2)" />
        <span className="shrink-0 text-(--fg)">Explored</span>
        <span className="min-w-0 flex-1 truncate text-(--fg2)"><span className="sr-only"> </span>{names.join(", ")}</span>
        <ChevronRight aria-hidden className="size-3.5 shrink-0 text-(--fg3) in-open:rotate-90" />
      </summary>
      {open && <div className="flex flex-col pl-(--s5)">{tools.map((item, index) => <ToolRow key={item.seq ?? index} item={item} fresh={fresh(item)} />)}</div>}
    </details>
  )
}

// One row for everything the agent did between two messages: its latest thought as the title,
// what it ran as the detail. Opened, it reads top to bottom as thoughts and steps in order.
function ActivityGroup({ items, live, end, fresh, machine, sessionId }: { items: ThreadItem[]; live?: boolean; end?: number; fresh: (item: ThreadItem) => boolean; machine: string; sessionId: string }) {
  const tools = items.filter((item) => item.kind === "tool")
  const thoughts = items.filter((item) => item.kind === "agent_thought_chunk")
  const failed = tools.filter((item) => (item.data as ToolPayload).status === "failed").length
  // ts is only known for streamed items; omit the time rather than guess.
  const stamps = items.flatMap((item) => item.ts === undefined ? [] : [item.ts, item.endTs ?? item.ts])
  if (end !== undefined && stamps.length) stamps.push(end)
  const span = stamps.length ? Math.max(...stamps) - Math.min(...stamps) : 0
  const { open, onToggle } = useOpen(false)
  const gist = thoughts.length ? thoughtGist(chunkText(thoughts.at(-1)!.data)) : ""
  const counts = activityCounts(tools)
  const title = gist || (tools.length ? "Worked" : "Thought")
  const detail = [...counts, ...(span >= 1000 ? [clock(span)] : [])].join(" · ")
  // Runs of exploration fold together; built only while open.
  const rows: (ThreadItem | ThreadItem[])[] = []
  if (open) {
    for (const item of items) {
      const looks = item.kind === "tool" && explores(item.data as ToolPayload)
      const previous = rows.at(-1)
      if (looks && Array.isArray(previous)) previous.push(item)
      else rows.push(looks ? [item] : item)
    }
  }
  return (
    <>
      <details data-slot="tool-group" open={open} onToggle={onToggle} className="text-(length:--fs-sm)">
        <summary className="-mx-(--s2) flex min-h-(--row-h) cursor-default list-none items-center gap-(--s3) rounded-(--r-row) px-(--s2) marker:content-none hover:bg-(--hover) [&::-webkit-details-marker]:hidden">
          <ChevronRight aria-hidden className="size-3.5 shrink-0 text-(--fg3) in-open:rotate-90" />
          <span className={cn("min-w-0 shrink truncate", live ? "shimmer" : "text-(--fg)")}>{title}</span>
          {detail && <span className="min-w-0 flex-1 truncate tabular-nums text-(--fg3)"><span className="sr-only"> · </span>{detail}</span>}
          {!detail && <span className="flex-1" />}
          {failed > 0 && <span className="shrink-0 text-(--danger-word)"><span className="sr-only"> · </span>{failed} failed</span>}
        </summary>
        {open && (
          <div className="ml-(--s2) flex flex-col border-l-[0.5px] border-(--sep) pl-(--s4)">
            {rows.map((row, index) => Array.isArray(row)
              ? row.length > 1 ? <ExploreRow key={row[0]!.seq ?? index} tools={row} fresh={fresh} /> : <ToolRow key={row[0]!.seq ?? index} item={row[0]!} fresh={fresh(row[0]!)} />
              : row.kind === "agent_thought_chunk" ? <ThoughtBlock key={row.seq ?? index} text={chunkText(row.data)} />
              : <ToolRow key={row.seq ?? index} item={row} fresh={fresh(row)} />)}
          </div>
        )}
      </details>
      <Attachments slot="tool-attachments" items={tools.flatMap((item) => toolAttachments(item.data as ToolPayload))} machine={machine} sessionId={sessionId} />
    </>
  )
}

function MessageContextMenu({
  text,
  children,
  onQuote,
  onFork,
  onRetry,
}: {
  text: string
  children: ReactNode
  onQuote?: (text: string) => void
  onFork?: () => void
  onRetry?: () => void
}) {
  if (!text.trim()) return children
  return (
    <RowContextMenu>
      <RowContextMenuTrigger className="min-w-0 select-text">{children}</RowContextMenuTrigger>
      <RowContextMenuContent>
        <RowContextMenuItem icon={<Copy aria-hidden />} onClick={() => void copyText(text)}>
          Copy
        </RowContextMenuItem>
        {onQuote && (
          <RowContextMenuItem icon={<Quote aria-hidden />} onClick={() => onQuote(text)}>
            Quote in reply
          </RowContextMenuItem>
        )}
        {onFork && (
          <RowContextMenuItem icon={<GitFork aria-hidden />} onClick={onFork}>
            Fork conversation
          </RowContextMenuItem>
        )}
        {onRetry && (
          <RowContextMenuItem icon={<RotateCcw aria-hidden />} onClick={onRetry}>
            Retry last message
          </RowContextMenuItem>
        )}
      </RowContextMenuContent>
    </RowContextMenu>
  )
}

function AgentBlock({
  text,
  last,
  streaming,
  meta,
  onQuote,
  onFork,
  onRetry,
}: {
  text: string
  last?: boolean
  streaming?: boolean
  meta?: string
  onQuote?: (text: string) => void
  onFork?: () => void
  onRetry?: () => void
}) {
  const actions = text && !streaming
  const [copied, copy] = useCopied()
  return (
    <div data-slot="agent-message" className="group relative">
      <MessageContextMenu text={text} onQuote={onQuote} onFork={onFork} onRetry={onRetry}>
        <AgentMessageBody text={text} streaming={streaming} />
      </MessageContextMenu>
      {actions && (
        // Always shown under the last answer; on hover or focus for older ones.
        <div
          data-slot="answer-actions"
          className={`mt-(--s1) flex flex-wrap items-center gap-(--s1) transition-opacity ${last ? "" : "min-[760px]:opacity-0 min-[760px]:group-hover:opacity-100 min-[760px]:group-focus-within:opacity-100"}`}
        >
          <Button type="button" size="icon" variant="ghost" className="size-(--ctl-h-sm)" aria-label={copied ? "Copied" : "Copy message"} title={copied ? "Copied" : "Copy"} onClick={() => copy(text)}>
            {copied ? <Check aria-hidden /> : <Copy aria-hidden />}
          </Button>
          {onRetry && (
            <Button type="button" size="icon" variant="ghost" className="size-(--ctl-h-sm)" aria-label="Retry last message" title="Retry" onClick={onRetry}>
              <RotateCcw aria-hidden />
            </Button>
          )}
          {onFork && (
            <Button type="button" size="icon" variant="ghost" className="size-(--ctl-h-sm)" aria-label="Fork conversation" title="Fork from here" onClick={onFork}>
              <GitFork aria-hidden />
            </Button>
          )}
          {onQuote && (
            <Button type="button" size="icon" variant="ghost" className="size-(--ctl-h-sm)" aria-label="Quote in reply" title="Quote" onClick={() => onQuote(text)}>
              <Quote aria-hidden />
            </Button>
          )}
          {last && meta && <span data-slot="answer-meta" className="ms-(--s2) text-(length:--fs-xs) tabular-nums text-(--fg3)">{meta}</span>}
        </div>
      )}
    </div>
  )
}

function CompactionBlock({ item }: { item: ThreadItem }) {
  const compaction = item.data as CompactionPayload
  const sizes = compaction.before !== undefined && compaction.after !== undefined ? ` · ${compactCount(compaction.before)} → ${compactCount(compaction.after)}` : ""
  const label = compaction.status === "in_progress" ? "Compacting context" : compaction.error ? "Context compaction failed" : `Context compacted${sizes}`
  const rule = <span aria-hidden className="h-px flex-1 bg-(--sep)" />
  return (
    <details data-slot="conversation-compaction" aria-label="Context compaction" className="text-(length:--fs-xs) text-(--fg3)">
      <summary className="flex cursor-default list-none items-center gap-(--s3) marker:content-none [&::-webkit-details-marker]:hidden">
        {rule}
        <span className={compaction.status === "in_progress" ? "shimmer" : undefined}>{label}</span>
        {rule}
      </summary>
      {compaction.summary && <p className="mt-(--s2) whitespace-pre-wrap text-(--fg2)">{compaction.summary}</p>}
      {compaction.error && <p role="alert" className="mt-(--s2) text-(--danger-word)">{compaction.error}</p>}
    </details>
  )
}


function ThreadItemView({
  item,
  machine,
  sessionId,
  readOnly,
  agent,
  onQuote,
  onFork,
  onRetry,
  onSignedIn,
  onOpenChat,
  last,
  streaming,
  fresh,
  meta,
}: {
  item: ThreadItem
  machine: string
  sessionId: string
  readOnly?: boolean
  agent?: string
  onQuote?: (text: string) => void
  onFork?: () => void
  onRetry?: () => void
  onSignedIn?: () => void
  onOpenChat?: (from: PromptFrom) => void
  last?: boolean
  streaming?: boolean
  fresh?: boolean
  meta?: string
}) {
  switch (item.kind) {
    case "user_message_chunk":
      return <UserMessageBody data={item.data} machine={machine} sessionId={sessionId} onQuote={onQuote} onFork={onFork} onOpenChat={onOpenChat} />
    case "agent_message_chunk": {
      const attachments = chunkAttachments(item.data as TextChunkPayload)
      const text = chunkText(item.data)
      if (!attachments.length) return <AgentBlock text={text} last={last} streaming={streaming} meta={meta} onQuote={onQuote} onFork={onFork} onRetry={onRetry} />
      return (
        <div className="flex flex-col gap-(--s2)">
          {text && <AgentBlock text={text} last={last} streaming={streaming} meta={meta} onQuote={onQuote} onFork={onFork} onRetry={onRetry} />}
          <Attachments slot="agent-attachments" items={attachments} machine={machine} sessionId={sessionId} />
        </div>
      )
    }
    case "rich_block":
      return <RichCard data={item.data} machine={machine} sessionId={sessionId} />
    case "agent_thought_chunk":
      return <ThoughtBlock text={chunkText(item.data)} />
    case "tool":
      return <ToolRow item={item} fresh={fresh} machine={machine} sessionId={sessionId} />
    case "permission":
      return <fieldset disabled={readOnly} className="min-w-0"><PermissionCard item={item} machine={machine} sessionId={sessionId} readOnly={readOnly} agent={agent} /></fieldset>
    case "elicitation":
      return <fieldset disabled={readOnly} className="min-w-0"><ElicitationCard item={item} machine={machine} sessionId={sessionId} /></fieldset>
    case "compaction":
      return <CompactionBlock item={item} />
    case "divider": {
      const notice = item.data as NoticePayload
      if (notice.harness && onSignedIn) {
        return <SignInCard machine={machine} harness={notice.harness} methods={Array.isArray(notice.methods) ? notice.methods : undefined} reason={notice.description ?? undefined} onSignedIn={onSignedIn} />
      }
      return (
        <div data-slot="conversation-divider" className="flex flex-col gap-(--s1) text-center text-(length:--fs-xs) text-(--fg3)">
          {notice.title && <p className="font-medium text-(--fg2)">{notice.title}</p>}
          {notice.description && <p>{noticeText(notice.description)}</p>}
        </div>
      )
    }
    case "goal":
      return <GoalLine goal={item.data as GoalState} />
    case "task":
      // A subagent's brief from its parent.
      return (
        <div data-slot="subagent-task" className="flex flex-col gap-(--s2)">
          <p aria-hidden className="text-(length:--fs-xs) font-medium text-(--fg3)">Task</p>
          <div className="rounded-(--r-sheet) bg-(--track) px-(--s4) py-(--s3)"><AgentMessageBody text={chunkText(item.data)} /></div>
        </div>
      )
    case "plan":
      return null
    default:
      return null
  }
}

// One line per goal state change: set, paused, met, blocked, limited, cleared.
function GoalLine({ goal }: { goal: GoalState }) {
  const elapsed = goal.active_ms ? ` in ${clock(goal.active_ms)}` : ""
  const turns = goal.turns ? ` · ${goal.turns} ${goal.turns === 1 ? "turn" : "turns"}` : ""
  const text =
    goal.state === "active" ? `Goal: ${goal.objective}` :
    goal.state === "met" ? `Goal met${elapsed}${turns}` :
    `${goalStateWords[goal.state]}${goal.state === "paused" || goal.state === "cleared" ? "" : elapsed}${turns}`
  return (
    <div data-slot="goal-line" className="flex items-center gap-(--s3) text-(length:--fs-xs) text-(--fg3)">
      <Flag aria-hidden className="size-3.5 shrink-0" />
      <span className="min-w-0 truncate">{text}</span>
      {goal.last_reason && goal.state !== "active" && <span className="min-w-0 truncate text-(--fg2)">· {goal.last_reason}</span>}
    </div>
  )
}

type Entry = ThreadItem | ThreadItem[] | { cards: { subagent: Subagent; item: ThreadItem }[] }

// One work log per run of thoughts and tool calls; user, agent, permission and compaction items end the
// run. A tool that spawned a subagent (and a subagent seen before its tool) becomes a card, and
// cards side by side share one "in parallel" heading.
function groupItems(items: ThreadItem[], subagents: Record<string, Subagent>): Entry[] {
  const byTool = new Map<string, Subagent>()
  for (const subagent of Object.values(subagents)) if (subagent.toolCallId) byTool.set(subagent.toolCallId, subagent)
  const toolCallIds = new Set(items.flatMap((item) => item.kind === "tool" ? [(item.data as ToolPayload).toolCallId] : []))
  const out: Entry[] = []
  let batch: ThreadItem[] = []
  const flush = () => {
    if (batch.length === 0) return
    out.push(batch.length === 1 && batch[0]!.kind === "tool" ? batch[0]! : [...batch])
    batch = []
  }
  const card = (subagent: Subagent, item: ThreadItem) => {
    flush()
    const previous = out.at(-1)
    if (previous && "cards" in previous) previous.cards.push({ subagent, item })
    else out.push({ cards: [{ subagent, item }] })
  }
  for (const item of items) {
    if (item.kind === "agent_thought_chunk") batch.push(item)
    else if (item.kind === "tool") {
      const spawned = byTool.get((item.data as ToolPayload).toolCallId ?? "")
      if (spawned) card(spawned, item)
      else batch.push(item)
    } else if (item.kind === "subagent") {
      const subagent = subagents[(item.data as { id: string }).id]
      // A linked tool call shows this subagent's card in its place.
      if (subagent && !(subagent.toolCallId && toolCallIds.has(subagent.toolCallId))) card(subagent, item)
    } else if (item.kind !== "plan") {
      flush()
      out.push(item)
    }
  }
  flush()
  return out
}

// What the agent is doing right now, with how long it has been at it.
export function livePhase(items: ThreadItem[], subagents: Record<string, Subagent>): { label: string; since?: number } {
  const running = Object.values(subagents).filter(subagentRunning).length
  if (running > 0) return { label: `${running} ${running === 1 ? "subagent" : "subagents"} running` }
  const last = items.findLast((item) => item.kind !== "plan" && item.kind !== "goal")
  if (last?.kind === "agent_thought_chunk") return { label: "Thinking", since: last.ts }
  const tool = items.findLast((item) => item.kind === "tool" && (item.data as ToolPayload).status === "in_progress")
  if (tool) {
    const data = tool.data as ToolPayload
    const command = data.command ?? (data.rawInput as { command?: string } | undefined)?.command
    return { label: `Running ${command ?? toolTitle(data, "a tool")}`, since: tool.ts }
  }
  if (last?.kind === "agent_message_chunk") return { label: "Writing", since: last.ts }
  return { label: "Working", since: last?.ts }
}

function LiveLine({ items, subagents, turnStarted }: { items: ThreadItem[]; subagents: Record<string, Subagent>; turnStarted?: number }) {
  const now = useNow(true)
  const phase = livePhase(items, subagents)
  const since = phase.since ?? turnStarted
  return (
    <p data-slot="thread-working" className="flex min-w-0 items-center gap-(--s3) text-(length:--fs-sm) text-(--fg3)">
      <Spinner />
      <span className="min-w-0 truncate">{phase.label}</span>
      {since !== undefined && <span className="shrink-0 tabular-nums">{clock(Math.max(0, now - since))}</span>}
    </p>
  )
}

// "Worked 1m 52s · 14.2k tokens · $0.09", plus the time of the answer.
export function turnMeta(turn: ThreadState["last_turn"], ts?: number): string {
  const parts = [
    turn ? `Worked ${clock(turn.ms)}` : undefined,
    turn?.tokens ? `${compactCount(turn.tokens)} tokens` : undefined,
    turn?.cost ? `$${turn.cost.toFixed(2)}` : undefined,
    ts !== undefined ? new Date(ts).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }) : undefined,
  ]
  return parts.filter(Boolean).join(" · ")
}

const noSubagents: Record<string, Subagent> = {}

export function ThreadView({
  items,
  machine,
  sessionId,
  onQuote,
  onFork,
  onRetry,
  readOnly,
  agent,
  working,
  subagents = noSubagents,
  lastTurn,
  turnStarted,
  animateAfter,
  onOpenSubagent,
  onStopSubagent,
  onSignedIn,
  onOpenChat,
  authHarness,
  scrollRef,
  pinRequests,
}: {
  readOnly?: boolean
  agent?: string
  working?: boolean
  items: ThreadItem[]
  machine: string
  sessionId: string
  onQuote?: (text: string) => void
  onFork?: () => void
  onRetry?: () => void
  subagents?: Record<string, Subagent>
  lastTurn?: ThreadState["last_turn"]
  turnStarted?: number
  /** Items with a newer seq arrived after the first paint and animate in. */
  animateAfter?: number
  onOpenSubagent?: (id: string) => void
  onStopSubagent?: (id: string) => void
  /** Starts the chat once its agent is signed in; only the latest sign-in notice offers it. */
  onSignedIn?: () => void
  /** Opens the chat that sent a message with ask or reply. */
  onOpenChat?: (from: PromptFrom) => void
  authHarness?: string
  scrollRef?: RefObject<HTMLDivElement | null>
  /** Open requests are answered from the card pinned above the composer, so a busy thread cannot bury them. */
  pinRequests?: boolean
}) {
  const retry = latestUserPrompt(items) && onRetry ? onRetry : undefined
  const lastAgent = items.findLast((item) => item.kind === "agent_message_chunk")
  const compacted = items.some((item) => item.kind === "compaction")
  const signInIndex = items.findLastIndex((item) => {
    if (item.kind !== "divider") return false
    const notice = item.data as NoticePayload
    return notice.code === "auth_required" || (notice.title === "The agent could not run this prompt" && notice.description?.includes("Authentication required"))
  })
  // Agent output after the notice proves the sign-in worked, so the card retires.
  const signIn = signInIndex >= 0 && !items.slice(signInIndex + 1).some((item) => item.kind === "agent_message_chunk" || item.kind === "tool")
    ? items[signInIndex]
    : undefined
  const fresh = (item: ThreadItem) => animateAfter !== undefined && item.seq !== undefined && item.seq > animateAfter
  const permission = (item: ThreadItem) => (
    <fieldset disabled={readOnly} className="min-w-0">
      <PermissionCard item={item} machine={machine} sessionId={sessionId} readOnly={readOnly} agent={agent} />
    </fieldset>
  )
  const entries = groupItems(items, subagents)
  const virtual = scrollRef !== undefined && entries.length > 80
  const listRef = useRef<HTMLDivElement>(null)
  const [scrollMargin, setScrollMargin] = useState(0)
  const virtualizer = useRef<Virtualizer<HTMLDivElement, HTMLDivElement> | null>(null)
  const previousOffset = useRef<number | null>(null)
  const [layout, setLayout] = useState<{ rows: VirtualItem[]; height: number }>({ rows: [], height: 0 })
  useLayoutEffect(() => {
    const options = {
      enabled: false, count: 0,
      getScrollElement: () => null,
      estimateSize: () => 96,
      observeElementRect,
      observeElementOffset,
      scrollToFn: elementScroll,
      onChange: (instance: Virtualizer<HTMLDivElement, HTMLDivElement>) => {
        const rows = instance.getVirtualItems()
        const height = instance.getTotalSize()
        setLayout((current) => {
          if (current.height !== height || current.rows.length !== rows.length) return { rows, height }
          return rows.every((row, index) => {
            const previous = current.rows[index]!
            return row.key === previous.key && row.index === previous.index && row.start === previous.start && row.size === previous.size
          }) ? current : { rows, height }
        })
      },
    }
    const instance = new Virtualizer<HTMLDivElement, HTMLDivElement>(options)
    virtualizer.current = instance
    const dispose = instance._didMount()
    return () => {
      // A reader at the end returns to the end, not to where the end was when the chat was hidden.
      if (instance.options.enabled) previousOffset.current = instance.isAtEnd(8) ? null : instance.scrollOffset
      dispose()
      virtualizer.current = null
    }
  }, [])
  useLayoutEffect(() => {
    const instance = virtualizer.current
    if (!instance) return
    instance.setOptions({
      ...instance.options,
      enabled: virtual,
      count: entries.length,
      getScrollElement: () => scrollRef?.current ?? null,
      overscan: 8,
      anchorTo: "end",
      followOnAppend: true,
      scrollMargin,
      getItemKey: (index) => {
        const entry = entries[index]
        if (!entry) return `pending-${index}`
        const item = Array.isArray(entry) ? entry[0] : "cards" in entry ? entry.cards[0]?.item : entry
        return item?.seq ?? `pending-${index}`
      },
    })
    instance._willUpdate()
    if (virtual) instance.options.onChange?.(instance, false)
  }, [entries, scrollMargin, scrollRef, virtual])
  useLayoutEffect(() => {
    const list = listRef.current
    const viewport = scrollRef?.current
    if (!virtual || !list || !viewport) return
    const measure = () => setScrollMargin(list.getBoundingClientRect().top - viewport.getBoundingClientRect().top + viewport.scrollTop)
    measure()
    const before = list.parentElement?.previousElementSibling
    const observer = new ResizeObserver(measure)
    observer.observe(viewport)
    if (before) observer.observe(before)
    return () => observer.disconnect()
  }, [scrollRef, virtual])
  const canAnchor = virtual && layout.height > 0
  useLayoutEffect(() => {
    if (!canAnchor) return
    const instance = virtualizer.current
    if (previousOffset.current === null) instance?.scrollToEnd()
    else instance?.scrollToOffset(previousOffset.current)
  }, [canAnchor])
  if (items.length === 0) {
    return working ? <LiveLine items={items} subagents={subagents} turnStarted={turnStarted} /> : <p className="py-(--s7) text-center text-(length:--fs-sm) text-(--fg3)">No messages yet.</p>
  }

  // When an agent reply follows a run of work, the run lasted until that reply began.
  const nextReplyTs = new Map<ThreadItem, number | undefined>()
  for (let i = 0; i < items.length - 1; i++) {
    const next = items[i + 1]!
    if (next.kind === "agent_message_chunk") nextReplyTs.set(items[i]!, next.ts)
  }
  const renderEntry = (entry: (typeof entries)[number], index: number) => {
    if (Array.isArray(entry)) {
      return (
        <div key={entry[0]!.seq ?? index} className={cn("thread-item", fresh(entry.at(-1)!) && "tool-enter")}>
          <ActivityGroup items={entry} live={working && index === entries.length - 1} end={nextReplyTs.get(entry.at(-1)!)} fresh={fresh} machine={machine} sessionId={sessionId} />
        </div>
      )
    }
    if ("cards" in entry) {
      const running = entry.cards.filter(({ subagent }) => subagentRunning(subagent)).length
      return (
        <div key={entry.cards[0]!.item.seq ?? index} data-slot="subagent-group" className={`flex flex-col gap-(--s2) ${fresh(entry.cards[0]!.item) ? "thread-enter" : ""}`}>
          {entry.cards.length > 1 && (
            <p className="text-(length:--fs-xs) text-(--fg3)">
              {running > 0 ? `Running ${entry.cards.length} subagents in parallel` : `${entry.cards.length} subagents`}
            </p>
          )}
          {entry.cards.map(({ subagent, item }) => (
            <SubagentCard
              key={subagent.id}
              subagent={subagent}
              toolTitle={item.kind === "tool" ? (item.data as ToolPayload).title : undefined}
              renderPermission={permission}
              onOpen={onOpenSubagent ? () => onOpenSubagent(subagent.id) : undefined}
              onStop={!readOnly && onStopSubagent && !subagent.id.startsWith("tool:") ? () => onStopSubagent(subagent.id) : undefined}
            />
          ))}
        </div>
      )
    }
    // The compaction block is the one divider for a compaction; the notice would repeat it.
    if (compacted && entry.kind === "divider" && (entry.data as NoticePayload).title === "Context compacted") return null
    if (pinRequests && isOpenRequest(entry)) {
      return (
        <p key={entry.seq ?? index} data-slot="pinned-request-marker" className="thread-item flex min-h-(--row-h) items-center gap-(--s3) text-(length:--fs-sm) text-(--warn-word)">
          <Lock aria-hidden className="size-3.5 shrink-0" />
          <span>Waiting for you · answer above the message box</span>
        </p>
      )
    }
    const isLastAgent = entry === lastAgent
    return (
      <div key={entry.seq ?? index} className={cn("thread-item", fresh(entry) && (entry.kind === "tool" ? "tool-enter" : "thread-enter"))}>
        <ThreadItemView
          item={entry === signIn && authHarness && !(entry.data as NoticePayload).harness
            ? { ...entry, data: { ...(entry.data as NoticePayload), harness: authHarness, description: "Sign in again to continue this chat." } }
            : entry}
          machine={machine}
          sessionId={sessionId}
          readOnly={readOnly}
          agent={agent}
          onQuote={onQuote}
          onFork={onFork}
          onRetry={isLastAgent ? retry : undefined}
          onSignedIn={entry === signIn ? onSignedIn : undefined}
          onOpenChat={onOpenChat}
          last={isLastAgent && !working}
          streaming={isLastAgent && working}
          fresh={fresh(entry)}
          meta={isLastAgent && !working ? turnMeta(lastTurn, entry.ts) : undefined}
        />
      </div>
    )
  }
  return (
    <div className="flex flex-col gap-(--s4)">
      {virtual ? (
        <div ref={listRef} data-thread-virtual className="relative" style={{ height: layout.height }}>
          {layout.rows.filter((row) => entries[row.index] !== undefined).map((row) => (
            <div
              key={row.key}
              data-index={row.index}
              ref={(node) => virtualizer.current?.measureElement(node)}
              className="absolute inset-x-0 top-0 pb-(--s4)"
              style={{ transform: `translateY(${row.start - scrollMargin}px)` }}
            >
              {renderEntry(entries[row.index]!, row.index)}
            </div>
          ))}
        </div>
      ) : entries.map(renderEntry)}
      {working && <LiveLine items={items} subagents={subagents} turnStarted={turnStarted} />}
    </div>
  )
}
