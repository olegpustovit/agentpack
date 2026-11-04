import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { useNavigate } from "@tanstack/react-router"
import { useEffect, useRef, useState } from "react"
import { AtSign, Check, ChevronDown, ChevronRight, Circle, CircleDot, PanelRight, Paperclip, SendHorizontal, Slash } from "lucide-react"
import { Menu, MenuContent, MenuItem, MenuTrigger } from "@/components/kit/overlays"
import { Meter } from "@/components/kit/data"
import { Banner, Spinner } from "@/components/kit/feedback"
import { PopupSelect } from "@/components/kit/forms"
import { StatusDot } from "@/components/kit/status-dot"
import { Button } from "@/components/ui/button"
import { Switch } from "@/components/ui/switch"
import { ToolbarSlot, ToolbarTitle } from "@/features/shell/toolbar-slot"
import { StatusSlot } from "@/features/status/status-bar"
import { sessionsQuery } from "@/features/sessions/queries"
import type { SessionInfo } from "@/lib/api-types"
import { harnessName } from "@/lib/format"
import { hud } from "@/lib/hud"
import { api, LOCAL } from "@/lib/api"
import { conversationEventsQuery, conversationErrorMessage, conversationNeedsUpdate } from "./queries"
import { configRole, parseConfigOptions } from "./config-options"
import type { LogEntry, ThreadItem } from "./model"
import { reduceLog } from "./reducer"
import { latestUserPrompt, quoteMessage, ThreadView } from "./thread-view"
import { ConversationInspector } from "./conversation-inspector"
import { applyHint, filterAtHints, filterSlashHints, type ComposerHint } from "./composer-hints"
import { ComposerHintsMenu } from "./composer-hints-menu"
import { diffAnchorId, listDiffChanges } from "./diff-summary"
import { uploadConversationBlob, type PendingAttachment } from "./upload-blob"
import { imageFilesFromClipboard, imageFilesFromList, promptRequest, retryPromptInput } from "./composer-attach"

type UploadedAttachment = PendingAttachment & { name: string }
type SendInput = {
  text: string
  attachments: (Pick<PendingAttachment, "sha" | "mime" | "width" | "height"> & { name?: string })[]
  resourceLink?: { sha: string; name: string }
  clearComposer: boolean
}
type PlanEntry = { id?: string; status?: string; content?: string }

