import { useMutation, useQueryClient } from "@tanstack/react-query"
import { useEffect, useRef, useState, type ReactNode, type RefObject } from "react"
import { ArrowUp, AtSign, Check, ChevronDown, CornerDownLeft, Plus, Slash, Square } from "lucide-react"
import { Menu, MenuContent, MenuItem, MenuTrigger } from "@/components/kit/overlays"
import { Spinner } from "@/components/kit/feedback"
import { Button } from "@/components/ui/button"
import { pendingChatClaim } from "@/features/sessions/start"
import { sessionsQuery } from "@/features/sessions/queries"
import { agentIds } from "@/features/sessions/rules"
import type { SessionInfo } from "@/lib/api-types"
import { harnessName } from "@/lib/format"
import { useIntent } from "@/lib/intent"
import { hud } from "@/lib/hud"
import { api, ApiError, LOCAL } from "@/lib/api"
import { conversationEventsQuery, conversationErrorMessage, conversationNeedsUpdate } from "./queries"
import type { LogEntry, ThreadItem, ThreadState, UserMessagePayload } from "./model"
import { reduceThread } from "./reducer"
import { userImageSrc } from "./thread-view"
import { parseGoalCommand } from "./goal-sheet"
import { QueueChips } from "./queue-chips"
import type { useHistoryPaging } from "./conversation-log"
import { ChatConfigControls, type useChatConfig } from "./chat-config"
import { applyHint, commandSpans, type AgentCommand, filterAtHints, filterSlashHints, moveHint, slashQuery, type ComposerHint } from "./composer-hints"
import { ComposerHintsMenu } from "./composer-hints-menu"
import { listDiffChanges } from "./diff-summary"
import { loadAttachments, revokePreview, saveAttachments, uploadConversationBlob, type PendingAttachment } from "./upload-blob"
import { imageFilesFromClipboard, imageFilesFromList, promptRequest } from "./composer-attach"

type UploadedAttachment = PendingAttachment & { name: string }
export type SendInput = {
  text: string
  /** Interrupt the running turn and send this next. */
  steer?: boolean
  attachments: (Pick<PendingAttachment, "sha" | "mime" | "width" | "height"> & { name?: string })[]
  resourceLink?: { sha: string; name: string }
  clearComposer: boolean
}
type PromptIntent = SendInput & { id: string; startedAt?: number }
type GoalCommand = NonNullable<ReturnType<typeof parseGoalCommand>>

const draftKey = (machine: string, sessionId: string) => `agentpack:draft:${machine}/${sessionId}`
function loadDraft(key: string) {
  try {
    return sessionStorage.getItem(key) ?? ""
  } catch {
    return ""
  }
}

