import { useQuery, useQueryClient } from "@tanstack/react-query"
import { useNavigate } from "@tanstack/react-router"
import { lazy, Suspense, useEffect, useLayoutEffect, useRef, useState } from "react"
import { ArrowLeft, Ellipsis, PanelRight, Square, SquareTerminal } from "lucide-react"
import { Menu, MenuContent, MenuItem, MenuTrigger } from "@/components/kit/overlays"
import { Banner, Spinner } from "@/components/kit/feedback"
import { Button } from "@/components/ui/button"
import { ToolbarSlot, ToolbarTitle } from "@/features/shell/toolbar-slot"
import { useIsPhone } from "@/features/shell/use-is-phone"
import { chatShellsQuery } from "@/features/terminal/chat-shells"
import { rememberChatTerminal, takeChatTerminalCommand, terminalCommandEvent, type TerminalCommand, type TerminalRequest } from "@/features/terminal/shortcuts"
import { StatusSlot } from "@/features/status/status-bar"
import { useStartSession } from "@/features/sessions/start"
import { sessionLabel } from "@/features/sessions/group"
import { sessionsQuery } from "@/features/sessions/queries"
import { openWaits, waitingDetail } from "@/features/sessions/session-status"
import { useEventsConnected } from "@/features/events/use-events"
import { identityQuery, useMachines } from "@/features/machines/queries"
import type { SessionInfo } from "@/lib/api-types"
import { harnessName, relativeTime } from "@/lib/format"
import { clock, goalElapsedMs, liveGoal } from "@/lib/goal"
import { useNow } from "@/lib/use-now"
import { cacheBuildID, restoreChatQuery } from "@/lib/persist"
import { useIntent } from "@/lib/intent"
import { hud } from "@/lib/hud"
import { api, ApiError, LOCAL } from "@/lib/api"
import { configQuery } from "@/lib/config"
import { archiveUnavailableMessage, conversationEventsQuery, conversationErrorMessage, conversationNeedsUpdate, mergeConversationPages, type ConversationPage } from "./queries"
import type { PromptFrom, ThreadItem } from "./model"
import { helperSubagents, isOpenRequest } from "./reducer"
import { latestUserPrompt, quoteMessage, ThreadView } from "./thread-view"
import { ElicitationCard, PermissionCard } from "./request-cards"
import { GoalPill, parseGoalCommand, useGoalActions } from "./goal-sheet"
import { subagentRunning } from "./subagent-card"
import { ConversationInspector } from "./conversation-inspector"
import { useConversationStream, useHistoryPaging, type DerivedState } from "./conversation-log"
import { useChatConfig } from "./chat-config"
import { GitInspectorSections, GitToolbarActions, branchLine, useSessionGit } from "./session-git"
import { advertises, agentCommands } from "./composer-hints"
import { retryPromptInput } from "./composer-attach"
import { chatTiming, Composer, useComposer, withSending } from "./composer"

const TerminalPanel = lazy(() => import("@/features/terminal/terminal-panel"))

