import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { useNavigate } from "@tanstack/react-router"
import { lazy, Suspense, useEffect, useLayoutEffect, useRef, useState } from "react"
import { ArrowLeft, ArrowUp, AtSign, Check, ChevronDown, CornerDownLeft, PanelRight, Plus, Slash, Square } from "lucide-react"
import { Menu, MenuContent, MenuItem, MenuTrigger } from "@/components/kit/overlays"
import { Banner, Spinner } from "@/components/kit/feedback"
import { PopupSelect } from "@/components/kit/forms"
import { Button } from "@/components/ui/button"
import { ToolbarSlot, ToolbarTitle } from "@/features/shell/toolbar-slot"
import { StatusSlot } from "@/features/status/status-bar"
import { sessionsQuery } from "@/features/sessions/queries"
import { clearFirstMessage, firstMessage, loadAgentChoices, markNewChat, rememberAgentChoice, takeNewChat } from "@/features/sessions/choices"
import { agentIds } from "@/features/sessions/rules"
import { useMachines } from "@/features/machines/queries"
import type { SessionInfo } from "@/lib/api-types"
import { harnessName, relativeTime } from "@/lib/format"
import { clock, goalElapsedMs, liveGoal } from "@/lib/goal"
import { useNow } from "@/lib/use-now"
import { cacheBuildID, restoreChatQuery, trackStreamingQuery } from "@/lib/persist"
import { hud } from "@/lib/hud"
import { api, ApiError, LOCAL } from "@/lib/api"
import { ompSettingsQuery, quickRoles } from "@/features/settings/omp-model"
import { archiveUnavailableMessage, conversationEventsQuery, conversationErrorMessage, conversationNeedsUpdate, joinHistory, mergeConversationPages, olderConversationPage, type ConversationPage } from "./queries"
import { configRole, dangerousMode, parseConfigOptions } from "./config-options"
import type { LogEntry, ThreadItem } from "./model"
import { helperSubagents, reduceThread } from "./reducer"
import { latestUserPrompt, quoteMessage, ThreadView, userImageSrc } from "./thread-view"
import { GoalPill, parseGoalCommand, useGoalActions } from "./goal-sheet"
import { QueueChips } from "./queue-chips"
import { subagentRunning } from "./subagent-card"
import { ConversationInspector } from "./conversation-inspector"
import { GitInspectorSections, GitToolbarActions, branchLine, useSessionGit } from "./session-git"
import { advertises, agentCommands, applyHint, cachedCommands, commandSpans, filterAtHints, filterSlashHints, moveHint, rememberCommands, slashQuery, type ComposerHint } from "./composer-hints"
import { ComposerHintsMenu } from "./composer-hints-menu"
import { listDiffChanges } from "./diff-summary"
import { loadAttachments, revokePreview, saveAttachments, uploadConversationBlob, type PendingAttachment } from "./upload-blob"
import { imageFilesFromClipboard, imageFilesFromList, promptRequest, retryPromptInput } from "./composer-attach"

type UploadedAttachment = PendingAttachment & { name: string }
type SendInput = {
  text: string
  /** Interrupt the running turn and send this next. */
  steer?: boolean
  attachments: (Pick<PendingAttachment, "sha" | "mime" | "width" | "height"> & { name?: string })[]
  resourceLink?: { sha: string; name: string }
  clearComposer: boolean
  /** Last log seq when sent; a prompt after it is this message, so the optimistic copy goes. Unset when queued. */
  optimisticAfter?: number
}
const GoalSheet = lazy(() => import("./goal-editor").then((module) => ({ default: module.GoalSheet })))
const draftKey = (machine: string, sessionId: string) => `agentpack:draft:${machine}/${sessionId}`
function loadDraft(key: string) {
  try {
    return sessionStorage.getItem(key) ?? ""
  } catch {
    return ""
  }
}
type PlanEntry = { id?: string; status?: string; content?: string }

