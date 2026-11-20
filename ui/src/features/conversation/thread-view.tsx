import { useState, type ReactNode } from "react"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import { BookOpen, ChevronRight, Copy, Flag, Folder, GitFork, Globe, Pencil, Lock, Quote, RotateCcw, Search, Sparkles, SquareTerminal, Wrench } from "lucide-react"
import { RowContextMenu, RowContextMenuContent, RowContextMenuItem, RowContextMenuTrigger } from "@/components/kit/overlays"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Spinner } from "@/components/kit/feedback"
import { api, LOCAL } from "@/lib/api"
import { copyText } from "@/lib/hud"
import { SignInCard } from "@/features/agents/sign-in"
import { cn } from "@/lib/utils"
import type { GoalState } from "@/lib/api-types"
import { clock, goalStateWords } from "@/lib/goal"
import { useNow } from "@/lib/use-now"
import type { CompactionPayload, ElicitationField, ElicitationPayload, NoticePayload, PermissionPayload, Subagent, TextChunkPayload, ThreadItem, ThreadState, ToolPayload, UserMessagePayload } from "./model"
import { compactCount, SubagentCard, subagentRunning } from "./subagent-card"
import { AgentMessageBody } from "./agent-message"
import { ToolDiffBody } from "./tool-diff"
import { ToolOutput } from "./tool-output"
import { QuickLook } from "./quick-look"
import { conversationErrorMessage } from "./queries"
import { listDiffChanges } from "./diff-summary"

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
}: {
  data: unknown
  machine: string
  sessionId: string
  onQuote?: (text: string) => void
  onFork?: () => void
}) {
  const body = data as UserMessagePayload
  const text = body.text?.text ?? ""
  const attachments = body.images ?? [
    ...(body.image ? [body.image] : []),
    ...(body.resource_link ? [body.resource_link] : []),
  ]
  const previews = attachments.flatMap((image) => {
    const src = userImageSrc(machine, sessionId, image.blob)
    return src ? [{ image, src }] : []
  })
  const [quickLook, setQuickLook] = useState<number | null>(null)
  const quickLookImage = quickLook === null ? undefined : previews[quickLook]
  return (
    <>
      <div className="flex w-full flex-col items-start gap-(--s2)">
        <p aria-hidden className="text-(length:--fs-xs) font-medium text-(--fg3)">You</p>
        {previews.length > 0 && (
          <div data-slot="user-attachments" className="flex max-w-full flex-wrap gap-(--s3)">
            {previews.map(({ image, src }, index) => {
              const label = image.name || `image ${index + 1}`
              return (
                <button
                  key={`${image.blob}-${index}`}
                  type="button"
                  aria-label={`Preview ${label}`}
                  className="rounded-(--r-pop) border-[0.5px] border-(--sep) p-0"
                  onClick={() => setQuickLook(index)}
                >
                  <img
                    src={src}
                    alt={image.name ?? ""}
                    width={image.width}
                    height={image.height}
                    className="max-h-48 max-w-full rounded-(--r-pop) object-contain"
                  />
                </button>
              )
            })}
          </div>
        )}
        {text ? (
          <MessageContextMenu text={text} onQuote={onQuote} onFork={onFork}>
            <p data-slot="user-bubble" className="w-full rounded-(--r-sheet) bg-(--track) px-(--s4) py-(--s3) text-(length:--fs) text-(--fg) whitespace-pre-wrap [overflow-wrap:anywhere]">
              {text}
            </p>
          </MessageContextMenu>
        ) : null}
      </div>
      {quickLookImage && (
        <QuickLook
          src={quickLookImage.src}
          label={quickLookImage.image.name || "Attached image"}
          onClose={() => setQuickLook(null)}
        />
      )}
    </>
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

// Shell output: the fixture's `output`, the adapter's raw string, or its text content without the console fence.
export function toolOutput(tool: ToolPayload): string {
  if (tool.output) return tool.output.trim()
  if (typeof tool.rawOutput === "string") return tool.rawOutput.trim()
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

function ToolRow({ item }: { item: ThreadItem }) {
  const tool = item.data as ToolPayload
  const shell = tool.kind === "execute" || tool.kind === "terminal"
  const command = shell ? (tool.command ?? (tool.rawInput as { command?: string } | undefined)?.command) : undefined
  const title = command ?? tool.title ?? tool.kind ?? "Tool"
  const status = tool.status ?? "pending"
  const Icon = toolIcon(tool.kind)
  const diff = <ToolDiffBody content={tool.content} toolId={tool.toolCallId} />
  const hasDiff = (tool.content ?? []).some((block) => block.type === "diff" && (block.oldText || block.newText))
  const output = toolOutput(tool)
  const terminalBody = shell ? output || (tool.kind === "terminal" ? command ?? "" : "") : ""
  const input = shortInput(tool.rawInput)
  const took = item.ts !== undefined && item.endTs !== undefined && item.endTs - item.ts >= 1000 ? formatDuration(item.endTs - item.ts) : ""
  const { open, onToggle } = useOpen(hasDiff || Boolean(terminalBody))
  return (
    <details
      open={open}
      onToggle={onToggle}
      data-slot={hasDiff ? "tool-diff-block" : terminalBody ? "tool-terminal-block" : "tool-row"}
      data-tool-row=""
      className="text-(length:--fs-sm)"
    >
      <summary className="-mx-(--s2) flex min-h-(--row-h) cursor-default list-none items-center gap-(--s3) rounded-(--r-row) px-(--s2) marker:content-none hover:bg-(--hover) [&::-webkit-details-marker]:hidden">
        <Icon aria-hidden className="size-3.5 shrink-0 text-(--fg2)" />
        <span className={`min-w-0 flex-1 truncate text-(--fg) ${shell ? "font-(family-name:--mono) text-(length:--fs-xs)" : ""}`}>{title}</span>
        {took && <span className="shrink-0 tabular-nums text-(length:--fs-xs) text-(--fg3)">{took}</span>}
        {statusGlyph(status)}
        <ChevronRight aria-hidden className="size-3.5 shrink-0 text-(--fg3) in-open:rotate-90" />
      </summary>
      {!open ? null : hasDiff ? (
        diff
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
}

// Thoughts are markdown like replies, dimmed, and rendered only once opened.
function ThoughtBlock({ text, ms }: { text: string; ms?: number }) {
  const { open, onToggle } = useOpen(false)
  return (
    <details data-slot="thought-block" open={open} onToggle={onToggle} className="text-(length:--fs-sm)">
      <summary className="-mx-(--s2) flex min-h-(--row-h) cursor-default list-none items-center gap-(--s3) rounded-(--r-row) px-(--s2) text-(--fg2) marker:content-none hover:bg-(--hover) [&::-webkit-details-marker]:hidden">
        <ChevronRight aria-hidden className="size-3.5 shrink-0 text-(--fg3) in-open:rotate-90" />
        <Sparkles aria-hidden className="size-3.5 shrink-0" />
        {ms !== undefined && ms >= 1000 ? `Thought for ${clock(ms)}` : "Thought"}
      </summary>
      {open && (
        <div className="ml-(--s3) border-l-[0.5px] border-(--sep) py-(--s2) pl-(--s4) opacity-80">
          <AgentMessageBody text={text} />
        </div>
      )}
    </details>
  )
}

function formatDuration(ms: number): string {
  const s = Math.round(ms / 1000)
  return s < 60 ? `${s} s` : `${Math.floor(s / 60)} m ${s % 60} s`
}

function plural(n: number, word: string) {
  return `${n} ${word}${n === 1 ? "" : "s"}`
}

function ToolGroup({ tools }: { tools: ThreadItem[] }) {
  const failed = tools.filter((item) => (item.data as ToolPayload).status === "failed").length
  const files = new Set(listDiffChanges(tools).map((change) => change.path)).size
  // ts is only known for streamed items; omit the time rather than guess.
  const stamps = tools.flatMap((item) => item.ts === undefined ? [] : [item.ts, item.endTs ?? item.ts])
  const span = stamps.length ? Math.max(...stamps) - Math.min(...stamps) : 0
  const { open, onToggle } = useOpen(failed > 0)
  const parts = [
    span >= 1000 ? `Worked for ${formatDuration(span)}` : "Worked",
    plural(tools.length, "tool"),
    ...(files ? [`${plural(files, "file")} changed`] : []),
  ]
  return (
    <details data-slot="tool-group" open={open} onToggle={onToggle} className="text-(length:--fs-sm)">
      <summary className="-mx-(--s2) flex min-h-(--row-h) cursor-default list-none items-center gap-(--s3) rounded-(--r-row) px-(--s2) marker:content-none hover:bg-(--hover) [&::-webkit-details-marker]:hidden">
        <ChevronRight aria-hidden className="size-3.5 shrink-0 text-(--fg3) in-open:rotate-90" />
        <span className="min-w-0 flex-1 truncate text-(--fg2)">{parts.join(" · ")}</span>
        {failed > 0 && <span className="shrink-0 text-(--danger-word)">{failed} failed</span>}
      </summary>
      {open && (
        <div className="flex flex-col border-l-[0.5px] border-(--sep) pl-(--s4)">
          {tools.map((item, index) => <ToolRow key={item.seq ?? index} item={item} />)}
        </div>
      )}
    </details>
  )
}

export function ElicitationCard({ item, machine, sessionId }: { item: ThreadItem; machine: string; sessionId: string }) {
  const ask = item.data as ElicitationPayload & { optionIds?: string[]; allowMultiple?: boolean }
  const [selected, setSelected] = useState<string[]>([])
  const queryClient = useQueryClient()
  const answer = useMutation({
    mutationFn: (value: string) =>
      api(`/api/sessions/${encodeURIComponent(sessionId)}/conversation/elicitation`, {
        machine,
        method: "POST",
        body: { elicitation_id: ask.elicitationId, value },
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["conversation", machine, sessionId] })
    },
  })
  const result = ask.result
  return (
    <div data-slot="elicitation-card" className="rounded-(--r-sheet) border-[0.5px] border-(--warn) bg-(--window) px-(--s5) pt-(--s5) pb-(--s4) shadow-(--shadow-card)">
      <p className="text-(length:--fs) font-semibold text-(--fg)">{ask.prompt ?? "Input needed"}</p>
      {ask.resolved ? <p className="mt-(--s2) text-(length:--fs-sm) text-(--fg2)">Answered: {answerSummary(ask, result?.value)}</p> : ask.inactive ? <p className="mt-(--s2) text-(length:--fs-sm) text-(--fg2)">No longer active</p> : ask.fields?.length ? (
        <ElicitationForm fields={ask.fields} disabled={!ask.elicitationId || answer.isPending} onSubmit={(value) => answer.mutate(value)} />
      ) : (
        <div className="mt-(--s3) flex flex-wrap gap-(--s2)">
          {(ask.choices ?? []).map((choice, index) => {
            const value = ask.optionIds?.[index] ?? choice
            return (
              <Button
                key={value}
                type="button"
                size="sm"
                variant={ask.allowMultiple && selected.includes(value) ? "primary" : "default"}
                aria-pressed={ask.allowMultiple ? selected.includes(value) : undefined}
                disabled={!ask.elicitationId || answer.isPending}
                onClick={() => {
                  if (!ask.allowMultiple) answer.mutate(value)
                  else setSelected((current) => current.includes(value) ? current.filter((item) => item !== value) : [...current, value])
                }}
              >
                {choice}
              </Button>
            )
          })}
          {ask.allowMultiple && (
            <Button
              type="button"
              size="sm"
              variant="default"
              disabled={!ask.elicitationId || answer.isPending || selected.length === 0}
              onClick={() => answer.mutate(JSON.stringify(selected))}
            >
              Submit
            </Button>
          )}
        </div>
      )}
      {answer.error && <p className="mt-(--s2) text-(length:--fs-sm) text-(--danger)">{conversationErrorMessage(machine, "answer a question", answer.error)}</p>}
    </div>
  )
}

function ElicitationForm({ fields, disabled, onSubmit }: { fields: ElicitationField[]; disabled: boolean; onSubmit: (value: string) => void }) {
  const [values, setValues] = useState<Record<string, string | string[]>>({})
  // One question with options and nothing else to fill answers on click.
  const quick = fields.length === 1 && !fields[0]!.multiple && (fields[0]!.options?.length ?? 0) > 0
  return (
    <div className="mt-(--s3) flex flex-col gap-(--s4)">
      {fields.map((field) => {
        const current = values[field.key]
        return (
          <div key={field.key} className="flex flex-col gap-(--s2)">
            {(field.title || field.description) && (
              <p className="text-(length:--fs-sm) text-(--fg)">
                {field.title && <span className="font-semibold">{field.title}</span>}
                {field.title && field.description && " · "}
                {field.description && <span className="text-(--fg2)">{field.description}</span>}
              </p>
            )}
            {field.options?.length ? (
              <div className="flex flex-wrap gap-(--s2)">
                {field.options.map((option) => {
                  const picked = Array.isArray(current) ? current.includes(option.value) : current === option.value
                  return (
                    <Button
                      key={option.value}
                      type="button"
                      size="sm"
                      variant={picked ? "primary" : "default"}
                      aria-pressed={quick ? undefined : picked}
                      title={option.description}
                      disabled={disabled}
                      onClick={() => {
                        if (quick) return onSubmit(JSON.stringify({ [field.key]: option.value }))
                        setValues((all) => {
                          const prior = all[field.key]
                          if (!field.multiple) return { ...all, [field.key]: prior === option.value ? "" : option.value }
                          const list = Array.isArray(prior) ? prior : []
                          return { ...all, [field.key]: list.includes(option.value) ? list.filter((item) => item !== option.value) : [...list, option.value] }
                        })
                      }}
                    >
                      {option.label}
                    </Button>
                  )
                })}
              </div>
            ) : (
              <Input
                aria-label={field.title || field.description || field.key}
                placeholder={field.title === "Other" ? "Your own answer (optional)" : undefined}
                value={typeof current === "string" ? current : ""}
                disabled={disabled}
                onChange={(event) => {
                  const text = event.currentTarget.value
                  setValues((all) => ({ ...all, [field.key]: text }))
                }}
              />
            )}
          </div>
        )
      })}
      {!quick && (
        <div>
          <Button type="button" size="sm" variant="primary" disabled={disabled} onClick={() => onSubmit(JSON.stringify(values))}>
            Submit
          </Button>
        </div>
      )}
    </div>
  )
}

function answerSummary(ask: ElicitationPayload, value?: string): string {
  if (!value) return "—"
  if (!ask.fields?.length) return value
  try {
    const answers = JSON.parse(value) as Record<string, unknown>
    const parts = ask.fields.flatMap((field) => {
      const picked = ([] as unknown[]).concat(answers[field.key] ?? []).map(String).filter(Boolean)
      return picked.map((item) => field.options?.find((option) => option.value === item)?.label ?? item)
    })
    return parts.length ? parts.join(", ") : "skipped"
  } catch {
    return value
  }
}

export function findPendingPermission(items: ThreadItem[]): ThreadItem | undefined {
  return items.findLast((item) => {
    const permission = item.kind === "permission" ? item.data as PermissionPayload : undefined
    return Boolean(permission?.requestId && !permission.resolved && !permission.inactive)
  })
}

function permissionVerb(tool?: string): string {
  switch (tool) {
    case "execute": case "terminal": case "Bash": return "run this command"
    case "edit": return "edit this file"
    case "read": return "read this file"
    case "fetch": return "fetch this page"
    default: return tool ? `use ${tool}` : "continue"
  }
}

// Adapters word options as "Yes"/"No"; the card uses the prototype's verbs and keeps specific labels.
function optionLabel(option: { id: string; label: string }): string {
  if (option.id.startsWith("reject") || option.label === "No") return "Reject"
  if (option.id === "allow-once" || option.label === "Yes") return "Allow once"
  if (option.id === "allow-always") return "Always allow"
  return option.label
}

export function PermissionCard({
  item,
  machine,
  sessionId,
  readOnly,
  agent,
}: {
  item: ThreadItem
  machine: string
  sessionId: string
  readOnly?: boolean
  agent?: string
}) {
  const perm = item.data as PermissionPayload
  const queryClient = useQueryClient()
  const answer = useMutation({
    mutationFn: (optionId: string) =>
      api(`/api/sessions/${encodeURIComponent(sessionId)}/conversation/permission`, {
        machine,
        method: "POST",
        body: { request_id: perm.requestId, option_id: optionId },
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["conversation", machine, sessionId] })
    },
  })
  const open = !readOnly && !perm.resolved && !perm.inactive
  const options = perm.options ?? []
  if (perm.resolved || perm.inactive) {
    const chosen = options.find((option) => option.id === perm.result?.optionId)
    const answerLabel = chosen ? optionLabel(chosen) : perm.result?.optionId
    return (
      <p data-slot="permission-card" className="flex min-h-(--row-h) items-center gap-(--s3) text-(length:--fs-sm) text-(--fg3)">
        <Lock aria-hidden className="size-3.5 shrink-0" />
        <span className="min-w-0 truncate">{perm.resolved ? `Answered: ${answerLabel ?? "—"}` : "No longer active"}{perm.summary ? ` · ${perm.summary}` : ""}</span>
      </p>
    )
  }
  return (
    <div
      data-slot="permission-card"
      role="group"
      aria-label={`Permission request: ${perm.tool ?? "Permission"}`}
      tabIndex={0}
      onKeyDown={(event) => {
        if (!open || !perm.requestId || answer.isPending || event.repeat || event.altKey || event.ctrlKey || event.metaKey || event.shiftKey) return
        if (event.target instanceof HTMLElement && event.target.closest("input, textarea, select, [contenteditable='true']")) return
        const option = event.key === "Enter" && event.target === event.currentTarget ? options[0] : /^[1-9]$/.test(event.key) ? options[Number(event.key) - 1] : undefined
        if (!option) return
        event.preventDefault()
        answer.mutate(option.id)
      }}
      className="rounded-(--r-sheet) border-[0.5px] border-(--warn) bg-(--window) px-(--s5) pt-(--s5) pb-(--s4) shadow-(--shadow-card) outline-none focus-visible:ring-2 focus-visible:ring-(--accent-soft)"
    >
      <div className="flex items-start gap-(--s4)">
        <span aria-hidden className="grid size-7 shrink-0 place-items-center rounded-[7px] bg-[color-mix(in_oklch,var(--warn)_20%,transparent)] text-(--warn-word)">
          <Lock className="size-4" />
        </span>
        <p className="pt-(--s1) text-(length:--fs) font-semibold text-(--fg)">Allow {agent || "the agent"} to {permissionVerb(perm.tool)}?</p>
      </div>
      {perm.summary && <pre data-slot="permission-command" className="my-(--s4) max-h-40 overflow-auto rounded-(--r-ctl) bg-(--code) px-(--s4) py-(--s3) font-(family-name:--mono) text-(length:--fs-sm) text-(--fg) whitespace-pre-wrap break-words">{perm.summary}</pre>}
      <div className="flex flex-wrap justify-end gap-(--s2)">
        {options.map((option, index) => ({ option, index })).reverse().map(({ option, index }) => (
          <Button key={option.id} type="button" size="sm" variant={index === 0 ? "primary" : "default"} title={index < 9 ? `Press ${index + 1}` : undefined} disabled={!perm.requestId || answer.isPending} onClick={() => answer.mutate(option.id)}>
            {optionLabel(option)}
            {index === 0 && <kbd aria-hidden className="font-(family-name:--mono) opacity-70">↩</kbd>}
          </Button>
        ))}
      </div>
      {answer.error && <p className="mt-(--s2) text-(length:--fs-sm) text-(--danger)">{conversationErrorMessage(machine, "answer a permission request", answer.error)}</p>}
    </div>
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
      <RowContextMenuTrigger className="min-w-0">{children}</RowContextMenuTrigger>
      <RowContextMenuContent>
        <RowContextMenuItem icon={<Copy aria-hidden />} onClick={() => copyText(text)}>
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
          <Button type="button" size="icon" variant="ghost" className="size-(--ctl-h-sm)" aria-label="Copy message" title="Copy" onClick={() => copyText(text)}>
            <Copy aria-hidden />
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
  next,
  last,
  streaming,
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
  /** ts of the item after this one; a thought lasted until then. */
  next?: number
  last?: boolean
  streaming?: boolean
  meta?: string
}) {
  switch (item.kind) {
    case "user_message_chunk":
      return <UserMessageBody data={item.data} machine={machine} sessionId={sessionId} onQuote={onQuote} onFork={onFork} />
    case "agent_message_chunk":
      return <AgentBlock text={chunkText(item.data)} last={last} streaming={streaming} meta={meta} onQuote={onQuote} onFork={onFork} onRetry={onRetry} />
    case "agent_thought_chunk":
      return <ThoughtBlock text={chunkText(item.data)} ms={item.ts !== undefined && next !== undefined ? next - item.ts : undefined} />
    case "tool":
      return <ToolRow item={item} />
    case "permission":
      return <fieldset disabled={readOnly} className="min-w-0"><PermissionCard item={item} machine={machine} sessionId={sessionId} readOnly={readOnly} agent={agent} /></fieldset>
    case "elicitation":
      return <fieldset disabled={readOnly} className="min-w-0"><ElicitationCard item={item} machine={machine} sessionId={sessionId} /></fieldset>
    case "compaction":
      return <CompactionBlock item={item} />
    case "divider": {
      const notice = item.data as NoticePayload
      if (notice.code === "auth_required" && notice.harness && onSignedIn) {
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

// One work log per run of tool calls; user, agent, thought, permission and compaction items end the
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
    out.push(batch.length === 1 ? batch[0]! : [...batch])
    batch = []
  }
  const card = (subagent: Subagent, item: ThreadItem) => {
    flush()
    const previous = out.at(-1)
    if (previous && "cards" in previous) previous.cards.push({ subagent, item })
    else out.push({ cards: [{ subagent, item }] })
  }
  for (const item of items) {
    if (item.kind === "tool") {
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
    return { label: `Running ${command ?? data.title ?? data.kind ?? "a tool"}`, since: tool.ts }
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
  subagents = {},
  lastTurn,
  turnStarted,
  animateAfter,
  onOpenSubagent,
  onStopSubagent,
  onSignedIn,
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
}) {
  if (items.length === 0) {
    return working ? <LiveLine items={items} subagents={subagents} turnStarted={turnStarted} /> : <p className="py-(--s7) text-center text-(length:--fs-sm) text-(--fg3)">No messages yet.</p>
  }
  const retry = latestUserPrompt(items) && onRetry ? onRetry : undefined
  const lastAgent = items.findLast((item) => item.kind === "agent_message_chunk")
  const compacted = items.some((item) => item.kind === "compaction")
  const signIn = items.findLast((item) => item.kind === "divider" && (item.data as NoticePayload).code === "auth_required")
  const fresh = (item: ThreadItem) => animateAfter !== undefined && item.seq !== undefined && item.seq > animateAfter
  const permission = (item: ThreadItem) => (
    <fieldset disabled={readOnly} className="min-w-0">
      <PermissionCard item={item} machine={machine} sessionId={sessionId} readOnly={readOnly} agent={agent} />
    </fieldset>
  )
  const entries = groupItems(items, subagents)
  // Each item's successor timestamp, found once instead of searching the list per item.
  const nextTs = new Map<ThreadItem, number | undefined>()
  for (let i = 0; i < items.length; i++) nextTs.set(items[i]!, items[i + 1]?.ts)
  return (
    <div className="flex flex-col gap-(--s4)">
      {entries.map((entry, index) => {
        if (Array.isArray(entry)) {
          return (
            <div key={entry[0]!.seq ?? index} className={cn("thread-item", fresh(entry.at(-1)!) && "tool-enter")}>
              <ToolGroup tools={entry} />
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
        const isLastAgent = entry === lastAgent
        return (
          <div key={entry.seq ?? index} className={cn("thread-item", fresh(entry) && (entry.kind === "tool" ? "tool-enter" : "thread-enter"))}>
            <ThreadItemView
              item={entry}
              machine={machine}
              sessionId={sessionId}
              readOnly={readOnly}
              agent={agent}
              onQuote={onQuote}
              onFork={onFork}
              onRetry={isLastAgent ? retry : undefined}
              onSignedIn={entry === signIn ? onSignedIn : undefined}
              next={nextTs.get(entry)}
              last={isLastAgent && !working}
              streaming={isLastAgent && working}
              meta={isLastAgent && !working ? turnMeta(lastTurn, entry.ts) : undefined}
            />
          </div>
        )
      })}
      {working && <LiveLine items={items} subagents={subagents} turnStarted={turnStarted} />}
    </div>
  )
}