const GoalSheet = lazy(() => import("./goal-editor").then((module) => ({ default: module.GoalSheet })))
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
  const stickToBottomRef = useRef(true)
  const [inspectorOpen, setInspectorOpen] = useState(() => window.innerWidth > 1240)
  const threadScrollRef = useRef<HTMLDivElement>(null)
  const threadContentRef = useRef<HTMLDivElement>(null)
  const lastScrollRef = useRef({ top: 0, height: 0 })
  // Follow the end while the reader is there: content that grows (streaming, images, measured rows) or
  // a pane revealed again keeps the latest line on screen. Runs before paint, so nothing flickers.
  useLayoutEffect(() => {
    const thread = threadScrollRef.current
    const content = threadContentRef.current
    if (!thread || !content) return
    const observer = new ResizeObserver(() => {
      if (thread.clientHeight === 0) return // hidden by Activity
      const last = lastScrollRef.current
      // A reader who reached the end in the same frame the output grew has no scroll event yet: follow too.
      if (stickToBottomRef.current || Math.abs(last.height - thread.scrollTop - thread.clientHeight) < 8) {
        stickToBottomRef.current = true
        thread.scrollTop = thread.scrollHeight
      }
      lastScrollRef.current = { top: thread.scrollTop, height: thread.scrollHeight }
    })
    observer.observe(content)
    observer.observe(thread)
    return () => observer.disconnect()
  }, [])
  const readOnly = unavailable || info?.reachable === false || Boolean(info?.archived_at)
  // A starting chat accepts text before the worker completes its handshake.
  const starting = !readOnly && info?.conversation_state === "starting" && !info.ended_at
  const live = !readOnly && !starting && info?.state.running === true
  // A stopped chat comes back on its next message: the gateway restarts it under the same ID, reloads
  // the agent's conversation and sends the message once it runs.
  // One that never started (its agent wasn't signed in, say) starts fresh the same way.
  const resumable = !readOnly && !starting && !live && Boolean(info?.ended_at)
  const [streamConnected, setStreamConnected] = useState(false)
  const [derived, setDerived] = useState<DerivedState>()
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
  const history = useHistoryPaging(machine, sessionId, tail, threadScrollRef)
  const { historyKey, older, entries, hasOlder, transcript, loadOlder } = history
  // The owner's fold is the live state; the row, then this browser's reducer, are the fallbacks for older owners.
  const activity = derived?.conversation_state ?? info?.conversation_state
  // Idle with background work open: the agent will wake by itself (internal/acp WaitItem).
  const waitNow = useNow(true, 60_000)
  const waits = activity === "idle" && info && !readOnly && !subagentId ? openWaits(derived ?? info, waitNow) : []
  const knownChats = useQuery({ ...sessionsQuery(false), enabled: waits.some((item) => item.kind === "reply") }).data
  const chatName = (id: string) => {
    const chat = knownChats?.find((row) => row.id === id) ?? helpers.find((row) => row.id === id)
    return chat && sessionLabel({ ...chat, machine, machineName: "", offline: false })
  }
  const parent = activity ? {
    ...transcript,
    working: activity === "working",
    needs_you: activity === "needs_you",
  } : transcript
  const config = useQuery(configQuery).data
  const logConfig = config?.conversation_config_log === true
  const reportTimings = config?.debug_chat_timings === true
  const chatConfig = useChatConfig({ machine, sessionId, info, parent, live, starting, logConfig })
  const { snapshot } = chatConfig
  const needsUpdate = conversationNeedsUpdate(conversationError, live && !logConfig ? chatConfig.configError : undefined)
  const archiveUnavailable = archiveUnavailableMessage(conversationError)
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
  useConversationStream(machine, sessionId, (live || (starting && info?.provisionalStart !== "claim")) && !needsUpdate && tailLoaded, setStreamConnected, setDerived)

  const subagent = subagentId ? parent.subagents?.[subagentId] : undefined
  const thread = subagent?.thread ?? parent
  // Entries already loaded on first paint are history and never animate.
  const [animateAfter, setAnimateAfter] = useState<number>()
  if (animateAfter === undefined && tail) setAnimateAfter(tail.entries.at(-1)?.seq ?? 0)
  const closeSubagent = () => void navigate({ to: ".", search: (prev: Record<string, unknown>) => ({ ...prev, sub: undefined }) })
  const identity = useQuery(identityQuery).data
  // A sender on the machine serving this page is "local" in routes.
  const openChat = (from: PromptFrom) =>
    void navigate({ to: "/sessions/$machine/$sessionId", params: { machine: !from.node ? machine : from.node === identity?.node.id ? LOCAL : from.node, sessionId: from.session } })
  const openSubagent = (id: string) =>
    id.startsWith("helper:")
      ? void navigate({ to: "/sessions/$machine/$sessionId", params: { machine, sessionId: id.slice("helper:".length) } })
      : void navigate({ to: ".", search: (prev: Record<string, unknown>) => ({ ...prev, sub: id }) })
  const plan = currentPlan(parent.items)
  // The live record is the log; the session row is the fallback for chats whose log is not loaded.
  const goal = parent.goal ?? (liveGoal(info?.goal) ? info?.goal : undefined)
  const goalNow = useNow(goal?.state === "active")
  const [goalSheet, setGoalSheet] = useState<{ objective: string }>()
  // The editor loads on first open and then stays mounted, so closing it still animates.
  const [goalEditorLoaded, setGoalEditorLoaded] = useState(false)
  if (goalSheet && !goalEditorLoaded) setGoalEditorLoaded(true)
  const goalActions = useGoalActions(machine, sessionId)

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
  const composer = useComposer({ machine, sessionId, info, entries, reportTimings })
  const { send, resume } = composer
  const submitPrompt = send.mutate
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
  function quoteReply(text: string) {
    composer.setDraft((current) => `${current}${current ? "\n\n" : ""}${quoteMessage(text)}\n`)
    composer.inputRef.current?.focus()
  }
  function goalCommand(command: NonNullable<ReturnType<typeof parseGoalCommand>>) {
    if (command.kind === "sheet") setGoalSheet({ objective: "" })
    else if (command.kind === "action") goalActions.act.mutate(command.action)
    else goalActions.set.mutate(
      { goal: { objective: command.objective }, edit: false },
      { onError: (error) => hud.error(conversationErrorMessage(machine, "set the goal", error)) },
    )
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
        requestAnimationFrame(() => composer.inputRef.current?.focus())
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
    requestAnimationFrame(() => composer.inputRef.current?.focus())
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
            if (el.clientHeight === 0) return // hidden by Activity: the browser zeroed the offset, not the reader
            const last = lastScrollRef.current
            // At the end it follows; moving up without the content changing is the reader leaving it.
            if (el.scrollHeight - el.scrollTop - el.clientHeight < 8) stickToBottomRef.current = true
            // Landed on the end as it was before output grew in the same frame: follow and catch up.
            else if (el.scrollTop > last.top && Math.abs(last.height - el.scrollTop - el.clientHeight) < 8) {
              stickToBottomRef.current = true
              el.scrollTop = el.scrollHeight
            } else if (el.scrollTop < last.top && el.scrollHeight === last.height) stickToBottomRef.current = false
            lastScrollRef.current = { top: el.scrollTop, height: el.scrollHeight }
            // Fetch the previous page a screen before the reader reaches the top.
            if (el.scrollTop < el.clientHeight) void loadOlder()
          }}
          tabIndex={0} aria-label="Conversation" className="min-h-0 min-w-0 flex-1 overflow-auto px-(--s4) pt-(--s4) pb-[calc(var(--composer-h,0px)+36px)]">
          <div ref={threadContentRef} className="mx-auto w-full max-w-180">
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
                  pinRequests={!readOnly && !subagentId}
                  items={subagent ? thread.items : withSending(thread.items, entries, composer.pendingSends)}
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
                  onOpenChat={openChat}
                  onStopSubagent={readOnly ? undefined : (id) => stopSubagent.mutate(id)}
                  authHarness={info?.harness}
                  // The gateway restarts a running chat once its agent is signed in; a stopped one resumes here.
                  onSignedIn={!subagent && !readOnly && (resumable || live)
                    ? () => { if (resumable) resume.mutate(composer.draft.trim()) }
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
      {!readOnly && !subagentId && (
        <Composer
          composer={composer} machine={machine} sessionId={sessionId} info={info} parent={parent} history={history}
          chatConfig={chatConfig} commands={commands} contextPercent={contextPercent} compact={canCompact ? compact : undefined}
          starting={starting} resumable={resumable} needsUpdate={needsUpdate} stopping={cancelTurn.isPending} onStop={() => cancelTurn.mutate()}
          onFork={canFork && !fork.isPending ? forkChat : undefined} onSwitchTerminal={onSwitchTerminal} onGoalCommand={goalCommand}
          threadScrollRef={threadScrollRef} stickToBottomRef={stickToBottomRef}
        >
          <GoalPill
            goal={goal}
            plan={plan}
            readOnly={readOnly || needsUpdate}
            onEdit={() => setGoalSheet({ objective: goal?.objective ?? "" })}
            onAction={(action) => goalActions.act.mutate(action)}
          />
          {waits.length > 0 && (
            <div data-slot="waiting-strip" role="status" className="flex min-w-0 items-center gap-(--s3) rounded-(--r-ctl) bg-(--card) bg-[linear-gradient(var(--accent-soft),var(--accent-soft))] px-(--s3) py-(--s2) text-(length:--fs-sm) text-(--fg2)">
              <span aria-hidden data-slot="agent-dot" data-state="waiting" className="agent-dot static! shrink-0" />
              <span className="min-w-0 truncate">
                <strong className="font-semibold text-(--fg)">Waiting</strong> for {waitingDetail(waits, chatName, waitNow)} · {agentName} wakes by itself
              </span>
            </div>
          )}
          {parent.items.some(isOpenRequest) && (
            <div data-slot="pinned-requests" className="flex max-h-[50vh] flex-col gap-(--s3) overflow-auto">
              {parent.items.filter(isOpenRequest).map((item) => item.kind === "permission"
                ? <PermissionCard key={item.seq} item={item} machine={machine} sessionId={sessionId} agent={agentName} />
                : <ElicitationCard key={item.seq} item={item} machine={machine} sessionId={sessionId} />)}
            </div>
          )}
        </Composer>
      )}
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