function currentPlan(items: ThreadItem[]): PlanEntry[] {
  let plan: PlanEntry[] = []
  for (const item of items) {
    if (item.kind === "plan") plan = (item.data as { entries?: PlanEntry[] } | undefined)?.entries ?? []
  }
  return plan
}
// Loaded on demand so the conversation UI stays out of the initial bundle.
export default function ConversationPane({
  machine,
  sessionId,
  info,
  title,
  subtitle,
  onSwitchTerminal,
}: {
  machine: string
  sessionId: string
  info?: SessionInfo
  title?: string
  subtitle?: string
  onSwitchTerminal?: () => void
}) {
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const [draft, setDraft] = useState("")
  const [attachments, setAttachments] = useState<UploadedAttachment[]>([])
  const [inspectorOpen, setInspectorOpen] = useState(() => window.innerWidth > 1240)
  const fileInputRef = useRef<HTMLInputElement>(null)
  const composerInputRef = useRef<HTMLTextAreaElement>(null)
  const threadScrollRef = useRef<HTMLDivElement>(null)
  const scrolledSessionRef = useRef("")
  const [configValues, setConfigValues] = useState<Record<string, string | boolean>>({})
  const live = info?.state.running === true
  const { data: entries = [], error: conversationError } = useQuery({ ...conversationEventsQuery(machine, sessionId, live), retry: false })
  const { data: configRaw, error: configError } = useQuery({
    queryKey: ["conversation-config", machine, sessionId],
    queryFn: () => api<unknown>(`/api/sessions/${encodeURIComponent(sessionId)}/conversation/config-options`, { machine }),
    retry: false,
  })
  const needsUpdate = conversationNeedsUpdate(conversationError, configError)
  const configOptions = parseConfigOptions(configRaw)
  const selectOptions = configOptions.filter((option) => option.type === "select")
  const permissionsOption = selectOptions.find((option) => configRole(option) === "permissions")
  const modelOption = selectOptions.find((option) => configRole(option) === "model")
  const effortOption = selectOptions.find((option) => configRole(option) === "effort")
  const extraOptions = configOptions.filter((option) => configRole(option) === "other")
  useEffect(() => {
    if (!live || needsUpdate) return
    const url = new URL(`/api/sessions/${encodeURIComponent(sessionId)}/conversation`, location.origin)
    url.searchParams.set("stream", "1")
    if (machine && machine !== LOCAL) url.searchParams.set("node", machine)
    const source = new EventSource(url)
    source.onmessage = (event) => {
      if (!event.data) return
      let entry: LogEntry
      try {
        entry = JSON.parse(event.data) as LogEntry
      } catch {
        return
      }
      queryClient.setQueryData<LogEntry[]>(["conversation", machine, sessionId], (current) => {
        const rows = current ?? []
        if (rows.some((row) => row.seq === entry.seq)) return rows
        return [...rows, entry]
      })
    }
    return () => source.close()
  }, [live, machine, needsUpdate, queryClient, sessionId])

  const thread = reduceLog(entries)
  useEffect(() => {
    const element = threadScrollRef.current
    if (!element || entries.length === 0) return
    const nearLatest = element.scrollHeight - element.scrollTop - element.clientHeight < 160
    if (scrolledSessionRef.current !== sessionId || nearLatest) element.scrollTop = element.scrollHeight
    scrolledSessionRef.current = sessionId
  }, [entries, sessionId])
  useEffect(() => {
    const input = composerInputRef.current
    if (!input) return
    input.style.height = "auto"
    input.style.height = `${Math.min(input.scrollHeight, 180)}px`
  }, [draft])
  const plan = currentPlan(thread.items)
  const planDone = plan.filter((entry) => entry.status === "completed").length
  const currentStep =
    plan.find((entry) => entry.status === "in_progress") ??
    plan.find((entry) => entry.status === "pending") ??
    plan.at(-1)
  const setConfig = useMutation({
    mutationFn: ({ key, value }: { key: string; value: string | boolean }) =>
      api(`/api/sessions/${encodeURIComponent(sessionId)}/conversation/config`, {
        machine,
        method: "POST",
        body: { key, value },
      }),
    onError: (error, { key, value }) =>
      hud.error(
        conversationErrorMessage(machine, `set ${key}`, error),
        conversationNeedsUpdate(error) ? undefined : () => setConfig.mutate({ key, value }),
      ),
  })
  function setConfigValue(key: string, value: string | boolean) {
    setConfigValues((current) => ({ ...current, [key]: value }))
    setConfig.mutate({ key, value })
  }
  const fork = useMutation({
    mutationFn: () => {
      if (!info?.agent_session_id || !info.agent_capabilities?.sessionCapabilities?.fork) {
        throw new Error("Agent does not support conversation forks")
      }
      return api<SessionInfo>(`/api/sessions/${encodeURIComponent(sessionId)}/fork`, { machine, method: "POST" })
    },
    onSuccess: (next) => {
      void queryClient.invalidateQueries({ queryKey: sessionsQuery(machine, false).queryKey })
      void navigate({ to: "/sessions/$machine/$sessionId", params: { machine, sessionId: next.id } })
    },
    onError: (error) =>
      hud.error(
        conversationErrorMessage(machine, "fork a conversation", error),
        conversationNeedsUpdate(error) ? undefined : () => fork.mutate(),
      ),
  })
  const cancelTurn = useMutation({
    mutationFn: () =>
      api(`/api/sessions/${encodeURIComponent(sessionId)}/conversation/cancel`, { machine, method: "POST" }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["conversation", machine, sessionId] })
    },
    onError: (error) =>
      hud.error(
        conversationErrorMessage(machine, "stop the conversation", error),
        conversationNeedsUpdate(error) ? undefined : () => cancelTurn.mutate(),
      ),
  })
  const send = useMutation({
    mutationFn: (input: SendInput) => {
      const request = promptRequest(
        input.text,
        input.attachments,
        info?.agent_capabilities?.promptCapabilities?.image === true,
        input.resourceLink,
      )
      return api(`/api/sessions/${encodeURIComponent(sessionId)}/conversation/${request.path}`, {
        machine,
        method: "POST",
        body: request.body,
      })
    },
    onSuccess: (_, input) => {
      if (input.clearComposer) {
        setDraft("")
        setAttachments([])
      }
      void queryClient.invalidateQueries({ queryKey: ["conversation", machine, sessionId] })
    },
    onError: (error, input) =>
      hud.error(
        conversationErrorMessage(machine, input.attachments.length > 1 ? "send multiple images" : "send a message", error),
        conversationNeedsUpdate(error) ? undefined : () => send.mutate(input),
      ),
  })
  const upload = useMutation({
    mutationFn: (file: File) => uploadConversationBlob(machine, sessionId, file),
    onError: (error, file) =>
      hud.error(
        conversationErrorMessage(machine, "attach images", error),
        conversationNeedsUpdate(error) ? undefined : () => upload.mutate(file),
      ),
  })
  const retryInput = (() => {
    const prompt = latestUserPrompt(thread.items)
    return prompt
      ? retryPromptInput(prompt, info?.agent_capabilities?.promptCapabilities?.image === true)
      : undefined
  })()
  const canRetry = Boolean(retryInput && !thread.needs_you && !needsUpdate && !send.isPending)
  const canFork = Boolean(!needsUpdate && info?.agent_session_id && info.agent_capabilities?.sessionCapabilities?.fork)
  function retryLastPrompt() {
    if (retryInput && canRetry) {
      send.mutate({
        text: retryInput.text,
        attachments: retryInput.attachments,
        resourceLink: retryInput.resourceLink,
        clearComposer: false,
      })
    }
  }
  const canSend = (draft.trim().length > 0 || attachments.length > 0) && !send.isPending && !upload.isPending && !thread.needs_you && !needsUpdate
  const diffPaths = listDiffChanges(thread.items).map((change) => change.path)
  const slashHints = filterSlashHints(draft).filter((hint) => hint.value !== "/fork" || (canFork && !fork.isPending))
  const atHints = filterAtHints(draft, diffPaths)
  const composerHints = slashHints.length > 0 ? slashHints : atHints
  const hintTitle =
    slashHints.length > 0 ? `Commands from ${harnessName(info?.harness ?? "agent")}` : "Files in this thread"
  function pickHint(hint: ComposerHint) {
    if (hint.value === "/fork") {
      if (canFork && !fork.isPending) fork.mutate()
      setDraft("")
      return
    }
    if (hint.value === "/terminal") {
      onSwitchTerminal?.()
      setDraft("")
      return
    }
    setDraft(applyHint(draft, hint))
    if (hint.value === "/review") {
      const path = diffPaths[0]
      if (path) document.getElementById(diffAnchorId(path))?.scrollIntoView({ behavior: "smooth", block: "center" })
    }
  }
  function quoteReply(text: string) {
    setDraft((current) => `${current}${current ? "\n\n" : ""}${quoteMessage(text)}\n`)
    composerInputRef.current?.focus()
  }
  async function attachImages(files: File[]) {
    if (files.length === 0 || thread.needs_you || needsUpdate) return
    if (send.isPending) {
      hud.error("Wait for the message to finish sending before adding images.")
      return
    }
    if (upload.isPending) {
      hud.error("Wait for the current image upload to finish.")
      return
    }
    for (const file of files) {
      try {
        const value = await upload.mutateAsync(file)
        setAttachments((current) => [...current, { ...value, name: file.name }])
      } catch {
        return
      }
    }
  }
  const statusState = thread.needs_you ? "needsYou" : thread.working ? "running" : "idle"
  const usageLabel =
    thread.context_limit && thread.context_tokens !== undefined
      ? `${thread.context_tokens.toLocaleString()} / ${thread.context_limit.toLocaleString()} context`
      : undefined

  return (
    <div className="relative flex h-full min-h-0 flex-col bg-(--window)">
      <StatusSlot>
        {usageLabel && (
          <span className="flex min-w-0 flex-1 items-center gap-(--s3)">
            <Meter value={thread.context_tokens} max={thread.context_limit} label={usageLabel} />
            <span className="shrink-0 tabular-nums">{usageLabel}</span>
            {thread.cost_usd !== undefined && thread.cost_usd > 0 && (
              <span className="shrink-0 tabular-nums">${thread.cost_usd.toFixed(2)}</span>
            )}
          </span>
        )}
        {!usageLabel && info && (
          <span>
            {[harnessName(info.harness), info.mode || "default"].filter(Boolean).join(" · ")}
          </span>
        )}
      </StatusSlot>
      {title && <ToolbarTitle title={title} subtitle={subtitle} />}
      <ToolbarSlot>
        <Button
          type="button"
          variant="ghost"
          size="icon"
          aria-label={inspectorOpen ? "Hide session details" : "Show session details"}
          aria-controls="conversation-inspector-content"
          aria-expanded={inspectorOpen}
          title={inspectorOpen ? "Hide session details" : "Show session details"}
          onClick={() => setInspectorOpen((open) => !open)}
        >
          <PanelRight aria-hidden />
        </Button>
      </ToolbarSlot>
      <div className="flex min-h-0 min-w-0 flex-1">
        <div ref={threadScrollRef} tabIndex={0} aria-label="Conversation" className="min-h-0 min-w-0 flex-1 overflow-auto px-(--s4) py-(--s4) pb-64">
          <div className="mx-auto w-full max-w-180">
            {!needsUpdate && (
              <div className="mb-(--s4) flex items-center gap-(--s3)">
                {thread.working ? <Spinner /> : <StatusDot state={statusState} />}
                {thread.working && (
                  <Button type="button" size="sm" variant="ghost" disabled={cancelTurn.isPending} onClick={() => cancelTurn.mutate()}>
                    Stop
                  </Button>
                )}
              </div>
            )}
            {needsUpdate ? (
              <Banner tone="warn">
                {machine === LOCAL ? "The gateway needs an update to show this conversation." : "This peer needs an update to show this conversation."}
              </Banner>
            ) : (
              <ThreadView
                items={thread.items}
                machine={machine}
                sessionId={sessionId}
                onQuote={quoteReply}
                onFork={canFork && !fork.isPending ? () => fork.mutate() : undefined}
                onRetry={canRetry ? retryLastPrompt : undefined}
              />
            )}
          </div>
        </div>
        <ConversationInspector thread={thread} info={info} machine={machine} open={inspectorOpen} />
      </div>
      <div
        className="pointer-events-none absolute inset-x-0 bottom-0 z-20 px-(--s4) pt-36 pb-(--s5)"
        style={{ backgroundImage: "linear-gradient(to top, var(--window) 55%, transparent)" }}
        onDragOver={(event) => {
          if (event.dataTransfer.types.includes("Files")) event.preventDefault()
        }}
        onDrop={(event) => {
          event.preventDefault()
          void attachImages(imageFilesFromList(event.dataTransfer.files))
        }}
      >
          <div className="pointer-events-auto mx-auto flex w-full max-w-180 flex-col gap-(--s3)">
            {plan.length > 0 && (
              <details data-slot="plan-pill" className="group overflow-hidden rounded-(--r-pop) bg-(--glass) backdrop-blur-md" style={{ boxShadow: "var(--shadow-composer)" }}>
                <summary className="flex min-h-(--row-h) cursor-default list-none items-center gap-(--s3) px-(--s4) py-(--s2) text-(length:--fs-sm) marker:content-none [&::-webkit-details-marker]:hidden">
                  <span className="shrink-0 font-medium">{planDone} of {plan.length}</span>
                  <span className="min-w-0 flex-1 truncate text-(--fg2)">{currentStep?.content}</span>
                  <ChevronRight aria-hidden className="size-(--s4) shrink-0 text-(--fg3) transition-transform group-open:rotate-90" />
                </summary>
                <ol className="flex flex-col border-t-[0.5px] border-(--sep) px-(--s4) py-(--s2)">
                  {plan.map((step, index) => {
                    const completed = step.status === "completed"
                    const active = step.status === "in_progress"
                    const label = completed ? "Completed" : active ? "In progress" : "Pending"
                    return (
                      <li
                        key={step.id ?? index}
                        aria-label={`${label}: ${step.content ?? "Plan step"}`}
                        className={`flex min-h-(--row-h) items-center gap-(--s3) text-(length:--fs-sm) ${completed ? "text-(--fg3) line-through" : active ? "font-medium text-(--fg)" : "text-(--fg2)"}`}
                      >
                        <span className={`grid size-(--s4) shrink-0 place-items-center ${completed ? "text-(--ok-word)" : active ? "text-(--accent)" : "text-(--fg3)"}`}>
                          {completed ? (
                            <Check aria-hidden className="size-(--s4)" />
                          ) : active ? (
                            <CircleDot aria-hidden className="size-(--s4) animate-spin motion-reduce:animate-none" />
                          ) : (
                            <Circle aria-hidden className="size-(--s3)" />
                          )}
                        </span>
                        <span className="min-w-0">{step.content}</span>
                      </li>
                    )
                  })}
                </ol>
              </details>
            )}
            <div className="flex flex-col gap-(--s2) rounded-(--r-sheet) bg-(--field) p-(--s3) focus-within:ring-2 focus-within:ring-(--accent-soft)" style={{ boxShadow: "var(--shadow-composer)" }}>
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
                <ComposerHintsMenu title={hintTitle} hints={composerHints} onPick={pickHint} />
                <textarea
                  ref={composerInputRef}
                  id="conversation-message"
                  aria-label="Message"
                  rows={1}
                  placeholder={needsUpdate ? "Update this peer to open the conversation…" : thread.needs_you ? "Answer the permission card first…" : "Message the agent…"}
                  value={draft}
                  disabled={send.isPending || thread.needs_you || needsUpdate}
                  onChange={(event) => setDraft(event.currentTarget.value)}
                  onPaste={(event) => {
                    const files = imageFilesFromClipboard(event.clipboardData)
                    if (files.length > 0) {
                      event.preventDefault()
                      void attachImages(files)
                    }
                  }}
                  onKeyDown={(event) => {
                    if (event.key === "Enter" && !event.shiftKey) {
                      event.preventDefault()
                      if (canSend) send.mutate({ text: draft.trim(), attachments, clearComposer: true })
                    }
                  }}
                  className="min-h-(--ctl-h) max-h-45 w-full resize-none border-0 bg-transparent px-(--s3) py-(--s2) text-(length:--fs) text-(--fg) outline-none placeholder:text-(--fg3)"
                />
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
                  disabled={send.isPending || upload.isPending || thread.needs_you || needsUpdate}
                  onClick={() => fileInputRef.current?.click()}
                >
                  <Paperclip aria-hidden />
                </Button>
                <Button
                  type="button"
                  size="icon"
                  variant="ghost"
                  aria-label="Mention a file"
                  disabled={thread.needs_you || needsUpdate}
                  onClick={() => setDraft((current) => `${current}${current && !current.endsWith(" ") ? " " : ""}@`)}
                >
                  <AtSign aria-hidden />
                </Button>
                <Button
                  type="button"
                  size="icon"
                  variant="ghost"
                  aria-label="Commands"
                  disabled={thread.needs_you || needsUpdate}
                  onClick={() => setDraft("/")}
                >
                  <Slash aria-hidden />
                </Button>
                <span className="min-w-0 flex-1" />
                {permissionsOption && (
                  <PopupSelect
                    label="Permissions"
                    value={typeof configValues[permissionsOption.id] === "string" ? configValues[permissionsOption.id] as string : permissionsOption.value}
                    onValueChange={(value) => setConfigValue(permissionsOption.id, value)}
                    disabled={setConfig.isPending || thread.needs_you || needsUpdate}
                    items={permissionsOption.items}
                  />
                )}
                {(modelOption || effortOption) && (
                  <Menu>
                    <MenuTrigger
                      render={
                        <Button
                          type="button"
                          size="sm"
                          variant="ghost"
                          aria-label={effortOption ? "Model and effort" : modelOption?.name ?? "Model"}
                          title="Choose model and effort"
                          disabled={setConfig.isPending || thread.needs_you || needsUpdate}
                          className="max-w-48 gap-(--s2)"
                        >
                          <span className="min-w-0 truncate">
                            {modelOption
                              ? modelOption.items.find((item) => item.value === (typeof configValues[modelOption.id] === "string" ? configValues[modelOption.id] : modelOption.value))?.label ?? modelOption.value
                              : "Effort"}
                          </span>
                          {effortOption && (
                            <span className="shrink-0 text-(--fg3)">
                              {effortOption.items.find((item) => item.value === (typeof configValues[effortOption.id] === "string" ? configValues[effortOption.id] : effortOption.value))?.label ?? effortOption.value}
                            </span>
                          )}
                          <ChevronDown aria-hidden />
                        </Button>
                      }
                    />
                    <MenuContent align="end">
                      {modelOption && (
                        <>
                          <p className="px-(--s3) py-(--s2) text-(length:--fs-xs) text-(--fg3)">Model</p>
                          {modelOption.items.map((item) => {
                            const selected = (typeof configValues[modelOption.id] === "string" ? configValues[modelOption.id] : modelOption.value) === item.value
                            return (
                              <MenuItem
                                key={item.value}
                                role="menuitemradio"
                                aria-checked={selected}
                                icon={selected ? <Check aria-hidden /> : <span aria-hidden className="size-(--s4)" />}
                                disabled={setConfig.isPending || thread.needs_you || needsUpdate}
                                onClick={() => setConfigValue(modelOption.id, item.value)}
                              >
                                {item.label}
                              </MenuItem>
                            )
                          })}
                        </>
                      )}
                      {effortOption && (
                        <>
                          {modelOption && <div role="separator" className="my-(--s2) h-px bg-(--sep)" />}
                          <p className="px-(--s3) py-(--s2) text-(length:--fs-xs) text-(--fg3)">Effort</p>
                          {effortOption.items.map((item) => {
                            const selected = (typeof configValues[effortOption.id] === "string" ? configValues[effortOption.id] : effortOption.value) === item.value
                            return (
                              <MenuItem
                                key={item.value}
                                role="menuitemradio"
                                aria-checked={selected}
                                icon={selected ? <Check aria-hidden /> : <span aria-hidden className="size-(--s4)" />}
                                disabled={setConfig.isPending || thread.needs_you || needsUpdate}
                                onClick={() => setConfigValue(effortOption.id, item.value)}
                              >
                                {item.label}
                              </MenuItem>
                            )
                          })}
                        </>
                      )}
                    </MenuContent>
                  </Menu>
                )}
                {extraOptions.map((option) => option.type === "boolean" ? (
                  <label key={option.id} className="flex items-center gap-(--s2) text-(length:--fs-xs) text-(--fg2)">
                    <span>{option.name}</span>
                    <Switch
                      aria-label={option.name}
                      checked={typeof configValues[option.id] === "boolean" ? configValues[option.id] as boolean : option.value}
                      onCheckedChange={(value) => setConfigValue(option.id, value)}
                      disabled={setConfig.isPending || thread.needs_you || needsUpdate}
                    />
                  </label>
                ) : (
                  <PopupSelect
                    key={option.id}
                    label={option.name}
                    value={typeof configValues[option.id] === "string" ? configValues[option.id] as string : option.value}
                    onValueChange={(value) => setConfigValue(option.id, value)}
                    disabled={setConfig.isPending || thread.needs_you || needsUpdate}
                    items={option.items}
                  />
                ))}
                <Button
                  type="button"
                  size="icon"
                  variant="ghost"
                  disabled={!canSend}
                  aria-label="Send"
                  onClick={() => send.mutate({ text: draft.trim(), attachments, clearComposer: true })}
                >
                  <SendHorizontal aria-hidden />
                </Button>
              </div>
            </div>
          </div>
      </div>
    </div>
  )
}
