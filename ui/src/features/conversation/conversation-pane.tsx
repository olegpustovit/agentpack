import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { useNavigate } from "@tanstack/react-router"
import { lazy, Suspense, useEffect, useLayoutEffect, useRef, useState } from "react"
import { ArrowLeft, ArrowUp, AtSign, Check, ChevronDown, CornerDownLeft, Ellipsis, PanelRight, Plus, Slash, Square, SquareTerminal } from "lucide-react"
import { Menu, MenuContent, MenuItem, MenuTrigger } from "@/components/kit/overlays"
import { Banner, Spinner } from "@/components/kit/feedback"
import { PopupSelect } from "@/components/kit/forms"
import { Button } from "@/components/ui/button"
import { ToolbarSlot, ToolbarTitle } from "@/features/shell/toolbar-slot"
import { useIsPhone } from "@/features/shell/use-is-phone"
import { chatShellsQuery } from "@/features/terminal/chat-shells"
import { rememberChatTerminal, takeChatTerminalCommand, terminalCommandEvent, type TerminalCommand, type TerminalRequest } from "@/features/terminal/shortcuts"
import { StatusSlot } from "@/features/status/status-bar"
import { pendingChatClaim, useStartSession } from "@/features/sessions/start"
import { sessionsQuery } from "@/features/sessions/queries"
import { agentIds } from "@/features/sessions/rules"
import { useConversationEvents, useEventsConnected } from "@/features/events/use-events"
import { useMachines } from "@/features/machines/queries"
import type { SessionInfo } from "@/lib/api-types"
import { harnessName, relativeTime } from "@/lib/format"
import { clock, goalElapsedMs, liveGoal } from "@/lib/goal"
import { useNow } from "@/lib/use-now"
import { cacheBuildID, restoreChatQuery, trackStreamingQuery } from "@/lib/persist"
import { useIntent } from "@/lib/intent"
import { hud } from "@/lib/hud"
import { api, ApiError, LOCAL } from "@/lib/api"
import { configQuery } from "@/lib/config"
import { ompSettingsQuery, quickRoles } from "@/features/settings/omp-model"
import { agentSnapshotQuery, archiveUnavailableMessage, conversationEventsQuery, conversationErrorMessage, conversationNeedsUpdate, joinHistory, mergeConversationPages, olderConversationPage, type ConversationPage } from "./queries"
import { configRole, dangerousMode, parseConfigOptions } from "./config-options"
import type { LogEntry, ThreadItem, UserMessagePayload } from "./model"
import { helperSubagents, reduceThread } from "./reducer"
import { latestUserPrompt, quoteMessage, ThreadView, userImageSrc } from "./thread-view"
import { GoalPill, parseGoalCommand, useGoalActions } from "./goal-sheet"
import { QueueChips } from "./queue-chips"
import { subagentRunning } from "./subagent-card"
import { ConversationInspector } from "./conversation-inspector"
import { GitInspectorSections, GitToolbarActions, branchLine, useSessionGit } from "./session-git"
import { advertises, agentCommands, applyHint, commandSpans, filterAtHints, filterSlashHints, moveHint, slashQuery, type ComposerHint } from "./composer-hints"
import { ComposerHintsMenu } from "./composer-hints-menu"
import { listDiffChanges } from "./diff-summary"
import { loadAttachments, revokePreview, saveAttachments, uploadConversationBlob, type PendingAttachment } from "./upload-blob"
import { imageFilesFromClipboard, imageFilesFromList, promptRequest, retryPromptInput } from "./composer-attach"

const TerminalPanel = lazy(() => import("@/features/terminal/terminal-panel"))

