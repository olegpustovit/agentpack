import { useEffect, useRef, useState, type ReactNode } from "react"
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

function ToolRow({ data }: { data: unknown }) {
  const tool = data as ToolPayload
  const title = tool.title ?? tool.kind ?? "Tool"
  const status = tool.status ?? "pending"
  const Icon = toolIcon(tool.kind)
  const diff = <ToolDiffBody content={tool.content} />
  const hasDiff = (tool.content ?? []).some((block) => block.type === "diff" && (block.oldText || block.newText))
  const terminalBody = tool.output?.trim() || (tool.kind === "terminal" && tool.command ? tool.command : "")
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
        <span className={`shrink-0 ${status === "completed" ? "text-(--ok-word)" : status === "failed" ? "text-(--danger-word)" : "text-(--fg3)"}`}>{status}</span>
      </summary>
      {hasDiff ? (
        diff
      ) : terminalBody ? (
        <pre
          data-slot="tool-terminal"
          className="mt-(--s2) max-h-40 overflow-auto rounded-(--r-row) bg-(--window) px-(--s3) py-(--s2) font-(family-name:--mono) text-(length:--fs-xs) text-(--fg2) whitespace-pre-wrap"
        >
          {terminalBody}
        </pre>
      ) : (
        <pre className="mt-(--s2) max-h-40 overflow-auto rounded-(--r-row) bg-(--window) px-(--s3) py-(--s2) font-(family-name:--mono) text-(length:--fs-xs) text-(--fg2) whitespace-pre-wrap">
          {JSON.stringify(data, null, 2)}
        </pre>
      )}
    </details>
  )
}

function ToolGroup({ tools }: { tools: ThreadItem[] }) {
  if (tools.length === 1) return <ToolRow data={tools[0]!.data} />
  return (
    <details data-slot="tool-group" className="text-(length:--fs-sm)">
      <summary className="-mx-(--s2) flex min-h-(--row-h) cursor-default list-none items-center gap-(--s3) rounded-(--r-row) px-(--s2) marker:content-none hover:bg-(--hover) [&::-webkit-details-marker]:hidden">
        <ChevronRight aria-hidden className="size-(--s4) shrink-0 text-(--fg3) in-open:rotate-90" />
        <span className="min-w-0 flex-1 truncate font-medium text-(--fg)">Explored</span>
        <span className="shrink-0 text-(--fg3)">{tools.length} steps</span>
      </summary>
      <ul className="flex flex-col border-l-[0.5px] border-(--sep) pl-(--s4)">
        {tools.map((item, index) => {
          const tool = item.data as ToolPayload
          const Icon = toolIcon(tool.kind)
          return (
            <li key={index} className="flex min-h-(--row-h) items-center gap-(--s3) text-(length:--fs-sm) text-(--fg2)">
              <Icon aria-hidden className="size-(--s4) shrink-0" />
              <span className="min-w-0 flex-1 truncate">{tool.title ?? tool.kind ?? "Tool"}</span>
              <span className="shrink-0 text-(--fg3)">{tool.status ?? "pending"}</span>
            </li>
          )
        })}
      </ul>
    </details>
  )
}

