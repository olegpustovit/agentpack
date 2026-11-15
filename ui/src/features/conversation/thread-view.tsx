import { useState, type ReactNode } from "react"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import { BookOpen, ChevronRight, Copy, Folder, GitFork, Globe, Pencil, Quote, RotateCcw, Search, Sparkles, Terminal, Wrench } from "lucide-react"
import { RowContextMenu, RowContextMenuContent, RowContextMenuItem, RowContextMenuTrigger } from "@/components/kit/overlays"
import { Button } from "@/components/ui/button"
import { api, LOCAL } from "@/lib/api"
import { hud } from "@/lib/hud"
import type { CompactionPayload, ElicitationPayload, NoticePayload, PermissionPayload, TextChunkPayload, ThreadItem, ToolPayload, UserMessagePayload } from "./model"
import { AgentMessageBody } from "./agent-message"
import { ToolDiffBody } from "./tool-diff"
import { QuickLook } from "./quick-look"
import { conversationErrorMessage } from "./queries"
import { listDiffChanges } from "./diff-summary"

function chunkText(data: unknown): string {
  return (data as TextChunkPayload).text?.text ?? ""
}

function userImageSrc(machine: string, sessionId: string, blob?: string) {
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
      <div className="flex w-full flex-col items-end gap-(--s2)">
        {previews.length > 0 && (
          <div data-slot="user-attachments" className="flex max-w-[min(78%,560px)] flex-wrap justify-end gap-(--s3)">
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
            <p data-slot="user-bubble" className="max-w-[min(78%,560px)] rounded-(--r-pop) bg-(--track) px-(--s4) py-(--s3) text-(length:--fs-sm) text-(--fg) whitespace-pre-wrap">
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

function copyText(text: string) {
  ;(navigator.clipboard?.writeText(text) ?? Promise.reject()).then(
    () => hud.success("Copied"),
    () => hud.error("Couldn't copy. Select the text and copy it manually."),
  )
}

function toolIcon(kind?: string) {
  switch (kind) {
    case "read": return BookOpen
    case "search": return Search
    case "list": return Folder
    case "edit": return Pencil
    case "terminal": return Terminal
    case "fetch": return Globe
    default: return Wrench
  }
}

function statusGlyph(status: string) {
  const glyph = status === "completed" ? "✓" : status === "failed" ? "✕" : "…"
  return (
    <span role="img" aria-label={status} title={status} className={`shrink-0 ${status === "failed" ? "text-(--danger-word)" : "text-(--fg3)"}`}>
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

const preClass = "mt-(--s2) max-h-40 overflow-auto rounded-(--r-row) bg-(--window) px-(--s3) py-(--s2) font-(family-name:--mono) text-(length:--fs-xs) text-(--fg2) whitespace-pre-wrap"

function ToolRow({ data }: { data: unknown }) {
  const tool = data as ToolPayload
  const title = tool.title ?? tool.kind ?? "Tool"
  const status = tool.status ?? "pending"
  const Icon = toolIcon(tool.kind)
  const diff = <ToolDiffBody content={tool.content} toolId={tool.toolCallId} />
  const hasDiff = (tool.content ?? []).some((block) => block.type === "diff" && (block.oldText || block.newText))
  const terminalBody = tool.output?.trim() || (tool.kind === "terminal" && tool.command ? tool.command : "")
  const input = shortInput(tool.rawInput)
  return (
    <details
      open={hasDiff || Boolean(terminalBody)}
      data-slot={hasDiff ? "tool-diff-block" : terminalBody ? "tool-terminal-block" : "tool-row"}
      data-tool-row=""
      className="text-(length:--fs-sm)"
    >
      <summary className="-mx-(--s2) flex min-h-(--row-h) cursor-default list-none items-center gap-(--s3) rounded-(--r-row) px-(--s2) marker:content-none hover:bg-(--hover) [&::-webkit-details-marker]:hidden">
        <ChevronRight aria-hidden className="size-(--s4) shrink-0 text-(--fg3) in-open:rotate-90" />
        <Icon aria-hidden className="size-(--s4) shrink-0 text-(--fg2)" />
        <span className="min-w-0 flex-1 truncate text-(--fg)">{title}</span>
        {statusGlyph(status)}
      </summary>
      {hasDiff ? (
        diff
      ) : terminalBody ? (
        <pre data-slot="tool-terminal" className={preClass}>{terminalBody}</pre>
      ) : (
        <div className="pl-(--s7)">
          {input && <p data-slot="tool-input" className="truncate font-(family-name:--mono) text-(length:--fs-xs) text-(--fg2)">{input}</p>}
          <details data-slot="tool-details" className="text-(length:--fs-xs) text-(--fg3)">
            <summary className="cursor-default">Details</summary>
            <pre className={preClass}>{JSON.stringify(data, null, 2)}</pre>
          </details>
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
  if (tools.length === 1) return <ToolRow data={tools[0]!.data} />
  const failed = tools.filter((item) => (item.data as ToolPayload).status === "failed").length
  const files = new Set(listDiffChanges(tools).map((change) => change.path)).size
  // ts is only known for streamed items; omit the time rather than guess.
  const stamps = tools.flatMap((item) => item.ts === undefined ? [] : [item.ts, item.endTs ?? item.ts])
  const span = stamps.length ? Math.max(...stamps) - Math.min(...stamps) : 0
  const parts = [
    span >= 1000 ? `Worked for ${formatDuration(span)}` : "Worked",
    plural(tools.length, "tool"),
    ...(files ? [`${plural(files, "file")} changed`] : []),
  ]
  return (
    <details data-slot="tool-group" open={failed > 0} className="text-(length:--fs-sm)">
      <summary className="-mx-(--s2) flex min-h-(--row-h) cursor-default list-none items-center gap-(--s3) rounded-(--r-row) px-(--s2) marker:content-none hover:bg-(--hover) [&::-webkit-details-marker]:hidden">
        <ChevronRight aria-hidden className="size-(--s4) shrink-0 text-(--fg3) in-open:rotate-90" />
        <span className="min-w-0 flex-1 truncate text-(--fg2)">{parts.join(" · ")}</span>
        {failed > 0 && <span className="shrink-0 text-(--danger-word)">{failed} failed</span>}
      </summary>
      <div className="flex flex-col border-l-[0.5px] border-(--sep) pl-(--s4)">
        {tools.map((item, index) => <ToolRow key={item.seq ?? index} data={item.data} />)}
      </div>
    </details>
  )
}

function ElicitationCard({ item, machine, sessionId }: { item: ThreadItem; machine: string; sessionId: string }) {
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
    <div data-slot="elicitation-card" className="rounded-(--r-pop) border-[0.5px] border-(--warn) bg-(--window) p-(--s4)">
      <p className="text-(length:--fs-sm) font-medium text-(--fg)">{ask.prompt ?? "Input needed"}</p>
      {ask.resolved ? <p className="mt-(--s2) text-(length:--fs-sm) text-(--fg2)">Answered: {result?.value ?? "—"}</p> : ask.inactive ? <p className="mt-(--s2) text-(length:--fs-sm) text-(--fg2)">No longer active</p> : (
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

export function findPendingPermission(items: ThreadItem[]): ThreadItem | undefined {
  return items.findLast((item) => {
    const permission = item.kind === "permission" ? item.data as PermissionPayload : undefined
    return Boolean(permission?.requestId && !permission.resolved && !permission.inactive)
  })
}

export function PermissionCard({
  item,
  machine,
  sessionId,
  readOnly,
  docked,
}: {
  item: ThreadItem
  machine: string
  sessionId: string
  readOnly?: boolean
  docked?: boolean
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
  const result = perm.result
  const answerLabel = perm.options?.find((option) => option.id === result?.optionId)?.label ?? result?.optionId
  return (
    <div
      data-slot="permission-card"
      role="group"
      aria-label={`Permission request: ${perm.tool ?? "Permission"}`}
      tabIndex={perm.resolved || perm.inactive ? -1 : 0}
      onKeyDown={(event) => {
        if (!open || !perm.requestId || answer.isPending || event.repeat || event.altKey || event.ctrlKey || event.metaKey || event.shiftKey || !/^[1-9]$/.test(event.key)) return
        if (event.target instanceof HTMLElement && event.target.closest("input, textarea, select, [contenteditable='true']")) return
        const option = perm.options?.[Number(event.key) - 1]
        if (!option) return
        event.preventDefault()
        answer.mutate(option.id)
      }}
      className={`rounded-(--r-pop) border-[0.5px] border-(--warn) bg-(--window) focus-visible:ring-2 focus-visible:ring-(--accent-soft) ${docked ? "p-(--s3)" : "p-(--s4)"}`}
    >
      <p className="text-(length:--fs-sm) font-medium text-(--fg)">{perm.tool ?? "Permission"}</p>
      {perm.summary && (docked
        ? <pre data-slot="permission-command" className="mt-(--s2) max-h-24 overflow-auto rounded-(--r-row) bg-(--track) px-(--s3) py-(--s2) font-(family-name:--mono) text-(length:--fs-xs) text-(--fg) whitespace-pre-wrap">{perm.summary}</pre>
        : <p className="mt-(--s2) text-(length:--fs-sm) text-(--fg2)">{perm.summary}</p>)}
      {perm.resolved ? <p className="mt-(--s2) text-(length:--fs-sm) text-(--fg2)">Answered: {answerLabel ?? "—"}</p> : perm.inactive ? <p className="mt-(--s2) text-(length:--fs-sm) text-(--fg2)">No longer active</p> : (
        <div className="mt-(--s3) flex flex-wrap gap-(--s2)">
          {(perm.options ?? []).map((option, index) => (
            <Button key={option.id} type="button" size="sm" variant="default" disabled={!perm.requestId || answer.isPending} onClick={() => answer.mutate(option.id)}>
              {index < 9 && <kbd aria-hidden className="font-(family-name:--mono) text-(--fg3)">{index + 1}</kbd>}
              {option.label}
            </Button>
          ))}
        </div>
      )}
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
  onQuote,
  onFork,
  onRetry,
}: {
  text: string
  onQuote?: (text: string) => void
  onFork?: () => void
  onRetry?: () => void
}) {
  return (
    <div data-slot="agent-message" className="group relative">
      <MessageContextMenu text={text} onQuote={onQuote} onFork={onFork} onRetry={onRetry}>
        <AgentMessageBody text={text} />
      </MessageContextMenu>
      {text && (
        <div className="mt-(--s1) flex justify-end gap-(--s1) min-[760px]:absolute min-[760px]:top-0 min-[760px]:right-0 min-[760px]:z-10 min-[760px]:mt-0 min-[760px]:rounded-(--r-pop) min-[760px]:bg-(--control) min-[760px]:p-(--s1) min-[760px]:opacity-0 min-[760px]:shadow-(--shadow-pop) min-[760px]:transition-opacity min-[760px]:group-hover:opacity-100 min-[760px]:group-focus-within:opacity-100">
          <Button type="button" size="icon" variant="ghost" className="size-(--ctl-h-sm)" aria-label="Copy message" onClick={() => copyText(text)}>
            <Copy aria-hidden />
          </Button>
          {onFork && (
            <Button type="button" size="icon" variant="ghost" className="size-(--ctl-h-sm)" aria-label="Fork conversation" onClick={onFork}>
              <GitFork aria-hidden />
            </Button>
          )}
          {onRetry && (
            <Button type="button" size="icon" variant="ghost" className="size-(--ctl-h-sm)" aria-label="Retry last message" onClick={onRetry}>
              <RotateCcw aria-hidden />
            </Button>
          )}
        </div>
      )}
    </div>
  )
}
function CompactionBlock({ item }: { item: ThreadItem }) {
  const compaction = item.data as CompactionPayload
  const label = compaction.status === "in_progress" ? "Compacting context…" : compaction.error ? "Context compaction failed" : "Context compacted"
  const rule = <span aria-hidden className="h-px flex-1 bg-(--sep)" />
  return (
    <details data-slot="conversation-compaction" aria-label="Context compaction" className="text-(length:--fs-xs) text-(--fg3)">
      <summary className="flex cursor-default list-none items-center gap-(--s3) marker:content-none [&::-webkit-details-marker]:hidden">
        {rule}
        <span>{label}</span>
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
  docked,
  onQuote,
  onFork,
  onRetry,
}: {
  item: ThreadItem
  machine: string
  sessionId: string
  readOnly?: boolean
  docked?: boolean
  onQuote?: (text: string) => void
  onFork?: () => void
  onRetry?: () => void
}) {
  switch (item.kind) {
    case "user_message_chunk":
      return <UserMessageBody data={item.data} machine={machine} sessionId={sessionId} onQuote={onQuote} onFork={onFork} />
    case "agent_message_chunk":
      return <AgentBlock text={chunkText(item.data)} onQuote={onQuote} onFork={onFork} onRetry={onRetry} />
    case "agent_thought_chunk":
      return (
        <details data-slot="thought-block" className="text-(length:--fs-sm)">
          <summary className="-mx-(--s2) flex min-h-(--row-h) cursor-default list-none items-center gap-(--s3) rounded-(--r-row) px-(--s2) text-(--fg2) marker:content-none hover:bg-(--hover) [&::-webkit-details-marker]:hidden">
            <ChevronRight aria-hidden className="size-(--s4) shrink-0 text-(--fg3) in-open:rotate-90" />
            <Sparkles aria-hidden className="size-(--s4) shrink-0" />
            Thought
          </summary>
          <p className="ml-(--s3) border-l-[0.5px] border-(--sep) py-(--s2) pl-(--s4) text-(--fg2) italic whitespace-pre-wrap">{chunkText(item.data)}</p>
        </details>
      )
    case "tool":
      return <ToolRow data={item.data} />
    case "permission":
      if (docked) return <p data-slot="permission-placeholder" className="text-(length:--fs-sm) text-(--fg2)">⧗ {(item.data as PermissionPayload).tool ?? "Permission"} · waiting for approval</p>
      return <fieldset disabled={readOnly}><PermissionCard item={item} machine={machine} sessionId={sessionId} readOnly={readOnly} /></fieldset>
    case "elicitation":
      return <fieldset disabled={readOnly}><ElicitationCard item={item} machine={machine} sessionId={sessionId} /></fieldset>
    case "compaction":
      return <CompactionBlock item={item} />
    case "divider": {
      const notice = item.data as NoticePayload
      return (
        <div data-slot="conversation-divider" className="flex flex-col gap-(--s1) text-center text-(length:--fs-xs) text-(--fg3)">
          {notice.title && <p className="font-medium text-(--fg2)">{notice.title}</p>}
          {notice.description && <p>{notice.description}</p>}
        </div>
      )
    }
    case "plan":
      return null
    default:
      return null
  }
}

// One work log per run of tool calls; user, agent, thought, permission and compaction items end the run.
function groupTools(items: ThreadItem[]): (ThreadItem | ThreadItem[])[] {
  const out: (ThreadItem | ThreadItem[])[] = []
  let batch: ThreadItem[] = []
  const flush = () => {
    if (batch.length === 0) return
    out.push(batch.length === 1 ? batch[0]! : [...batch])
    batch = []
  }
  for (const item of items) {
    if (item.kind === "tool") batch.push(item)
    else if (item.kind !== "plan") {
      flush()
      out.push(item)
    }
  }
  flush()
  return out
}

export function ThreadView({
  items,
  machine,
  sessionId,
  onQuote,
  onFork,
  onRetry,
  readOnly,
  dockPending,
}: {
  readOnly?: boolean
  dockPending?: boolean
  items: ThreadItem[]
  machine: string
  sessionId: string
  onQuote?: (text: string) => void
  onFork?: () => void
  onRetry?: () => void
}) {
  if (items.length === 0) {
    return <p className="py-(--s7) text-center text-(length:--fs-sm) text-(--fg3)">No messages yet.</p>
  }
  const retry = latestUserPrompt(items) && onRetry ? onRetry : undefined
  const pending = dockPending && !readOnly ? findPendingPermission(items) : undefined
  const lastAgent = items.findLast((item) => item.kind === "agent_message_chunk")
  const compacted = items.some((item) => item.kind === "compaction")
  return (
    <div className="flex flex-col gap-(--s4)">
      {groupTools(items).map((entry, index) => {
        if (Array.isArray(entry)) return <ToolGroup key={entry[0]!.seq ?? index} tools={entry} />
        // The compaction block is the one divider for a compaction; the notice would repeat it.
        if (compacted && entry.kind === "divider" && (entry.data as NoticePayload).title === "Context compacted") return null
        return (
          <ThreadItemView
            key={entry.seq ?? index}
            item={entry}
            machine={machine}
            sessionId={sessionId}
            readOnly={readOnly}
            docked={entry === pending}
            onQuote={onQuote}
            onFork={onFork}
            onRetry={entry === lastAgent ? retry : undefined}
          />
        )
      })}
    </div>
  )
}