function currentPlan(items: ThreadItem[]): PlanEntry[] {
  let plan: PlanEntry[] = []
  for (const item of items) {
    if (item.kind === "plan") plan = (item.data as { entries?: PlanEntry[] } | undefined)?.entries ?? []
  }
  return plan
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

// Loaded on demand so the conversation UI stays out of the initial bundle.
export default function ConversationPane({
  machine,
  sessionId,
  info,
  title,
  subtitle,
  onSwitchTerminal,
  readOnly: unavailable = false,
  subagent: subagentId,
  helpers = [],
}: {
  machine: string
  /** Helper chats this chat's agent started. */
  helpers?: SessionInfo[]
  sessionId: string
  /** A subagent open in the main view. */
  subagent?: string
  info?: SessionInfo
  title?: string
  subtitle?: string
  onSwitchTerminal?: () => void
  readOnly?: boolean
}) {
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const machines = useMachines()
  const [draft, setDraft] = useState(() => loadDraft(draftKey(machine, sessionId)) || firstMessage(sessionId))
  const [hintIndex, setHintIndex] = useState(0)
  const [hintsClosed, setHintsClosed] = useState(false)
  // The caret, valid only for the draft it was read from; a draft set by code puts it at the end.
  const [caret, setCaret] = useState<{ draft: string; at: number }>()
  const backdropRef = useRef<HTMLDivElement>(null)
  const stickToBottomRef = useRef(true)
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
  const [inspectorOpen, setInspectorOpen] = useState(() => window.innerWidth > 1240)
  const fileInputRef = useRef<HTMLInputElement>(null)
  const composerInputRef = useRef<HTMLTextAreaElement>(null)
  const threadScrollRef = useRef<HTMLDivElement>(null)
  const [configValues, setConfigValues] = useState<Record<string, string | boolean>>({})
  const readOnly = unavailable || info?.reachable === false || Boolean(info?.archived_at)
  // "+" opens the chat before the agent is up. A message sent meanwhile stays in the composer (and its
  // saved draft, so leaving the chat loses nothing) and goes out once the agent runs.
  const starting = !readOnly && info?.conversation_state === "starting" && !info.ended_at
  const live = !readOnly && !starting && info?.state.running === true
  // A stopped chat comes back on its next message: the gateway restarts it under the same ID, reloads
  // the agent's conversation and sends the message once it runs.
  // One that never started (its agent wasn't signed in, say) starts fresh the same way.
  const resumable = !readOnly && !starting && !live && Boolean(info?.ended_at)
  // A first message from New session waits in the composer, held like one typed while starting.
  const [held, setHeld] = useState(() => firstMessage(sessionId) !== "")
  const [streamConnected, setStreamConnected] = useState(false)
  const { data: tail, error: conversationError } = useQuery({
    ...conversationEventsQuery(machine, sessionId),
    enabled: info?.provisionalStart !== "claim",
    staleTime: streamConnected ? Infinity : undefined,
    structuralSharing: (previous, next) => mergeConversationPages(previous as ConversationPage | undefined, next as ConversationPage),
    retry: false,
  })
  useEffect(() => {
    const key = conversationEventsQuery(machine, sessionId).queryKey
    if (!queryClient.getQueryData(key)) void restoreChatQuery(queryClient, cacheBuildID || "dev", key)
  }, [machine, queryClient, sessionId])
  // Earlier pages, loaded as the thread scrolls up. Keyed, so another session never shows them.
  const historyKey = `${machine}\0${sessionId}`
  const [history, setHistory] = useState<{ key: string; entries: LogEntry[]; more: boolean; loading: boolean; error?: unknown }>()
  const older = history?.key === historyKey ? history : undefined
  const tailEntries = tail?.entries ?? []
  const entries = joinHistory(older?.entries ?? [], tailEntries)
  const hasOlder = (older ? older.more : Boolean(tail?.truncated)) && (entries[0]?.seq ?? 1) > 1
  // Scroll height and offset before older entries were prepended, so the reader stays on the same line.
  const scrollAnchorRef = useRef<{ height: number; top: number } | null>(null)
  const { data: configRaw, error: configError } = useQuery({
    queryKey: ["conversation-config", machine, sessionId],
    // Only a running agent has options; a stopped chat has no worker, and its 404 is not a version gap.
    enabled: live,
    queryFn: () => api<unknown>(`/api/sessions/${encodeURIComponent(sessionId)}/conversation/config-options`, { machine }),
    retry: false,
  })
  const needsUpdate = conversationNeedsUpdate(conversationError, live ? configError : undefined)
  const archiveUnavailable = archiveUnavailableMessage(conversationError)
  const configOptions = parseConfigOptions(configRaw)
  const selectOptions = configOptions.filter((option) => option.type === "select")
  const permissionsOption = selectOptions.find((option) => configRole(option) === "permissions")
  const modelOption = selectOptions.find((option) => configRole(option) === "model")
  // An Oh My Pi chat can switch to a role's model (and thinking), as set on the machine running it.
  const ompSettings = useQuery({ ...ompSettingsQuery(machine), enabled: live && info?.harness === "omp", retry: false }).data
  const roles = ompSettings && modelOption ? quickRoles(ompSettings, modelOption.items.map((item) => item.value)) : []
  const effortOption = selectOptions.find((option) => configRole(option) === "effort")
  const extraOptions = configOptions.filter((option) => configRole(option) === "other")
  const tailLoaded = tail !== undefined
  useEffect(() => {
    if (!live || needsUpdate || !tailLoaded) return
    const key = ["conversation", machine, sessionId]
    let source: EventSource | undefined
    let untrack: (() => void) | undefined
    const close = () => {
      source?.close()
      source = undefined
      untrack?.()
      untrack = undefined
      setStreamConnected(false)
    }
    const syncVisibility = () => {
      if (document.visibilityState === "hidden") {
        close()
        return
      }
      if (source) return
      const url = new URL(`/api/sessions/${encodeURIComponent(sessionId)}/conversation`, location.origin)
      url.searchParams.set("stream", "1")
      url.searchParams.set("since", String(queryClient.getQueryData<ConversationPage>(key)?.entries.at(-1)?.seq ?? 0))
      if (machine && machine !== LOCAL) url.searchParams.set("node", machine)
      source = new EventSource(url)
      untrack = trackStreamingQuery(key)
      source.onopen = () => setStreamConnected(true)
      source.onerror = () => setStreamConnected(false)
      source.onmessage = (event) => {
        if (!event.data) return
        let entry: LogEntry
        try {
          entry = JSON.parse(event.data) as LogEntry
        } catch {
          return
        }
        queryClient.setQueryData<ConversationPage>(key, (current) => {
          const rows = current?.entries ?? []
          if (entry.seq <= (rows.at(-1)?.seq ?? 0)) return current
          return { entries: [...rows, entry], truncated: current?.truncated ?? false }
        })
      }
      source.addEventListener("truncated", () => {
        queryClient.setQueryData<ConversationPage>(key, (current) => current && { ...current, truncated: true })
      })
    }
    document.addEventListener("visibilitychange", syncVisibility)
    syncVisibility()
    return () => {
      document.removeEventListener("visibilitychange", syncVisibility)
      close()
    }
  }, [live, machine, needsUpdate, queryClient, sessionId, tailLoaded])

  const parent = reduceThread(historyKey, entries)
  const subagent = subagentId ? parent.subagents?.[subagentId] : undefined
  const thread = subagent?.thread ?? parent
  // Entries already loaded on first paint are history and never animate.
  const [animateAfter, setAnimateAfter] = useState<number>()
  if (animateAfter === undefined && tail) setAnimateAfter(tail.entries.at(-1)?.seq ?? 0)
  const closeSubagent = () => void navigate({ to: ".", search: (prev: Record<string, unknown>) => ({ ...prev, sub: undefined }) })
  const openSubagent = (id: string) =>
    id.startsWith("helper:")
      ? void navigate({ to: "/sessions/$machine/$sessionId", params: { machine, sessionId: id.slice("helper:".length) } })
      : void navigate({ to: ".", search: (prev: Record<string, unknown>) => ({ ...prev, sub: id }) })
  useEffect(() => {
    if (!subagentId) return
    // Esc returns to the parent, unless a field or dialog is using it.
    const onKey = (event: KeyboardEvent) => {
      if (event.key !== "Escape" || event.defaultPrevented) return
      if ((event.target as HTMLElement | null)?.closest("input, textarea, [role=dialog], [role=menu]")) return
      void navigate({ to: ".", search: (prev: Record<string, unknown>) => ({ ...prev, sub: undefined }) })
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  }, [subagentId, navigate])
  useLayoutEffect(() => {
    const element = threadScrollRef.current
    if (!element || tailEntries.length === 0) return
    if (stickToBottomRef.current) element.scrollTop = element.scrollHeight
  }, [tailEntries])
  useLayoutEffect(() => {
    const element = threadScrollRef.current
    const anchor = scrollAnchorRef.current
    if (!element || !anchor) return
    scrollAnchorRef.current = null
    element.scrollTop = anchor.top + element.scrollHeight - anchor.height
  }, [older?.entries])

  // Scrolling never retries a failed page; the Retry button does.
  async function loadOlder(retry = false) {
    const first = entries[0]
    if (!first || !hasOlder || older?.loading || (older?.error && !retry)) return
    const kept = older?.entries ?? []
    setHistory({ key: historyKey, entries: kept, more: true, loading: true })
    try {
      const page = await olderConversationPage(machine, sessionId, first.seq)
      const element = threadScrollRef.current
      if (element) scrollAnchorRef.current = { height: element.scrollHeight, top: element.scrollTop }
      setHistory({ key: historyKey, entries: [...page.entries, ...kept], more: page.truncated, loading: false })
    } catch (error) {
      setHistory({ key: historyKey, entries: kept, more: true, loading: false, error })
    }
  }
  useEffect(() => {
    try {
      const key = draftKey(machine, sessionId)
      if (draft) sessionStorage.setItem(key, draft)
      else sessionStorage.removeItem(key)
    } catch {
      // Storage can be blocked; the draft then lives only in memory.
    }
  }, [draft, machine, sessionId])
  // Opening a chat (the pane is keyed by session) puts the caret in the composer, unless focus is
  // already in another field. Touch screens skip it so the keyboard does not cover the thread.
  const composerShown = !readOnly && !subagentId
  useEffect(() => {
    if (!composerShown || !matchMedia("(pointer: fine)").matches) return
    const active = document.activeElement
    if (active && active !== document.body && !active.closest("a, [data-source-row]")) return
    composerInputRef.current?.focus({ preventScroll: true })
  }, [composerShown])
  // The composer floats over the thread; the thread reserves exactly its height, so the last
  // message sits a short gap above it however many attachments or lines the composer holds.
  const composerRef = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const composer = composerRef.current
    const thread = threadScrollRef.current
    if (!composer || !thread) return
    const observer = new ResizeObserver(() => {
      thread.style.setProperty("--composer-h", `${composer.offsetHeight}px`)
      if (stickToBottomRef.current) thread.scrollTop = thread.scrollHeight
    })
    observer.observe(composer)
    return () => {
      observer.disconnect()
      thread.style.removeProperty("--composer-h")
    }
  }, [composerShown])
  useEffect(() => {
    const input = composerInputRef.current
    if (!input) return
    input.style.height = "auto"
    input.style.height = `${Math.min(input.scrollHeight, 180)}px`
  }, [draft])
  const plan = currentPlan(parent.items)
  // The live record is the log; the session row is the fallback for chats whose log is not loaded.
  const goal = parent.goal ?? (liveGoal(info?.goal) ? info?.goal : undefined)
  const goalNow = useNow(goal?.state === "active")
  const [goalSheet, setGoalSheet] = useState<{ objective: string }>()
  // The editor loads on first open and then stays mounted, so closing it still animates.
  const [goalEditorLoaded, setGoalEditorLoaded] = useState(false)
  if (goalSheet && !goalEditorLoaded) setGoalEditorLoaded(true)
  const goalActions = useGoalActions(machine, sessionId)
  const setConfig = useMutation({
    mutationFn: ({ key, value }: { key: string; value: string | boolean }) =>
      api(`/api/sessions/${encodeURIComponent(sessionId)}/conversation/config`, {
        machine,
        method: "POST",
        body: { key, value },
      }),
    onSuccess: (_, { key }) =>
      queryClient.invalidateQueries({ queryKey: ["conversation-config", machine, sessionId] }).then(() =>
        setConfigValues((values) => withoutKey(values, key)),
      ),
    onError: (error, { key, value }) => {
      setConfigValues((values) => withoutKey(values, key))
      hud.error(
        conversationErrorMessage(machine, `set ${key}`, error),
        conversationNeedsUpdate(error) ? undefined : () => setConfigValue(key, value),
      )
    },
  })
  function setConfigValue(key: string, value: string | boolean, remember = true) {
    setConfigValues((current) => ({ ...current, [key]: value }))
    setConfig.mutate({ key, value })
    const option = configOptions.find((candidate) => candidate.id === key)
    // Model and effort carry over to the next chat started in this folder with this agent.
    if (remember && info && option && (configRole(option) === "model" || configRole(option) === "effort")) rememberAgentChoice(info.directory, info.harness, key, value)
  }
  // A chat New session just started takes the folder's remembered model and effort, once.
  useEffect(() => {
    if (!info || readOnly || configOptions.length === 0 || !takeNewChat(sessionId)) return
    const remembered = loadAgentChoices(info.directory, info.harness)
    for (const option of configOptions) {
      const value = remembered[option.id]
      const role = configRole(option)
      if (value === undefined || value === option.value || (role !== "model" && role !== "effort")) continue
      if (option.type === "select" && !option.items.some((item) => item.value === value)) continue
      setConfigValue(option.id, value, false)
    }
  })
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
    onError: (error) =>
      hud.error(
        conversationErrorMessage(machine, "stop the conversation", error),
        conversationNeedsUpdate(error) ? undefined : () => cancelTurn.mutate(),
      ),
  })
  const stopSubagent = useMutation({
    mutationFn: (id: string) =>
      id.startsWith("helper:")
        ? api(`/api/sessions/${encodeURIComponent(id.slice("helper:".length))}/conversation/cancel`, { machine, method: "POST" })
        : api(`/api/sessions/${encodeURIComponent(sessionId)}/conversation/cancel`, { machine, method: "POST", body: { subagent: id } }),
    onError: (error, id) =>
      hud.error(conversationErrorMessage(machine, "stop the subagent", error), conversationNeedsUpdate(error) ? undefined : () => stopSubagent.mutate(id)),
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
        body: input.steer ? { ...request.body, steer: true } : request.body,
      })
    },
    // The composer clears as the message is sent; a failure puts it back.
    onMutate: (input) => {
      if (!input.clearComposer) return
      setDraft("")
      setAttachments(() => [])
    },
    onError: (error, input) => {
      if (input.clearComposer) setDraft((current) => current || input.text)
      hud.error(
        conversationErrorMessage(machine, input.steer ? "steer the agent" : input.attachments.length > 1 ? "send multiple images" : "send a message", error),
        conversationNeedsUpdate(error) ? undefined : () => send.mutate(input),
      )
    },
  })
  const resume = useMutation({
    mutationFn: (text: string) => api(`/api/sessions/${encodeURIComponent(sessionId)}/resume`, { machine, method: "POST", body: { text } }),
    onMutate: () => setDraft(""),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: sessionsQuery(machine, false).queryKey }),
    onError: (error, text) => {
      setDraft((current) => current || text)
      // An older peer can't start a chat whose agent never started.
      const old = error instanceof ApiError && error.status === 400 && error.message === "This chat cannot be resumed."
      hud.error(old ? `${machine === LOCAL ? "Gateway" : "Peer"} needs an update to restart this chat.` : conversationErrorMessage(machine, "resume the chat", error))
    },
  })
  // The chat carries on with another agent, which gets the transcript with the next message.
  const switchAgent = useMutation({
    mutationFn: (harness: string) => api(`/api/sessions/${encodeURIComponent(sessionId)}/harness`, { machine, method: "POST", body: { harness } }),
    onSuccess: () => {
      markNewChat(sessionId)
      return queryClient.invalidateQueries({ queryKey: sessionsQuery(machine, false).queryKey })
    },
    onError: (error, harness) =>
      hud.error(
        conversationErrorMessage(machine, `switch to ${harnessName(harness)}`, error),
        conversationNeedsUpdate(error) ? undefined : () => switchAgent.mutate(harness),
      ),
  })
  const steerQueued = useMutation({
    mutationFn: (queueId: string) =>
      api(`/api/sessions/${encodeURIComponent(sessionId)}/conversation/queue/${encodeURIComponent(queueId)}/steer`, { machine, method: "POST" }),
    onError: (error, queueId) =>
      hud.error(
        conversationErrorMessage(machine, "steer with the queued message", error),
        conversationNeedsUpdate(error) ? undefined : () => steerQueued.mutate(queueId),
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
  const canRetry = !readOnly && Boolean(retryInput && !thread.needs_you && !needsUpdate && !send.isPending)
  const canFork = !readOnly && Boolean(!needsUpdate && info?.agent_session_id && info.agent_capabilities?.sessionCapabilities?.fork)
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
  // Images upload to the running agent, so a starting chat holds text only.
  const canSend = !readOnly && (draft.trim().length > 0 || attachments.length > 0) && !(starting && (attachments.length > 0 || held)) && !send.isPending && !upload.isPending && !needsUpdate && !(resumable && attachments.length > 0)
  useEffect(() => {
    if (!held || !live && !info?.ended_at) return
    // A start that failed ends the row; the message simply stays in the composer.
    if (live && draft.trim()) send.mutate({ text: draft.trim(), attachments: [], clearComposer: true, optimisticAfter: entries.at(-1)?.seq ?? 0 })
    clearFirstMessage(sessionId)
    setHeld(false)
  }, [held, live, info?.ended_at, send, draft, entries, sessionId])
  // Enter sends, or queues while the agent works; steer interrupts the turn and sends this next.
  const nextQueued = parent.queue?.find((item) => !item.steer)
  function submit(steer = false) {
    // Enter on an empty composer sends the waiting message now.
    if (nextQueued && !draft.trim() && attachments.length === 0 && !readOnly && !needsUpdate && !steerQueued.isPending) {
      steerQueued.mutate(nextQueued.queueId)
      return
    }
    if (!canSend) return
    const text = draft.trim()
    const goalCommand = attachments.length === 0 ? parseGoalCommand(text) : undefined
    if (goalCommand) {
      setDraft("")
      if (goalCommand.kind === "sheet") setGoalSheet({ objective: "" })
      else if (goalCommand.kind === "action") goalActions.act.mutate(goalCommand.action)
      else goalActions.set.mutate(
        { goal: { objective: goalCommand.objective }, edit: false },
        { onError: (error) => hud.error(conversationErrorMessage(machine, "set the goal", error)) },
      )
      return
    }
    if (starting) {
      setHeld(true)
      return
    }
    if (resumable) {
      resume.mutate(text)
      return
    }
    // A message to an idle agent becomes the next prompt, so it can show before the gateway answers.
    // Writing instead of answering an open question interrupts it, as in the Claude CLI.
    send.mutate({ text, attachments, clearComposer: true, steer: (steer || parent.needs_you) && parent.working, optimisticAfter: parent.working ? undefined : entries.at(-1)?.seq ?? 0 })
  }
  // A merge conflict goes back to the agent, which resolves it in its own worktree.
  function askAgent(text: string) {
    if (resumable) resume.mutate(text)
    else send.mutate({ text, attachments: [], clearComposer: false })
  }
  const git = useSessionGit(machine, sessionId, info).data
  const branch = branchLine(git)
  function compact(focus?: string) {
    if (!canCompact || readOnly) return
    send.mutate({ text: focus ? `/compact ${focus}` : "/compact", attachments: [], clearComposer: false })
  }
  const advertised = agentCommands(entries)
  const commands = advertised ?? cachedCommands(info?.harness ?? "")
  const canCompact = advertises(commands, "compact")
  useEffect(() => {
    if (advertised && info?.harness) rememberCommands(info.harness, advertised)
  }, [advertised, info?.harness])
  const diffPaths = listDiffChanges(thread.items).map((change) => change.path)
  const caretAt = caret?.draft === draft ? caret.at : draft.length
  const beforeCaret = draft.slice(0, caretAt)
  const slashHints = filterSlashHints(beforeCaret, commands).filter((hint) => hint.value !== "/fork" || (canFork && !fork.isPending))
  const atHints = filterAtHints(beforeCaret, diffPaths)
  const spans = draft.includes("/") ? commandSpans(draft, commands) : []
  const composerHints = hintsClosed ? [] : slashHints.length > 0 ? slashHints : atHints
  const activeHint = Math.min(hintIndex, Math.max(composerHints.length - 1, 0))
  const hintTitle = slashHints.length > 0 ? "Commands" : "Files in this thread"
  function pickHint(hint: ComposerHint) {
    setHintIndex(0)
    if (hint.value === "/fork") {
      if (canFork && !fork.isPending) fork.mutate()
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
    requestAnimationFrame(() => composerInputRef.current?.setSelectionRange(head.length, head.length))
  }
  function quoteReply(text: string) {
    setDraft((current) => `${current}${current ? "\n\n" : ""}${quoteMessage(text)}\n`)
    composerInputRef.current?.focus()
  }
  async function attachImages(files: File[]) {
    if (readOnly || files.length === 0 || needsUpdate) return
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
  const agentName = harnessName(info?.harness ?? "agent")
  const machineName = machines.find((candidate) => candidate.id === machine)?.name ?? machine
  const contextPercent =
    parent.context_limit && parent.context_tokens !== undefined
      ? Math.min(100, Math.round((parent.context_tokens / parent.context_limit) * 100))
      : undefined


  return (
    <div className="relative flex h-full min-h-0 flex-col bg-(--window)">
      {/* The composer ring and an open inspector already show context, so the status bar adds it only while the inspector is closed. */}
      <StatusSlot>
        {info && (
          <span>
            {[harnessName(info.harness), info.mode || "default", goal ? `Goal ${goal.state === "active" ? "" : `${goal.state} `}${clock(goalElapsedMs(goal, goalNow))}` : "", contextPercent !== undefined && !inspectorOpen ? `Context ${contextPercent}%` : "", parent.cost_usd ? `$${parent.cost_usd.toFixed(2)}` : ""].filter(Boolean).join(" · ")}
          </span>
        )}
      </StatusSlot>
      {subagentId ? (
        <ToolbarTitle title={`${title ?? agentName} › ${subagent?.title || "Subagent"}`} subtitle={subagent?.description ?? subtitle} />
      ) : title && <ToolbarTitle title={title} subtitle={[subtitle, branch].filter(Boolean).join(" · ") || undefined} />}
      <ToolbarSlot>
        {!subagentId && <GitToolbarActions machine={machine} sessionId={sessionId} info={info} git={git} readOnly={readOnly || needsUpdate} />}
        {subagentId && (
          <Button type="button" variant="ghost" size="sm" aria-label="Back to the conversation" title="Back (Esc)" onClick={closeSubagent}>
            <ArrowLeft aria-hidden /> Back
          </Button>
        )}
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
      {readOnly && <Banner>{info?.archived_at ? "Archived, read-only" : `Offline, read-only${info?.last_sync ? ` · Last sync ${relativeTime(new Date(info.last_sync), Date.now())}` : ""}`}</Banner>}
      {archiveUnavailable && <Banner>{archiveUnavailable}</Banner>}
      <div className="flex min-h-0 min-w-0 flex-1">
        <div className="relative flex min-h-0 min-w-0 flex-1 flex-col">
        <div
          ref={threadScrollRef}
          onScroll={(event) => {
            const el = event.currentTarget
            stickToBottomRef.current = el.scrollHeight - el.scrollTop - el.clientHeight < 160
            // Fetch the previous page a screen before the reader reaches the top.
            if (el.scrollTop < el.clientHeight) void loadOlder()
          }}
          tabIndex={0} aria-label="Conversation" className="min-h-0 min-w-0 flex-1 overflow-auto px-(--s4) pt-(--s4) pb-[calc(var(--composer-h,0px)+36px)]">
          <div className="mx-auto w-full max-w-180">
            {needsUpdate ? (
              <Banner tone="warn">
                {machine === LOCAL ? "The gateway needs an update to show this conversation." : "This peer needs an update to show this conversation."}
              </Banner>
            ) : (
              <>
              {(hasOlder || older) && (
                <div data-slot="earlier-history" className="mb-(--s5) flex min-h-(--row-h) items-center justify-center gap-(--s3) text-(length:--fs-sm) text-(--fg2)">
                  {older?.loading ? (
                    <><Spinner aria-hidden /> Loading earlier messages…</>
                  ) : older?.error ? (
                    conversationNeedsUpdate(older.error) ? (
                      <span>{machine === LOCAL ? "Update the gateway" : "Update this peer"} to see earlier messages.</span>
                    ) : (
                      <>
                        <span>{conversationErrorMessage(machine, "load earlier messages", older.error)}</span>
                        <Button size="sm" onClick={() => void loadOlder(true)}>Retry</Button>
                      </>
                    )
                  ) : hasOlder ? (
                    <Button size="sm" variant="ghost" onClick={() => void loadOlder()}>Load earlier messages</Button>
                  ) : (
                    <span className="text-(--fg3)">Beginning of conversation</span>
                  )}
                </div>
              )}
              {subagentId && !subagent ? (
                <p className="py-(--s7) text-center text-(length:--fs-sm) text-(--fg3)">This subagent isn't in the loaded part of the conversation.</p>
              ) : (
                <ThreadView
                  items={subagent ? thread.items : withSending(thread.items, entries, send.isPending ? send.variables : undefined)}
                  machine={machine}
                  sessionId={sessionId}
                  readOnly={readOnly}
                  agent={subagent?.title || agentName}
                  working={live && (subagent ? subagentRunning(subagent) && !thread.needs_you : thread.working && !thread.needs_you)}
                  onQuote={readOnly || subagent ? undefined : quoteReply}
                  onFork={!subagent && canFork && !fork.isPending ? () => fork.mutate() : undefined}
                  onRetry={!subagent && canRetry ? retryLastPrompt : undefined}
                  subagents={subagent ? thread.subagents : { ...thread.subagents, ...helperSubagents(thread.items, helpers) }}
                  lastTurn={thread.last_turn}
                  turnStarted={thread.turn_started ?? subagent?.startedTs}
                  animateAfter={animateAfter}
                  onOpenSubagent={openSubagent}
                  onStopSubagent={readOnly ? undefined : (id) => stopSubagent.mutate(id)}
                  onSignedIn={!subagent && resumable ? () => resume.mutate(draft.trim()) : undefined}
                />
              )}
              {held && <p className="flex items-center justify-end gap-(--s2) text-(length:--fs-xs) text-(--fg3)"><Spinner aria-hidden />Sends when {harnessName(info?.harness ?? "agent")} is ready</p>}
              </>
            )}
          </div>
        </div>
      {!readOnly && subagent && (
        <div className="pointer-events-none absolute inset-x-0 bottom-0 z-20 px-(--s4) pb-(--s5)">
          <div className="pointer-events-auto mx-auto flex w-full max-w-180 items-center gap-(--s3) rounded-(--r-sheet) bg-(--window) px-(--s4) py-(--s2) text-(length:--fs-sm) shadow-(--shadow-composer)">
            <span className="min-w-0 flex-1 truncate text-(--fg2)">{`${agentName} owns this conversation. Approvals and Stop work here.`}</span>
            {subagentRunning(subagent) && subagent.cancellable && !subagent.id.startsWith("tool:") && (
              <Button type="button" size="sm" disabled={stopSubagent.isPending} onClick={() => stopSubagent.mutate(subagent.id)}>
                <Square aria-hidden className="fill-current" /> Stop
              </Button>
            )}
            <Button type="button" size="sm" variant="ghost" onClick={closeSubagent}><ArrowLeft aria-hidden /> Back</Button>
          </div>
        </div>
      )}
      {!readOnly && !subagentId && <div
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
          <div ref={composerRef} className="pointer-events-auto mx-auto flex w-full max-w-180 flex-col gap-(--s3)">
            <QueueChips queue={parent.queue ?? []} machine={machine} sessionId={sessionId} readOnly={readOnly || needsUpdate} />
            <GoalPill
              goal={goal}
              plan={plan}
              readOnly={readOnly || needsUpdate}
              onEdit={() => setGoalSheet({ objective: goal?.objective ?? "" })}
              onAction={(action) => goalActions.act.mutate(action)}
            />
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
                <ComposerHintsMenu id="composer-hints-list" activeIndex={activeHint} title={hintTitle} hints={composerHints} onPick={pickHint} query={slashQuery(beforeCaret)?.query ?? ""} agentName={agentName} />
                <div className="relative">
                {spans.some((span) => span.command) && (
                  // Marks commands behind the transparent textarea, which cannot style its own text.
                  <div ref={backdropRef} aria-hidden className="pointer-events-none absolute inset-0 overflow-hidden px-(--s3) py-(--s2) text-(length:--fs) wrap-break-word whitespace-pre-wrap text-transparent">
                    {spans.map((span, index) => span.command ? <mark key={index} data-command className="rounded-[3px] bg-(--accent-soft) text-transparent">{span.text}</mark> : span.text)}{" "}
                  </div>
                )}
                <textarea
                  ref={composerInputRef}
                  id="conversation-message"
                  aria-label="Message"
                  rows={1}
                  placeholder={needsUpdate ? "Update this peer to open the conversation…" : starting ? `Starting ${agentName}…` : nextQueued ? "Queue another message, or press ↵ to steer with the queued one" : parent.working ? `Queue a message for ${agentName} (⌘↵ steers)` : `Message ${agentName}`}
                  value={draft}
                  disabled={needsUpdate}
                  aria-controls={composerHints.length > 0 ? "composer-hints-list" : undefined}
                  aria-activedescendant={composerHints.length > 0 ? `composer-hints-list-${activeHint}` : undefined}
                  onScroll={(event) => { if (backdropRef.current) backdropRef.current.scrollTop = event.currentTarget.scrollTop }}
                  onSelect={(event) => setCaret({ draft: event.currentTarget.value, at: event.currentTarget.selectionStart })}
                  onChange={(event) => {
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
                    if (event.key === "Escape" && parent.working && !cancelTurn.isPending) {
                      event.preventDefault()
                      cancelTurn.mutate()
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
                  disabled={send.isPending || upload.isPending || needsUpdate}
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
                {!readOnly && info?.transport === "acp" && (
                  <Menu>
                    <MenuTrigger
                      render={
                        <Button type="button" size="sm" variant="ghost" title="Continue this chat with another agent" disabled={switchAgent.isPending || starting || needsUpdate} className="gap-(--s2)">
                          {agentName}
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
                {permissionsOption && (
                  <PopupSelect
                    label="Permissions"
                    value={typeof configValues[permissionsOption.id] === "string" ? configValues[permissionsOption.id] as string : permissionsOption.value}
                    onValueChange={(value) => setConfigValue(permissionsOption.id, value)}
                    // Stays usable while the agent waits on you: that is when switching to bypass helps.
                    disabled={setConfig.isPending || needsUpdate}
                    items={permissionsOption.items.map((item) => ({ ...item, danger: dangerousMode(item) }))}
                  />
                )}
                {(modelOption || effortOption || extraOptions.length > 0) && (
                  <Menu>
                    <MenuTrigger
                      render={
                        <Button
                          type="button"
                          size="sm"
                          variant="ghost"
                          aria-label={effortOption ? "Model and effort" : modelOption?.name ?? "Options"}
                          title="Choose model and effort"
                          disabled={setConfig.isPending || thread.needs_you || needsUpdate}
                          className="max-w-48 gap-(--s2)"
                        >
                          <span className="min-w-0 truncate">
                            {modelOption
                              ? modelOption.items.find((item) => item.value === (typeof configValues[modelOption.id] === "string" ? configValues[modelOption.id] : modelOption.value))?.label ?? modelOption.value
                              : effortOption ? "Effort" : "Options"}
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
                    <MenuContent align="end" className={roles.length > 0 ? "min-w-72" : undefined}>
                      {roles.length > 0 && modelOption && (
                        <>
                          <p className="px-[11px] pt-(--s2) pb-(--s1) text-(length:--fs-xs) font-semibold text-(--fg3)">Roles</p>
                          {roles.map((role) => (
                            <MenuItem
                              key={role.id}
                              icon={<span aria-hidden className="size-[15px]" />}
                              disabled={setConfig.isPending || thread.needs_you || needsUpdate}
                              onClick={() => {
                                setConfigValue(modelOption.id, role.selector)
                                if (role.level && effortOption?.items.some((item) => item.value === role.level)) setConfigValue(effortOption.id, role.level)
                              }}
                            >
                              {role.name}
                              <span className="ms-auto ps-(--s4) text-(--fg3)">{[modelOption.items.find((item) => item.value === role.selector)?.label ?? role.selector, role.level].filter(Boolean).join(" · ")}</span>
                            </MenuItem>
                          ))}
                          <div role="separator" className="mx-[11px] my-[5px] h-px bg-(--menu-sep)" />
                        </>
                      )}
                      {modelOption && (
                        <>
                          <p className="px-[11px] pt-(--s2) pb-(--s1) text-(length:--fs-xs) font-semibold text-(--fg3)">Model</p>
                          {modelOption.items.map((item) => {
                            const selected = (typeof configValues[modelOption.id] === "string" ? configValues[modelOption.id] : modelOption.value) === item.value
                            return (
                              <MenuItem
                                key={item.value}
                                role="menuitemradio"
                                aria-checked={selected}
                                icon={selected ? <Check aria-hidden /> : <span aria-hidden className="size-[15px]" />}
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
                          {modelOption && <div role="separator" className="mx-[11px] my-[5px] h-px bg-(--menu-sep)" />}
                          <p className="px-[11px] pt-(--s2) pb-(--s1) text-(length:--fs-xs) font-semibold text-(--fg3)">Effort</p>
                          {effortOption.items.map((item) => {
                            const selected = (typeof configValues[effortOption.id] === "string" ? configValues[effortOption.id] : effortOption.value) === item.value
                            return (
                              <MenuItem
                                key={item.value}
                                role="menuitemradio"
                                aria-checked={selected}
                                icon={selected ? <Check aria-hidden /> : <span aria-hidden className="size-[15px]" />}
                                disabled={setConfig.isPending || thread.needs_you || needsUpdate}
                                onClick={() => setConfigValue(effortOption.id, item.value)}
                              >
                                {item.label}
                              </MenuItem>
                            )
                          })}
                        </>
                      )}
                      {extraOptions.map((option) => {
                        const current = configValues[option.id] ?? option.value
                        return (
                          <div key={option.id}>
                            <div role="separator" className="mx-[11px] my-[5px] h-px bg-(--menu-sep)" />
                            {option.type === "boolean" ? (
                              <MenuItem
                                role="menuitemcheckbox"
                                aria-checked={current === true}
                                icon={current === true ? <Check aria-hidden /> : <span aria-hidden className="size-[15px]" />}
                                disabled={setConfig.isPending || thread.needs_you || needsUpdate}
                                onClick={() => setConfigValue(option.id, current !== true)}
                              >
                                {option.name}
                              </MenuItem>
                            ) : (
                              <>
                                <p className="px-[11px] pt-(--s2) pb-(--s1) text-(length:--fs-xs) font-semibold text-(--fg3)">{option.name}</p>
                                {option.items.map((item) => (
                                  <MenuItem
                                    key={item.value}
                                    role="menuitemradio"
                                    aria-checked={current === item.value}
                                    icon={current === item.value ? <Check aria-hidden /> : <span aria-hidden className="size-[15px]" />}
                                    disabled={setConfig.isPending || thread.needs_you || needsUpdate}
                                    onClick={() => setConfigValue(option.id, item.value)}
                                  >
                                    {item.label}
                                  </MenuItem>
                                ))}
                              </>
                            )}
                          </div>
                        )
                      })}
                      {info?.harness === "omp" && (
                        <>
                          <div role="separator" className="mx-[11px] my-[5px] h-px bg-(--menu-sep)" />
                          <MenuItem onClick={() => void navigate({ to: "/settings/$pane", params: { pane: "omp" } })}>Oh My Pi settings…</MenuItem>
                        </>
                      )}
                    </MenuContent>
                  </Menu>
                )}
                {contextPercent !== undefined && (
                  <button
                    type="button"
                    data-slot="context-ring"
                    aria-label={`Context ${contextPercent}% used${canCompact ? ". Compact" : ""}`}
                    title={canCompact ? `Context ${contextPercent}% used. Click to compact; ⌥-click to choose what to keep.` : `Context ${contextPercent}% used. ${agentName} doesn't offer compaction.`}
                    disabled={!canCompact || needsUpdate}
                    onClick={(event) => {
                      if (event.altKey) {
                        setDraft("/compact ")
                        composerInputRef.current?.focus()
                      } else compact()
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
                    disabled={cancelTurn.isPending}
                    aria-label={cancelTurn.isPending ? "Stopping…" : "Stop"}
                    title={cancelTurn.isPending ? "Stopping…" : "Stop (Esc)"}
                    onClick={() => cancelTurn.mutate()}
                  >
                    {cancelTurn.isPending ? <Spinner /> : <Square aria-hidden className="fill-current" />}
                  </Button>
                )}
                {parent.working && canSend && (
                  <Button type="button" size="sm" variant="ghost" title="Interrupt the agent and send this now (⌘↵)" onClick={() => submit(true)}>
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
      </div>}
        </div>
        <ConversationInspector
          thread={parent}
          info={info}
          machine={machine}
          machineName={machineName}
          open={inspectorOpen}
          sessionId={sessionId}
          readOnly={readOnly || needsUpdate}
          goal={goal}
          onGoalEdit={() => setGoalSheet({ objective: goal?.objective ?? "" })}
          onGoalAction={(action) => goalActions.act.mutate(action)}
          compact={canCompact ? compact : undefined}
          compactReason={canCompact ? undefined : `${agentName} doesn't offer compaction.`}
          onOpenSubagent={openSubagent}
          onSendComments={(text, sent) => send.mutate({ text, attachments: [], clearComposer: false }, { onSuccess: sent })}
          sendingComments={send.isPending}
          git={<GitInspectorSections machine={machine} sessionId={sessionId} git={git} readOnly={readOnly || needsUpdate} onAskRebase={askAgent} />}
        />
        {goalEditorLoaded && (
          <Suspense>
            <GoalSheet open={Boolean(goalSheet)} onOpenChange={(open) => !open && setGoalSheet(undefined)} machine={machine} sessionId={sessionId} goal={goal} initialObjective={goalSheet?.objective} />
          </Suspense>
        )}
      </div>
    </div>
  )
}

function withoutKey<T>(values: Record<string, T>, key: string): Record<string, T> {
  const rest = { ...values }
  delete rest[key]
  return rest
}

// The message being sent, shown at the end of the thread until the log holds a prompt newer than the send.
function withSending(items: ThreadItem[], entries: LogEntry[], sending: SendInput | undefined): ThreadItem[] {
  const after = sending?.optimisticAfter
  if (after === undefined || !sending?.text) return items
  for (let i = entries.length - 1; i >= 0 && entries[i]!.seq > after; i--) if (entries[i]!.kind === "prompt") return items
  return [...items, { kind: "user_message_chunk", data: { text: { text: sending.text } } }]
}