type UploadedAttachment = PendingAttachment & { name: string }
type SendInput = {
  text: string
  /** Interrupt the running turn and send this next. */
  steer?: boolean
  attachments: (Pick<PendingAttachment, "sha" | "mime" | "width" | "height"> & { name?: string })[]
  resourceLink?: { sha: string; name: string }
  clearComposer: boolean
}
type PromptIntent = SendInput & { id: string; startedAt?: number }

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
  const [draft, setDraft] = useState(() => loadDraft(draftKey(machine, sessionId)))
  const recalledPrompt = useRef<{ seq: number; text: string } | null>(null)
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
  // A starting chat accepts text before the worker completes its handshake.
  const starting = !readOnly && info?.conversation_state === "starting" && !info.ended_at
  const live = !readOnly && !starting && info?.state.running === true
  // A stopped chat comes back on its next message: the gateway restarts it under the same ID, reloads
  // the agent's conversation and sends the message once it runs.
  // One that never started (its agent wasn't signed in, say) starts fresh the same way.
  const resumable = !readOnly && !starting && !live && Boolean(info?.ended_at)
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
  const transcript = reduceThread(historyKey, entries)
  const activity = info?.conversation_state
  const parent = activity ? {
    ...transcript,
    working: activity === "working",
    needs_you: activity === "needs_you",
  } : transcript
  const config = useQuery(configQuery).data
  const logConfig = config?.conversation_config_log === true
  const reportTimings = config?.debug_chat_timings === true
  const { data: legacyConfig, error: configError } = useQuery({
    queryKey: ["conversation-config", machine, sessionId],
    // Only a running agent has options; a stopped chat has no worker, and its 404 is not a version gap.
    enabled: live && !logConfig,
    queryFn: () => api<unknown>(`/api/sessions/${encodeURIComponent(sessionId)}/conversation/config-options`, { machine }),
    retry: false,
  })
  const needsUpdate = conversationNeedsUpdate(conversationError, live && !logConfig ? configError : undefined)
  const archiveUnavailable = archiveUnavailableMessage(conversationError)
  const snapshot = useQuery(agentSnapshotQuery(machine, info)).data
  const configRaw = parent.agentConfig ?? legacyConfig ?? snapshot?.config
  const configOptions = parseConfigOptions(configRaw).map((option) =>
    option.type === "select" && configRole(option) === "permissions" && parent.agentMode && option.items.some((item) => item.value === parent.agentMode)
      ? { ...option, value: parent.agentMode }
      : option,
  )
  const provisionalConfig = starting && configOptions.length > 0
  const selectOptions = configOptions.filter((option) => option.type === "select")
  const permissionsOption = selectOptions.find((option) => configRole(option) === "permissions")
  const modelOption = selectOptions.find((option) => configRole(option) === "model")
  // An Oh My Pi chat can switch to a role's model (and thinking), as set on the machine running it.
  const ompSettings = useQuery({ ...ompSettingsQuery(machine), enabled: live && info?.harness === "omp", retry: false }).data
  const roles = ompSettings && modelOption ? quickRoles(ompSettings, modelOption.items.map((item) => item.value)) : []
  const effortOption = selectOptions.find((option) => configRole(option) === "effort")
  const extraOptions = configOptions.filter((option) => configRole(option) === "other")
  const tailLoaded = tail !== undefined
  useLayoutEffect(() => {
    if (!tailLoaded && !starting) return
    const opened = performance.getEntriesByName("agentpack:chat-switch", "mark").at(-1)
    if (!opened) return
    const frame = requestAnimationFrame(() => {
      performance.clearMarks("agentpack:chat-switch")
      chatTiming("chat-first-frame", opened.startTime, reportTimings)
    })
    return () => cancelAnimationFrame(frame)
  }, [historyKey, reportTimings, starting, tailLoaded])
  const streamReady = live || (starting && info?.provisionalStart !== "claim")
  const subscribe = useConversationEvents()
  const [legacyStream, setLegacyStream] = useState(false)
  useEffect(() => {
    if (!streamReady || needsUpdate || !tailLoaded) return
    const key = ["conversation", machine, sessionId]
    const onEntry = (data: string) => {
      let entry: LogEntry
      try {
        entry = JSON.parse(data) as LogEntry
      } catch {
        return
      }
      queryClient.setQueryData<ConversationPage>(key, (current) => {
        const rows = current?.entries ?? []
        if (entry.seq <= (rows.at(-1)?.seq ?? 0)) return current
        return { entries: [...rows, entry], truncated: current?.truncated ?? false }
      })
    }
    const onTruncated = () => {
      queryClient.setQueryData<ConversationPage>(key, (current) => current && !current.truncated ? { ...current, truncated: true } : current)
    }
    if (subscribe && !legacyStream) {
      let untrack: (() => void) | undefined
      const unsubscribe = subscribe({
        machine, sessionId, onEntry, onTruncated,
        onConnection: (connected) => {
          if (connected) {
            if (!untrack) untrack = trackStreamingQuery(key)
          } else {
            untrack?.()
            untrack = undefined
          }
          setStreamConnected(connected)
        },
        onFallback: () => setLegacyStream(true),
      })
      return () => {
        unsubscribe()
        untrack?.()
        untrack = undefined
      }
    }
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
      source.onmessage = (event) => onEntry(event.data)
      source.addEventListener("truncated", onTruncated)
    }
    document.addEventListener("visibilitychange", syncVisibility)
    syncVisibility()
    return () => {
      document.removeEventListener("visibilitychange", syncVisibility)
      close()
    }
  }, [legacyStream, machine, needsUpdate, queryClient, sessionId, streamReady, subscribe, tailLoaded])

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
  useLayoutEffect(() => {
    const element = threadScrollRef.current
    if (!element || tailEntries.length === 0 || element.querySelector("[data-thread-virtual]")) return
    if (stickToBottomRef.current) element.scrollTop = element.scrollHeight
  }, [tailEntries])
  useLayoutEffect(() => {
    const element = threadScrollRef.current
    const anchor = scrollAnchorRef.current
    if (!element || !anchor) return
    scrollAnchorRef.current = null
    if (element.querySelector("[data-thread-virtual]")) return
    element.scrollTop = anchor.top + element.scrollHeight - anchor.height
  }, [older?.entries])

  // Scrolling never retries a failed page; the Retry button does.
  async function loadOlder(retry = false, complete?: (loaded: LogEntry[]) => boolean) {
    const first = entries[0]
    if (!first || !hasOlder || older?.loading || (older?.error && !retry)) return
    const previous = older?.entries ?? []
    let kept = previous
    let before = first.seq
    setHistory({ key: historyKey, entries: kept, more: true, loading: true })
    let more: boolean
    let loaded: LogEntry[]
    do {
      let page: ConversationPage
      try {
        page = await olderConversationPage(machine, sessionId, before)
      } catch (error) {
        setHistory({ key: historyKey, entries: previous, more: true, loading: false, error })
        return
      }
      kept = [...page.entries, ...kept]
      loaded = joinHistory(kept, tailEntries)
      const next = page.entries[0]?.seq
      more = page.truncated && next !== undefined && next > 1 && next < before
      before = next ?? before
    } while (more && complete && !complete(loaded))
    const element = threadScrollRef.current
    if (element) scrollAnchorRef.current = { height: element.scrollHeight, top: element.scrollTop }
    setHistory({ key: historyKey, entries: kept, more, loading: false })
    return loaded
  }
  useEffect(() => {
    const save = draft && recalledPrompt.current?.text !== draft
    try {
      const key = draftKey(machine, sessionId)
      if (save) sessionStorage.setItem(key, draft)
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
    mutationFn: async ({ key, value }: { key: string; value: string | boolean }) => {
      const claimed = await pendingChatClaim(sessionId)
      return api(`/api/sessions/${encodeURIComponent(claimed?.id ?? sessionId)}/conversation/config`, {
        machine,
        method: "POST",
        body: { key, value },
      })
    },
    onSuccess: (_, { key }) => {
      if (!starting && !logConfig) {
        return queryClient.invalidateQueries({ queryKey: ["conversation-config", machine, sessionId] }).then(() =>
          setConfigValues((values) => withoutKey(values, key)),
        )
      }
    },
    onError: (error, { key, value }) => {
      setConfigValues((values) => withoutKey(values, key))
      hud.error(
        conversationErrorMessage(machine, `set ${key}`, error),
        conversationNeedsUpdate(error) ? undefined : () => setConfigValue(key, value),
      )
    },
  })
  function setConfigValue(key: string, value: string | boolean) {
    setConfigValues((current) => ({ ...current, [key]: value }))
    setConfig.mutate({ key, value })
  }
  useEffect(() => {
    if (live && configRaw !== undefined) setConfigValues((values) => Object.keys(values).length ? {} : values)
  }, [configRaw, live, parent.agentMode])

  const fork = useStartSession()
  const forkChat = () => {
    if (!info || !canFork || fork.isPending) return
    fork.mutate({ machine, directory: info.directory, harness: info.harness, mode: info.mode, view: "chat", forkFrom: sessionId })
  }
  const cancelTurn = useIntent({
    request: (_input: void, idempotencyKey) =>
      api(`/api/sessions/${encodeURIComponent(sessionId)}/conversation/cancel`, { machine, method: "POST", idempotencyKey }),
    failed: (error, _input, retry) =>
      hud.error(conversationErrorMessage(machine, "stop the conversation", error), conversationNeedsUpdate(error) ? undefined : retry),
  })
  const working = parent.working
  const stopping = cancelTurn.isPending
  const stopTurn = cancelTurn.mutate
  useEffect(() => {
    // Esc anywhere in the pane: leave a subagent, else stop the turn; fields and dialogs keep their own Esc.
    const onKey = (event: KeyboardEvent) => {
      if (event.key !== "Escape" || event.defaultPrevented) return
      if ((event.target as HTMLElement | null)?.closest("input, textarea, [role=dialog], [role=menu]")) return
      if (subagentId) void navigate({ to: ".", search: (prev: Record<string, unknown>) => ({ ...prev, sub: undefined }) })
      else if (working && !stopping && !readOnly) stopTurn()
      else return
      event.preventDefault()
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  }, [subagentId, navigate, working, stopping, readOnly, stopTurn])
  const stopSubagent = useIntent({
    request: (id: string, idempotencyKey) =>
      id.startsWith("helper:")
        ? api(`/api/sessions/${encodeURIComponent(id.slice("helper:".length))}/conversation/cancel`, { machine, method: "POST", idempotencyKey })
        : api(`/api/sessions/${encodeURIComponent(sessionId)}/conversation/cancel`, { machine, method: "POST", idempotencyKey, body: { subagent: id } }),
    failed: (error, _id, retry) =>
      hud.error(conversationErrorMessage(machine, "stop the subagent", error), conversationNeedsUpdate(error) ? undefined : retry),
  })
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
        info?.agent_capabilities?.promptCapabilities?.image === true,
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
  const submitPrompt = send.mutate
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
  const restartAfterSignIn = useIntent({
    request: (_input: void, id) => api(`/api/sessions/${encodeURIComponent(sessionId)}/restart`, {
      machine, body: { operation_id: id },
    }),
    failed: (error, _input, retry) => hud.error(conversationErrorMessage(machine, "restart the agent after sign-in", error), retry),
  })
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
      submitPrompt({
        text: retryInput.text,
        attachments: retryInput.attachments,
        resourceLink: retryInput.resourceLink,
        clearComposer: false,
      })
    }
  }
  // Images upload to the running agent, so a starting chat accepts text.
  const canSend = !readOnly && (draft.trim().length > 0 || attachments.length > 0) && !upload.isPending && !needsUpdate && !((starting || resumable) && attachments.length > 0) && !(resumable && resume.isPending)

  const nextQueued = parent.queue?.find((item) => !item.steer)

  function submit(steer = false) {
    // Enter on an empty composer sends the waiting message now.
    if (nextQueued && !draft.trim() && attachments.length === 0 && !readOnly && !needsUpdate && !steerQueued.isPending) {
      steerQueued.mutate(nextQueued.queueId)
      return
    }
    if (!canSend) return
    recalledPrompt.current = null
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
    if (resumable) {
      resume.mutate(text)
      return
    }
      // Writing instead of answering an open question interrupts it, as in the Claude CLI.
    submitPrompt({ text, attachments, clearComposer: true, steer: (steer || parent.needs_you) && parent.working })
  }
  // A merge conflict goes back to the agent, which resolves it in its own worktree.
  function askAgent(text: string) {
    if (resumable) resume.mutate(text)
    else submitPrompt({ text, attachments: [], clearComposer: false })
  }
  const git = useSessionGit(machine, sessionId, info).data
  const branch = branchLine(git)
  function compact(focus?: string) {
    if (!canCompact || readOnly) return
    submitPrompt({ text: focus ? `/compact ${focus}` : "/compact", attachments: [], clearComposer: false })
  }
  const commands = agentCommands(entries) ?? snapshot?.commands?.availableCommands ?? []
  const canCompact = advertises(commands, "compact")
  const diffPaths = listDiffChanges(thread.items).map((change) => change.path)
  const caretAt = caret?.draft === draft ? caret.at : draft.length
  const beforeCaret = draft.slice(0, caretAt)
  const slashHints = filterSlashHints(beforeCaret, commands).filter((hint) => hint.value !== "/fork" || (canFork && !fork.isPending))
  const atHints = filterAtHints(beforeCaret, diffPaths)
  const spans = draft.includes("/") ? commandSpans(draft, commands) : []
  const composerHints = hintsClosed ? [] : slashHints.length > 0 ? slashHints : atHints
  const activeHint = Math.min(hintIndex, Math.max(composerHints.length - 1, 0))
  const hintTitle = slashHints.length > 0 ? "Commands" : "Files in this thread"
  async function recallPrompt(direction: -1 | 1, input: HTMLTextAreaElement) {
    const current = recalledPrompt.current
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
      if (!loaded || composerInputRef.current !== input || input.value !== draft || recalledPrompt.current !== current) return
      next = findPrompt(reduceThread(historyKey, loaded).items)
    }
    if (!next && direction < 0) return
    recalledPrompt.current = next ?? null
    setHintsClosed(Boolean(next))
    setCaret(undefined)
    setDraft(next?.text ?? "")
  }
  function pickHint(hint: ComposerHint) {
    setHintIndex(0)
    if (hint.value === "/fork") {
      forkChat()
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
  const phone = useIsPhone()
  const eventsConnected = useEventsConnected()
  const [terminalOpen, setTerminalOpen] = useState(false)
  const [terminalLoaded, setTerminalLoaded] = useState(false)
  const [newShellRequest, setNewShellRequest] = useState(0)
  const chatAllowsTerminal = info?.transport === "acp" && !info.archived_at && !subagentId
  const shells = useQuery({
    ...chatShellsQuery(machine, sessionId),
    enabled: chatAllowsTerminal && !readOnly,
    refetchInterval: eventsConnected ? false : 15_000,
  })
  const terminalAvailable = chatAllowsTerminal && !(shells.error instanceof ApiError && [403, 409].includes(shells.error.status))
  useEffect(() => {
    if (!terminalAvailable) return
    const run = (command: TerminalCommand | "open") => {
      if (command === "new" && readOnly) return
      if (command === "toggle" && terminalOpen) {
        setTerminalOpen(false)
        requestAnimationFrame(() => composerInputRef.current?.focus())
        return
      }
      rememberChatTerminal({ machine, sessionId })
      setTerminalLoaded(true)
      setTerminalOpen(true)
      if (command === "new") setNewShellRequest((count) => count + 1)
    }
    const queued = takeChatTerminalCommand(machine, sessionId)
    if (queued) run(queued)
    const onCommand = (event: Event) => {
      event.preventDefault()
      const { command, repeat } = (event as CustomEvent<TerminalRequest>).detail
      if (!repeat) run(command)
    }
    window.addEventListener(terminalCommandEvent, onCommand)
    return () => window.removeEventListener(terminalCommandEvent, onCommand)
  }, [machine, sessionId, terminalAvailable, terminalOpen, readOnly])

  function hideTerminal() {
    setTerminalOpen(false)
    requestAnimationFrame(() => composerInputRef.current?.focus())
  }

  function toggleTerminal() {
    if (terminalOpen) hideTerminal()
    else {
      rememberChatTerminal({ machine, sessionId })
      setTerminalLoaded(true)
      setTerminalOpen(true)
    }
  }

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
        {terminalAvailable && (phone ? (
          <Menu>
            <MenuTrigger render={<Button type="button" variant="ghost" size="icon" aria-label="Chat actions"><Ellipsis aria-hidden /></Button>} />
            <MenuContent align="end"><MenuItem icon={<SquareTerminal />} onClick={toggleTerminal}>Terminal</MenuItem></MenuContent>
          </Menu>
        ) : (
          <Button type="button" variant="ghost" size="icon" aria-label="Terminal" title="Terminal (⌘J)" aria-pressed={terminalOpen} onClick={toggleTerminal} className="aria-pressed:text-(--accent)">
            <SquareTerminal aria-hidden />
          </Button>
        ))}
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
      {readOnly && <Banner>{info?.archived_at ? "Cold storage · read-only" : `Offline, read-only${info?.last_sync ? ` · Last sync ${relativeTime(new Date(info.last_sync), Date.now())}` : ""}`}</Banner>}
      {archiveUnavailable && <Banner>{archiveUnavailable}</Banner>}
      <div className="flex min-h-0 min-w-0 flex-1">
        <div className="relative flex min-h-0 min-w-0 flex-1 flex-col">
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
                  scrollRef={threadScrollRef}
                  items={subagent ? thread.items : withSending(thread.items, entries, pendingSends)}
                  machine={machine}
                  sessionId={sessionId}
                  readOnly={readOnly}
                  agent={subagent?.title || agentName}
                  working={live && (subagent ? subagentRunning(subagent) && !thread.needs_you : thread.working && !thread.needs_you)}
                  onQuote={readOnly || subagent ? undefined : quoteReply}
                  onFork={!subagent && canFork && !fork.isPending ? forkChat : undefined}
                  onRetry={!subagent && canRetry ? retryLastPrompt : undefined}
                  subagents={subagent ? thread.subagents : { ...thread.subagents, ...helperSubagents(thread.items, helpers) }}
                  lastTurn={thread.last_turn}
                  turnStarted={thread.turn_started ?? subagent?.startedTs}
                  animateAfter={animateAfter}
                  onOpenSubagent={openSubagent}
                  onStopSubagent={readOnly ? undefined : (id) => stopSubagent.mutate(id)}
                  authHarness={info?.harness}
                  onSignedIn={!subagent && !readOnly && (resumable || live)
                    ? () => resumable ? resume.mutate(draft.trim()) : restartAfterSignIn.mutate()
                    : undefined}
                />
              )}
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
            <QueueChips
              queue={parent.queue ?? []}
              machine={machine}
              sessionId={sessionId}
              readOnly={readOnly || needsUpdate}
              steering={steerQueued.isPending ? steerQueued.input : undefined}
              onSteer={steerQueued.mutate}
            />
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
                    recalledPrompt.current = null
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
                        ((event.key === "ArrowUp" || event.key === "ArrowDown") && recalledPrompt.current?.text === draft))) {
                      event.preventDefault()
                      void recallPrompt(event.key === "ArrowUp" ? -1 : 1, event.currentTarget)
                      return
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
                {provisionalConfig && <span className="text-(length:--fs-xs) text-(--fg3)">Provisional</span>}
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
                          disabled={setConfig.isPending || (!starting && thread.needs_you) || needsUpdate}
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
                              disabled={setConfig.isPending || (!starting && thread.needs_you) || needsUpdate}
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
                                disabled={setConfig.isPending || (!starting && thread.needs_you) || needsUpdate}
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
                                disabled={setConfig.isPending || (!starting && thread.needs_you) || needsUpdate}
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
                                disabled={setConfig.isPending || (!starting && thread.needs_you) || needsUpdate}
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
                                    disabled={setConfig.isPending || (!starting && thread.needs_you) || needsUpdate}
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
          {terminalAvailable && (terminalLoaded || !!shells.data?.length) && (
            <Suspense>
              <TerminalPanel
                chat={info} machine={machine} machineName={machineName} phone={phone} offline={readOnly}
                open={terminalOpen} onToggle={toggleTerminal} onHide={hideTerminal} newShellRequest={newShellRequest} shells={shells}
              />
            </Suspense>
          )}
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
          onSendComments={(text, sent) => submitPrompt({ text, attachments: [], clearComposer: false }, undefined, { onSuccess: sent })}
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

function chatTiming(name: "chat-first-frame" | "prompt-confirmed", start: number, report: boolean) {
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

function withSending(items: ThreadItem[], entries: LogEntry[], sending: PromptIntent[]): ThreadItem[] {
  if (!sending.length) return items
  const received = confirmedPrompts(entries)
  return [...items, ...sending.filter((item) => item.text && !received.has(item.id)).map((item): ThreadItem =>
    ({ kind: "user_message_chunk", data: { text: { text: item.text } } }),
  )]
}