// The draft, its attachments and the sends in flight. The pane holds it because quoting, retrying,
// sign-in and the inspector also write to the composer or send through it.
export function useComposer({ machine, sessionId, info, entries, reportTimings }: {
  machine: string
  sessionId: string
  info?: SessionInfo
  entries: LogEntry[]
  reportTimings: boolean
}) {
  const queryClient = useQueryClient()
  const [draft, setDraft] = useState(() => loadDraft(draftKey(machine, sessionId)))
  const recalledPromptRef = useRef<{ seq: number; text: string } | null>(null)
  const inputRef = useRef<HTMLTextAreaElement>(null)
  const [attachments, setAttachmentsState] = useState<UploadedAttachment[]>(() =>
    loadAttachments(`${draftKey(machine, sessionId)}:attachments`, (sha) => userImageSrc(machine, sessionId, sha)),
  )
  // Saved with the draft, so switching chats and coming back keeps them.
  function setAttachments(update: (current: UploadedAttachment[]) => UploadedAttachment[]) {
    setAttachmentsState((current) => {
      const next = update(current)
      for (const item of current) if (!next.includes(item)) revokePreview(item.previewUrl)
      saveAttachments(`${draftKey(machine, sessionId)}:attachments`, next)
      return next
    })
  }
  useEffect(() => {
    const save = draft && recalledPromptRef.current?.text !== draft
    try {
      const key = draftKey(machine, sessionId)
      if (save) sessionStorage.setItem(key, draft)
      else sessionStorage.removeItem(key)
    } catch {
      // Storage can be blocked; the draft then lives only in memory.
    }
  }, [draft, machine, sessionId])
  useEffect(() => {
    const input = inputRef.current
    if (!input) return
    input.style.height = "auto"
    input.style.height = `${Math.min(input.scrollHeight, 180)}px`
  }, [draft])

  const [pendingSends, setPendingSends] = useState<PromptIntent[]>([])
  useEffect(() => {
    if (!pendingSends.length) return
    const received = confirmedPrompts(entries)
    const pending = pendingSends.filter((item) => {
      if (!received.has(item.id)) return true
      if (item.startedAt !== undefined) {
        chatTiming("prompt-confirmed", item.startedAt, reportTimings)
      }
      return false
    })
    if (pending.length !== pendingSends.length) setPendingSends(pending)
  }, [entries, pendingSends, reportTimings])
  const discardSend = (id: string) => setPendingSends((pending) => pending.filter((item) => item.id !== id))
  const send = useIntent({
    scope: { id: sessionId },
    request: async (input: SendInput, id) => {
      const claimed = await pendingChatClaim(sessionId)
      const target = claimed?.id ?? sessionId
      const request = promptRequest(
        input.text,
        input.attachments,
        info?.conversation_state === "starting" ? undefined : info?.agent_capabilities?.promptCapabilities?.image === true,
        input.resourceLink,
      )
      return api<{ queued?: string }>(`/api/sessions/${encodeURIComponent(target)}/conversation/${request.path}`, {
        machine,
        method: "POST",
        idempotencyKey: id,
        body: input.steer ? { ...request.body, steer: true } : request.body,
      })
    },
    // The composer clears as the message is sent; a failure puts it back.
    apply: (input, id) => {
      const startedAt = performance.now()
      setPendingSends((pending) => [...pending.filter((item) => item.id !== id), { ...input, id, startedAt }])
      if (input.clearComposer) {
        setDraft("")
        setAttachments(() => [])
      }
      return () => {
        discardSend(id)
        if (input.clearComposer) setDraft((current) => current || input.text)
      }
    },
    accepted: (result, _input, id) => {
      if (result?.queued !== id) discardSend(id)
    },
    failed: (error, input, retry) => {
      hud.error(
        conversationErrorMessage(machine, input.steer ? "steer the agent" : input.attachments.length > 1 ? "send multiple images" : "send a message", error),
        conversationNeedsUpdate(error) ? undefined : retry,
      )
    },
  })
  const resume = useIntent({
    request: (text: string, idempotencyKey) => api<SessionInfo & { queued?: string }>(
      `/api/sessions/${encodeURIComponent(sessionId)}/resume`,
      { machine, method: "POST", idempotencyKey, body: { text } },
    ),
    apply: (text, id) => {
      if (!text) return
      const startedAt = performance.now()
      setPendingSends((pending) => [...pending.filter((item) => item.id !== id), { id, text, attachments: [], clearComposer: true, startedAt }])
      setDraft("")
      return () => {
        discardSend(id)
        setDraft((current) => current || text)
      }
    },
    accepted: (result, _text, id) => {
      if (result?.queued !== id) discardSend(id)
      void queryClient.invalidateQueries({ queryKey: conversationEventsQuery(machine, sessionId).queryKey })
      return queryClient.invalidateQueries({ queryKey: sessionsQuery(false).queryKey })
    },
    failed: (error, _text, retry) => {
      // An older peer can't start a chat whose agent never started.
      const old = error instanceof ApiError && error.status === 400 && error.message === "This chat cannot be resumed."
      hud.error(
        old ? `${machine === LOCAL ? "Gateway" : "Peer"} needs an update to restart this chat.` : conversationErrorMessage(machine, "resume the chat", error),
        old || conversationNeedsUpdate(error) ? undefined : retry,
      )
    },
  })
  return { draft, setDraft, recalledPromptRef, inputRef, attachments, setAttachments, pendingSends, send, resume }
}

// Context used, as a ring. At 75 % it turns to the warning colour: compact before the agent does it mid-task.
function ContextRing({ percent }: { percent: number }) {
  const radius = 8
  const length = 2 * Math.PI * radius
  return (
    <svg viewBox="0 0 20 20" className="size-4 -rotate-90" aria-hidden>
      <circle cx="10" cy="10" r={radius} fill="none" stroke="var(--track)" strokeWidth="2.5" />
      <circle
        cx="10" cy="10" r={radius} fill="none" strokeWidth="2.5" strokeLinecap="round"
        stroke={percent >= 75 ? "var(--warn)" : "var(--accent)"}
        strokeDasharray={length} strokeDashoffset={length * (1 - percent / 100)}
        style={{ transition: "stroke-dashoffset .6s ease-out" }}
      />
    </svg>
  )
}