function isExploratoryTool(item: ThreadItem): boolean {
  const kind = (item.data as ToolPayload | undefined)?.kind
  return kind === "read" || kind === "search" || kind === "list"
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

function PermissionCard({
  item,
  machine,
  sessionId,
  keyboardDefault,
}: {
  item: ThreadItem
  machine: string
  sessionId: string
  keyboardDefault: boolean
}) {
  const perm = item.data as PermissionPayload
  const queryClient = useQueryClient()
  const cardRef = useRef<HTMLDivElement>(null)
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
  useEffect(() => {
    if (!perm.requestId || perm.resolved || perm.inactive || answer.isPending) return
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape" || event.repeat || event.altKey || event.ctrlKey || event.metaKey || event.shiftKey || !/^[1-9]$/.test(event.key)) return
      if (event.target instanceof HTMLElement && event.target.closest("[role='menu'], [role='menuitem'], [role='listbox'], [role='option']")) return
      for (const menu of document.querySelectorAll<HTMLElement>("[role='menu'], [role='listbox']")) {
        if (menu.getClientRects().length > 0) return
      }
      const active = document.activeElement
      const focusedControl =
        active instanceof HTMLElement &&
        active.matches("a[href], button, input, select, textarea, summary, [tabindex]:not([tabindex='-1']), [contenteditable='true'], [role='menu'], [role='menuitem'], [role='listbox'], [role='option'], [role='combobox']")
      if (active !== cardRef.current && (!keyboardDefault || focusedControl)) return
      const option = perm.options?.[Number(event.key) - 1]
      if (!option) return
      event.preventDefault()
      answer.mutate(option.id)
    }
    window.addEventListener("keydown", onKeyDown)
    return () => window.removeEventListener("keydown", onKeyDown)
  }, [answer.isPending, answer.mutate, keyboardDefault, perm.inactive, perm.options, perm.requestId, perm.resolved])
  const result = perm.result
  const answerLabel = perm.options?.find((option) => option.id === result?.optionId)?.label ?? result?.optionId
  return (
    <div
      ref={cardRef}
      data-slot="permission-card"
      role="group"
      aria-label={`Permission request: ${perm.tool ?? "Permission"}`}
      tabIndex={perm.resolved || perm.inactive ? -1 : 0}
      className="rounded-(--r-pop) border-[0.5px] border-(--warn) bg-(--window) p-(--s4) focus-visible:ring-2 focus-visible:ring-(--accent-soft)"
    >
      <p className="text-(length:--fs-sm) font-medium text-(--fg)">{perm.tool ?? "Permission"}</p>
      {perm.summary && <p className="mt-(--s2) text-(length:--fs-sm) text-(--fg2)">{perm.summary}</p>}
      {perm.resolved ? <p className="mt-(--s2) text-(length:--fs-sm) text-(--fg2)">Answered: {answerLabel ?? "—"}</p> : perm.inactive ? <p className="mt-(--s2) text-(length:--fs-sm) text-(--fg2)">No longer active</p> : (
        <div className="mt-(--s3) flex flex-wrap gap-(--s2)">
          {(perm.options ?? []).map((option) => (
            <Button key={option.id} type="button" size="sm" variant="default" disabled={!perm.requestId || answer.isPending} onClick={() => answer.mutate(option.id)}>{option.label}</Button>
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
        <div className="absolute top-0 right-0 z-10 flex gap-(--s1) rounded-(--r-pop) bg-(--control) p-(--s1) opacity-0 shadow-(--shadow-pop) transition-opacity group-hover:opacity-100 group-focus-within:opacity-100 max-[760px]:opacity-100">
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
  return (
    <section
      data-slot="conversation-compaction"
      aria-label="Context compaction"
      className="rounded-(--r-row) border-[0.5px] border-(--sep) bg-(--track) px-(--s3) py-(--s2) text-(length:--fs-sm)"
    >
      <p className="font-medium text-(--fg)">{compaction.status}</p>
      {compaction.summary && <p className="mt-(--s2) whitespace-pre-wrap text-(--fg2)">{compaction.summary}</p>}
      {compaction.error && <p role="alert" className="mt-(--s2) text-(--danger-word)">{compaction.error}</p>}
    </section>
  )
}


function ThreadItemView({
  item,
  machine,
  sessionId,
  keyboardDefault,
  onQuote,
  onFork,
  onRetry,
}: {
  item: ThreadItem
  machine: string
  sessionId: string
  keyboardDefault: boolean
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
      return <PermissionCard item={item} machine={machine} sessionId={sessionId} keyboardDefault={keyboardDefault} />
    case "elicitation":
      return <ElicitationCard item={item} machine={machine} sessionId={sessionId} />
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

function groupTools(items: ThreadItem[]): (ThreadItem | ThreadItem[])[] {
  const out: (ThreadItem | ThreadItem[])[] = []
  let batch: ThreadItem[] = []
  const flush = () => {
    if (batch.length === 0) return
    out.push(batch.length === 1 ? batch[0]! : [...batch])
    batch = []
  }
  for (const item of items) {
    if (item.kind === "tool" && isExploratoryTool(item)) batch.push(item)
    else {
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
}: {
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
  const latestPrompt = latestUserPrompt(items)
  const retry = latestPrompt && onRetry ? onRetry : undefined
  let latestPendingPermission: ThreadItem | undefined
  for (let index = items.length - 1; index >= 0; index--) {
    const item = items[index]!
    const permission = item.kind === "permission" ? item.data as PermissionPayload : undefined
    if (permission?.requestId && !permission.resolved && !permission.inactive) {
      latestPendingPermission = item
      break
    }
  }
  return (
    <div className="flex flex-col gap-(--s4)">
      {groupTools(items).map((entry, index) =>
        Array.isArray(entry) ? (
          <ToolGroup key={index} tools={entry} />
        ) : (
          <ThreadItemView
            key={index}
            item={entry}
            machine={machine}
            sessionId={sessionId}
            keyboardDefault={entry === latestPendingPermission}
            onQuote={onQuote}
            onFork={onFork}
            onRetry={entry.kind === "agent_message_chunk" ? retry : undefined}
          />
        ),
      )}
    </div>
  )
}