// The dock floating over the thread: queued messages, the pane's strips (children), and the message field.
export function Composer({
  composer, machine, sessionId, info, parent, history, chatConfig, commands, contextPercent, compact,
  starting, resumable, needsUpdate, stopping, onStop, onFork, onSwitchTerminal, onGoalCommand,
  threadScrollRef, stickToBottomRef, children,
}: {
  composer: ReturnType<typeof useComposer>
  machine: string
  sessionId: string
  info?: SessionInfo
  parent: ThreadState
  history: ReturnType<typeof useHistoryPaging>
  chatConfig: ReturnType<typeof useChatConfig>
  commands: AgentCommand[]
  contextPercent?: number
  /** Absent when the agent doesn't offer compaction. */
  compact?: () => void
  starting: boolean
  resumable: boolean
  needsUpdate: boolean
  stopping: boolean
  onStop: () => void
  /** Absent when the chat can't be forked now. */
  onFork?: () => void
  onSwitchTerminal?: () => void
  onGoalCommand: (command: GoalCommand) => void
  threadScrollRef: RefObject<HTMLDivElement | null>
  stickToBottomRef: RefObject<boolean>
  children?: ReactNode
}) {
  const queryClient = useQueryClient()
  const { draft, setDraft, recalledPromptRef, inputRef, attachments, setAttachments, send, resume } = composer
  const { transcript, hasOlder, loadOlder, historyKey } = history
  const [hintIndex, setHintIndex] = useState(0)
  const [hintsClosed, setHintsClosed] = useState(false)
  // The caret, valid only for the draft it was read from; a draft set by code puts it at the end.
  const [caret, setCaret] = useState<{ draft: string; at: number }>()
  const backdropRef = useRef<HTMLDivElement>(null)
  const fileInputRef = useRef<HTMLInputElement>(null)
  const dockRef = useRef<HTMLDivElement>(null)
  const agentName = harnessName(info?.harness ?? "agent")
  const steering = info?.agent_capabilities?.steering === true

  // Opening a chat (the pane is keyed by session) puts the caret in the composer, unless focus is
  // already in another field. Touch screens skip it so the keyboard does not cover the thread.
  useEffect(() => {
    if (!matchMedia("(pointer: fine)").matches) return
    const active = document.activeElement
    if (active && active !== document.body && !active.closest("a, [data-source-row]")) return
    inputRef.current?.focus({ preventScroll: true })
  }, [inputRef])
  // The composer floats over the thread; the thread reserves exactly its height, so the last
  // message sits a short gap above it however many attachments or lines the composer holds.
  useEffect(() => {
    const dock = dockRef.current
    const thread = threadScrollRef.current
    if (!dock || !thread) return
    const observer = new ResizeObserver(() => {
      thread.style.setProperty("--composer-h", `${dock.offsetHeight}px`)
      if (stickToBottomRef.current) thread.scrollTop = thread.scrollHeight
    })
    observer.observe(dock)
    return () => {
      observer.disconnect()
      thread.style.removeProperty("--composer-h")
    }
  }, [threadScrollRef, stickToBottomRef])

  // The chat carries on with another agent, which gets the transcript with the next message.
  const switchAgent = useIntent({
    request: (harness: string, idempotencyKey) =>
      api(`/api/sessions/${encodeURIComponent(sessionId)}/harness`, { machine, method: "POST", idempotencyKey, body: { harness } }),
    accepted: () => queryClient.invalidateQueries({ queryKey: sessionsQuery(false).queryKey }),
    failed: (error, harness, retry) =>
      hud.error(conversationErrorMessage(machine, `switch to ${harnessName(harness)}`, error), conversationNeedsUpdate(error) ? undefined : retry),
  })
  const steerQueued = useIntent({
    request: (queueId: string, idempotencyKey) =>
      api(`/api/sessions/${encodeURIComponent(sessionId)}/conversation/queue/${encodeURIComponent(queueId)}/steer`, { machine, method: "POST", idempotencyKey }),
    failed: (error, _queueId, retry) => hud.error(
      conversationErrorMessage(machine, "steer with the queued message", error), conversationNeedsUpdate(error) ? undefined : retry,
    ),
  })
  const [uploadingFiles, setUploadingFiles] = useState<File[]>([])
  const upload = useMutation({
    mutationFn: async (file: File) => {
      const claimed = await pendingChatClaim(sessionId)
      return uploadConversationBlob(machine, claimed?.id ?? sessionId, file)
    },
    onSuccess: (value, file) => setAttachments((current) => [...current, { ...value, name: file.name }]),
    onError: (error, file) =>
      hud.error(
        conversationErrorMessage(machine, "attach images", error),
        conversationNeedsUpdate(error) ? undefined : () => void attachImages([file]),
      ),
  })

  const canSend = (draft.trim().length > 0 || attachments.length > 0) && uploadingFiles.length === 0 && !needsUpdate && !(resumable && attachments.length > 0) && !(resumable && resume.isPending)
  const nextQueued = parent.queue?.find((item) => !item.steer && !item.delivered)

  function submit(steer = false) {
    // Enter on an empty composer sends the waiting message now.
    if (nextQueued && !draft.trim() && attachments.length === 0 && !needsUpdate && !steerQueued.isPending) {
      steerQueued.mutate(nextQueued.queueId)
      return
    }
    if (!canSend) return
    recalledPromptRef.current = null
    const text = draft.trim()
    const goalCommand = attachments.length === 0 ? parseGoalCommand(text) : undefined
    if (goalCommand) {
      setDraft("")
      onGoalCommand(goalCommand)
      return
    }
    if (resumable) {
      resume.mutate(text)
      return
    }
    // Writing instead of answering an open question interrupts it, as in the Claude CLI.
    send.mutate({ text, attachments, clearComposer: true, steer: (steer || parent.needs_you) && parent.working })
  }

  const diffPaths = listDiffChanges(parent.items).map((change) => change.path)
  const caretAt = caret?.draft === draft ? caret.at : draft.length
  const beforeCaret = draft.slice(0, caretAt)
  const slashHints = filterSlashHints(beforeCaret, commands).filter((hint) => hint.value !== "/fork" || onFork)
  const atHints = filterAtHints(beforeCaret, diffPaths)
  const spans = draft.includes("/") ? commandSpans(draft, commands) : []
  const composerHints = hintsClosed ? [] : slashHints.length > 0 ? slashHints : atHints
  const activeHint = Math.min(hintIndex, Math.max(composerHints.length - 1, 0))
  const hintTitle = slashHints.length > 0 ? "Commands" : "Files in this thread"
  async function recallPrompt(direction: -1 | 1, input: HTMLTextAreaElement) {
    const current = recalledPromptRef.current
    const boundary = current?.text === draft ? current.seq : Infinity
    function findPrompt(items: ThreadItem[]) {
      const start = direction < 0 ? items.length - 1 : 0
      for (let i = start; i >= 0 && i < items.length; i += direction) {
        const item = items[i]!
        if (item.kind !== "user_message_chunk" || item.seq === undefined || (item.seq - boundary) * direction <= 0) continue
        const text = (item.data as UserMessagePayload).text?.text
        if (text?.trim()) return { seq: item.seq, text }
      }
    }
    let next = findPrompt(transcript.items)
    if (!next && direction < 0 && hasOlder) {
      const loaded = await loadOlder(true, (rows) => Boolean(findPrompt(reduceThread(historyKey, rows).items)))
      if (!loaded || inputRef.current !== input || input.value !== draft || recalledPromptRef.current !== current) return
      next = findPrompt(reduceThread(historyKey, loaded).items)
    }
    if (!next && direction < 0) return
    recalledPromptRef.current = next ?? null
    setHintsClosed(Boolean(next))
    setCaret(undefined)
    setDraft(next?.text ?? "")
  }
  function pickHint(hint: ComposerHint) {
    setHintIndex(0)
    if (hint.value === "/fork") {
      onFork?.()
      setDraft("")
      return
    }
    if (hint.value === "/goal" && draft.trim() === "/goal") {
      setDraft("/goal ")
      return
    }
    if (hint.value === "/terminal") {
      onSwitchTerminal?.()
      setDraft("")
      return
    }
    const head = applyHint(beforeCaret, hint)
    const tail = draft.slice(caretAt)
    const next = head + (tail.startsWith(" ") ? tail.slice(1) : tail)
    setDraft(next)
    setCaret({ draft: next, at: head.length })
    requestAnimationFrame(() => inputRef.current?.setSelectionRange(head.length, head.length))
  }
  async function attachImages(files: File[]) {
    if (files.length === 0 || needsUpdate) return
    if (uploadingFiles.length > 0) return
    setUploadingFiles(files)
    for (const file of files) {
      try {
        await upload.mutateAsync(file)
      } catch {
        // The upload offers a retry for this file. Keep uploading the rest.
      }
      setUploadingFiles((current) => current.filter((item) => item !== file))
    }
  }

  return (
    <div
      className="pointer-events-none absolute inset-x-0 bottom-0 z-20 px-(--s4) pt-(--s7) pb-(--s5)"
      style={{ backgroundImage: "linear-gradient(to top, var(--window) 55%, transparent)" }}
      onDragOver={(event) => {
        if (event.dataTransfer.types.includes("Files")) event.preventDefault()
      }}
      onDrop={(event) => {
        event.preventDefault()
        void attachImages(imageFilesFromList(event.dataTransfer.files))
      }}
    >
      <div ref={dockRef} className="pointer-events-auto mx-auto flex w-full max-w-180 flex-col gap-(--s3)">
        <QueueChips
          queue={parent.queue ?? []}
          machine={machine}
          sessionId={sessionId}
          readOnly={needsUpdate}
          steering={steerQueued.isPending ? steerQueued.input : undefined}
          steerJoins={steering}
          onSteer={steerQueued.mutate}
        />
        {children}
        <div className="flex flex-col gap-(--s1) rounded-[12px] bg-(--field) px-(--s3) pt-(--s3) pb-(--s2) shadow-(--shadow-composer) transition-shadow focus-within:shadow-[var(--shadow-composer),0_0_0_3px_var(--accent-soft)]">
          <div className="relative flex min-h-(--ctl-h) flex-col gap-(--s2)">
            {attachments.map((item, index) => (
              <div key={`${item.sha}-${index}`} className="flex items-center gap-(--s2) rounded-(--r-ctl) border-[0.5px] border-(--sep) bg-(--window) p-(--s2)">
                <img src={item.previewUrl} alt="" className="h-12 w-16 rounded-(--r-row) object-cover" />
                <span className="min-w-0 flex-1 truncate text-(length:--fs-xs) text-(--fg2)">Image attached · {item.name}</span>
                <Button type="button" size="sm" variant="ghost" aria-label={`Remove ${item.name}`} disabled={send.isPending} onClick={() => setAttachments((current) => current.filter((_, attachmentIndex) => attachmentIndex !== index))}>
                  Remove
                </Button>
              </div>
            ))}
            {uploadingFiles.map((file, index) => (
              <p key={index} role="status" className="truncate px-(--s2) text-(length:--fs-xs) text-(--fg2)">Attaching {file.name}…</p>
            ))}
            <ComposerHintsMenu id="composer-hints-list" activeIndex={activeHint} title={hintTitle} hints={composerHints} onPick={pickHint} query={slashQuery(beforeCaret)?.query ?? ""} agentName={agentName} />
            <div className="relative">
              {spans.some((span) => span.command) && (
                // Marks commands behind the transparent textarea, which cannot style its own text.
                <div ref={backdropRef} aria-hidden className="pointer-events-none absolute inset-0 overflow-hidden px-(--s3) py-(--s2) text-(length:--fs) wrap-break-word whitespace-pre-wrap text-transparent">
                  {spans.map((span, index) => span.command ? <mark key={index} data-command className="rounded-[3px] bg-(--accent-soft) text-transparent">{span.text}</mark> : span.text)}{" "}
                </div>
              )}
              <textarea
                ref={inputRef}
                id="conversation-message"
                aria-label="Message"
                rows={1}
                placeholder={needsUpdate ? "Update this peer to open the conversation…" : starting ? `Starting ${agentName}…` : nextQueued ? "Queue another message, or press ↵ to steer with the queued one" : parent.working ? `Queue a message for ${agentName} (⌘↵ ${steering ? "steers" : "interrupts to steer"})` : `Message ${agentName}`}
                value={draft}
                disabled={needsUpdate}
                aria-controls={composerHints.length > 0 ? "composer-hints-list" : undefined}
                aria-activedescendant={composerHints.length > 0 ? `composer-hints-list-${activeHint}` : undefined}
                onScroll={(event) => { if (backdropRef.current) backdropRef.current.scrollTop = event.currentTarget.scrollTop }}
                onSelect={(event) => setCaret({ draft: event.currentTarget.value, at: event.currentTarget.selectionStart })}
                onChange={(event) => {
                  recalledPromptRef.current = null
                  setDraft(event.currentTarget.value)
                  setCaret({ draft: event.currentTarget.value, at: event.currentTarget.selectionStart })
                  setHintIndex(0)
                  setHintsClosed(false)
                }}
                onPaste={(event) => {
                  const files = imageFilesFromClipboard(event.clipboardData)
                  if (files.length > 0) {
                    event.preventDefault()
                    void attachImages(files)
                  }
                }}
                onKeyDown={(event) => {
                  if (event.nativeEvent.isComposing || event.keyCode === 229) return
                  if (composerHints.length > 0) {
                    const move = event.key === "ArrowDown" ? 1 : event.key === "ArrowUp" ? -1 : 0
                    if (move) {
                      event.preventDefault()
                      setHintIndex(moveHint(activeHint, move, composerHints.length))
                      return
                    }
                    if (event.key === "Tab" || (event.key === "Enter" && !event.shiftKey)) {
                      event.preventDefault()
                      pickHint(composerHints[activeHint]!)
                      return
                    }
                    if (event.key === "Escape") {
                      event.preventDefault()
                      setHintsClosed(true)
                      return
                    }
                  }
                  if (!event.altKey && !event.ctrlKey && !event.metaKey && !event.shiftKey &&
                    ((event.key === "ArrowUp" && !draft) ||
                      ((event.key === "ArrowUp" || event.key === "ArrowDown") && recalledPromptRef.current?.text === draft))) {
                    event.preventDefault()
                    void recallPrompt(event.key === "ArrowUp" ? -1 : 1, event.currentTarget)
                    return
                  }
                  if (event.key === "Escape" && parent.working && !stopping) {
                    event.preventDefault()
                    onStop()
                    return
                  }
                  if (event.key === "Enter" && !event.shiftKey) {
                    event.preventDefault()
                    submit(event.metaKey || event.ctrlKey)
                  }
                }}
                className="relative block min-h-(--ctl-h) max-h-45 w-full resize-none border-0 bg-transparent px-(--s3) py-(--s2) text-(length:--fs) text-(--fg) outline-none placeholder:text-(--fg3)"
              />
            </div>
          </div>
          <div className="flex flex-wrap items-center gap-(--s1)">
            <input
              ref={fileInputRef}
              type="file"
              accept="image/png,image/jpeg,image/gif,image/webp"
              multiple
              className="sr-only"
              onChange={(event) => {
                const files = imageFilesFromList(event.currentTarget.files)
                event.currentTarget.value = ""
                void attachImages(files)
              }}
            />
            <Button
              type="button"
              size="icon"
              variant="ghost"
              aria-label="Attach image"
              disabled={uploadingFiles.length > 0 || needsUpdate}
              onClick={() => fileInputRef.current?.click()}
            >
              <Plus aria-hidden />
            </Button>
            <Button
              type="button"
              size="icon"
              variant="ghost"
              aria-label="Mention a file"
              disabled={needsUpdate}
              onClick={() => setDraft((current) => `${current}${current && !current.endsWith(" ") ? " " : ""}@`)}
            >
              <AtSign aria-hidden />
            </Button>
            <Button
              type="button"
              size="icon"
              variant="ghost"
              aria-label="Commands"
              disabled={needsUpdate}
              onClick={() => setDraft("/")}
            >
              <Slash aria-hidden />
            </Button>
            <span className="min-w-0 flex-1" />
            {info?.transport === "acp" && (
              <Menu>
                <MenuTrigger
                  render={
                    <Button type="button" size="sm" variant="ghost" title="Continue this chat with another agent" disabled={switchAgent.isPending || starting || needsUpdate} className="gap-(--s2)">
                      {switchAgent.isPending && switchAgent.input ? `Switching to ${harnessName(switchAgent.input)}…` : agentName}
                      <ChevronDown aria-hidden />
                    </Button>
                  }
                />
                <MenuContent align="end">
                  <p className="px-[11px] pt-(--s2) pb-(--s1) text-(length:--fs-xs) font-semibold text-(--fg3)">Continue with</p>
                  {agentIds.map((id) => (
                    <MenuItem
                      key={id}
                      role="menuitemradio"
                      aria-checked={id === info.harness}
                      icon={id === info.harness ? <Check aria-hidden /> : <span aria-hidden className="size-[15px]" />}
                      disabled={id === info.harness}
                      onClick={() => switchAgent.mutate(id)}
                    >
                      {harnessName(id)}
                    </MenuItem>
                  ))}
                </MenuContent>
              </Menu>
            )}
            <ChatConfigControls config={chatConfig} harness={info?.harness} starting={starting} needsYou={parent.needs_you} needsUpdate={needsUpdate} />
            {contextPercent !== undefined && (
              <button
                type="button"
                data-slot="context-ring"
                aria-label={`Context ${contextPercent}% used${compact ? ". Compact" : ""}`}
                title={compact ? `Context ${contextPercent}% used. Click to compact; ⌥-click to choose what to keep.` : `Context ${contextPercent}% used. ${agentName} doesn't offer compaction.`}
                disabled={!compact || needsUpdate}
                onClick={(event) => {
                  if (event.altKey) {
                    setDraft("/compact ")
                    inputRef.current?.focus()
                  } else compact?.()
                }}
                className="grid size-6 shrink-0 place-items-center rounded-full hover:bg-(--hover) disabled:hover:bg-transparent"
              >
                <ContextRing percent={contextPercent} />
              </button>
            )}
            {parent.working && (
              <Button
                type="button"
                size="icon"
                variant="ghost"
                disabled={stopping}
                aria-label={stopping ? "Stopping…" : "Stop"}
                title={stopping ? "Stopping…" : "Stop (Esc)"}
                onClick={onStop}
              >
                {stopping ? <Spinner /> : <Square aria-hidden className="fill-current" />}
              </Button>
            )}
            {parent.working && canSend && (
              <Button type="button" size="sm" variant="ghost" title={steering ? "Send this into the agent's current turn now (⌘↵)" : "Interrupt the agent and send this now (⌘↵)"} onClick={() => submit(true)}>
                <CornerDownLeft aria-hidden /> Steer
              </Button>
            )}
            {(!parent.working || canSend) && (
              <button
                type="button"
                disabled={!canSend}
                aria-label={parent.working ? "Queue" : "Send"}
                title={parent.working ? "Queue for after this turn (↵)" : "Send (↵)"}
                className="ml-(--s2) grid size-6 shrink-0 place-items-center rounded-full bg-(--accent) text-(--accent-fg) disabled:bg-(--track) disabled:text-(--fg3) [&_svg]:size-(--s4)"
                onClick={() => submit()}
              >
                <ArrowUp aria-hidden />
              </button>
            )}
          </div>
        </div>
      </div>
    </div>
  )
}

export function chatTiming(name: "chat-first-frame" | "prompt-confirmed", start: number, report: boolean) {
  const key = `agentpack:${name}`
  performance.clearMeasures(key)
  const { duration } = performance.measure(key, { start })
  if (report) {
    void api("/api/debug/timings", { method: "POST", body: { name, duration_ms: duration } }).catch(() => undefined)
  }
}

function confirmedPrompts(entries: LogEntry[]): Set<string> {
  const received = new Set<string>()
  for (const { kind, payload } of entries) {
    if (kind !== "prompt" && kind !== "queued_prompt" && kind !== "dequeued_prompt") continue
    const id = (payload as { queueId?: string }).queueId
    if (id) received.add(id)
  }
  return received
}

// Messages on their way show at the end of the thread until the log records them.
export function withSending(items: ThreadItem[], entries: LogEntry[], sending: PromptIntent[]): ThreadItem[] {
  if (!sending.length) return items
  const received = confirmedPrompts(entries)
  return [...items, ...sending.filter((item) => item.text && !received.has(item.id)).map((item): ThreadItem =>
    ({ kind: "user_message_chunk", data: { text: { text: item.text } } }),
  )]
}
