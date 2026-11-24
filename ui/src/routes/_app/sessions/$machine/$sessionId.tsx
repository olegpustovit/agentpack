import { useQuery } from "@tanstack/react-query"
import { createFileRoute, Link } from "@tanstack/react-router"
import { Activity, useEffect, useState } from "react"
import { search } from "@/lib/search"
import { conversationEventsQuery } from "@/features/conversation/queries"
import { useEventsConnected } from "@/features/events/use-events"
import { identityQuery, useMachines } from "@/features/machines/queries"
import { lastActivity, rowKey, sessionDetail, sessionLabel } from "@/features/sessions/group"
import { sessionRowQuery, sessionsQuery } from "@/features/sessions/queries"
import { markSeen } from "@/features/sessions/session-status"
import { useStartSession } from "@/features/sessions/start"
import { viewSwitchNeedsResume } from "@/features/sessions/view-switch"
import { rememberSession } from "@/features/shell/views"
import { ToolbarTitle } from "@/features/shell/toolbar-slot"
import { unavailableMessage } from "@/features/terminal/unavailable"
import { ApiError, LOCAL, routeMissing } from "@/lib/api"
import type { SessionInfo } from "@/lib/api-types"
import { Empty, Spinner } from "@/components/kit/feedback"
import { Button, buttonVariants } from "@/components/ui/button"
import { relativeTime } from "@/lib/format"

// Loaded on demand so xterm and the conversation UI stay out of the initial bundle. Not
// React.lazy: React holds a Suspense reveal for ~300 ms, which every session open paid.
type ConversationPaneType = typeof import("@/features/conversation/conversation-pane").default
type TerminalPaneType = typeof import("@/features/terminal/terminal-pane").default
const panes: { conversation?: ConversationPaneType; terminal?: TerminalPaneType } = {}
const loadPane = {
  conversation: () => import("@/features/conversation/conversation-pane").then((module) => (panes.conversation = module.default)),
  terminal: () => import("@/features/terminal/terminal-pane").then((module) => (panes.terminal = module.default)),
}

// Returns true when the chunk failed to load (a deploy replaced it, or the network dropped).
function usePane<T>(load: () => Promise<T>, cached: T | undefined, enabled: boolean) {
  const [pane, setPane] = useState<T | undefined>(() => cached)
  const [failed, setFailed] = useState(false)
  useEffect(() => {
    if (!enabled || pane) return
    let active = true
    // If a deploy replaced the chunk, let the user reload rather than wait indefinitely.
    const stalled = setTimeout(() => setFailed(true), 15_000)
    const finish = () => clearTimeout(stalled)
    load().then((loaded) => {
      finish()
      if (!active) return
      setPane(() => loaded)
      setFailed(false)
    }, () => {
      finish()
      if (active) setFailed(true)
    })
    return () => {
      active = false
      finish()
    }
  }, [enabled, load, pane])
  return { pane, failed }
}

function PaneLoading({ label, failed, immediate }: { label: string; failed?: boolean; immediate?: boolean }) {
  return (
    <div className="grid h-full place-items-center p-(--s6)">
      {failed ? (
        <div className="flex flex-col items-center gap-(--s4) text-center text-(length:--fs-sm) text-(--fg2)">
          <p>Couldn't load this view. The gateway was probably updated.</p>
          <Button type="button" size="sm" onClick={() => location.reload()}>Reload</Button>
        </div>
      ) : (
        // Fades in late so a fast load never flashes it.
        <p className={`flex items-center gap-(--s3) text-(length:--fs-sm) text-(--fg3) ${immediate ? "" : "opacity-0 [animation:pane-loading_.2s_.3s_forwards]"}`}>
          <Spinner /> {label}
        </p>
      )}
    </div>
  )
}

function SessionRoute() {
  const { machine, sessionId } = Route.useParams()
  const start = useStartSession()
  const live = useEventsConnected()
  const machines = useMachines()
  const host = machines.find((item) => item.id === machine)
  const identity = useQuery(identityQuery).data
  const sessions = useQuery(sessionsQuery(live))
  const { data } = sessions
  const listedInfo = data?.find((session) =>
    session.id === sessionId && (session.node_id && session.node_id !== identity?.node.id ? session.node_id : LOCAL) === machine)
  const detail = useQuery({
    ...sessionRowQuery(machine, sessionId),
    enabled: !sessions.isPending && !listedInfo,
  })
  const info = listedInfo ?? detail.data ?? undefined
  const loading = !info && (sessions.isPending || detail.isPending)
  const stale = info?.reachable === false || host?.online === false
  const message = unavailableMessage(info, stale)
  const { q, group, tab, sub } = Route.useSearch()
  const listed = info && { ...info, machine, machineName: host?.name ?? machine, offline: stale }
  const activity = listed && lastActivity(listed)
  const markable = listed?.transport === "acp" && listed.provisionalStart !== "claim"
  const seenKey = listed && rowKey(listed)
  const known = Boolean(info)
  useEffect(() => {
    if (known) rememberSession({ machine, sessionId, search: { q, group } })
  }, [known, machine, sessionId, q, group])
  useEffect(() => {
    if (markable && seenKey) markSeen(seenKey, activity)
  }, [markable, seenKey, activity])
  const [warmChats, setWarmChats] = useState<Array<{ machine: string; sessionId: string }>>([])
  const acp = info?.transport === "acp"
  useEffect(() => {
    if (!acp || message) return
    setWarmChats((current) => {
      if (current[0]?.machine === machine && current[0]?.sessionId === sessionId) return current
      return [{ machine, sessionId }, ...current.filter((chat) => chat.machine !== machine || chat.sessionId !== sessionId)].slice(0, 3)
    })
  }, [acp, machine, message, sessionId])
  const { pane: ConversationPane, failed: conversationFailed } = usePane(loadPane.conversation, panes.conversation, acp)
  const startingTerminal = !acp && info?.provisionalStart === "claim"
  const { pane: TerminalPane, failed: terminalFailed } = usePane(loadPane.terminal, panes.terminal, Boolean(info && !acp && !message && !startingTerminal))
  const title = listed && sessionLabel(listed)
  const lastSeen = info?.last_sync || activity
  const hostName = host?.name ?? (machine === LOCAL ? "this machine" : machine)
  const seenOn = lastSeen && Number.isFinite(Date.parse(lastSeen)) ? ` · Last seen ${relativeTime(new Date(lastSeen))}` : ""
  const subtitle = listed && (stale ? `${hostName}${seenOn}` : sessionDetail(listed).split(" · ").slice(listed.name ? 0 : 1).join(" · "))
  if (loading) return <PaneLoading label="Looking up session…" />
  if (!info || message) {
    const needsUpdate = routeMissing(detail.error)
    const unavailable = detail.error && !(detail.error instanceof ApiError && detail.error.status === 404)
    const heading = title || (stale ? `${hostName} is offline` : needsUpdate ? "Session lookup needs an update" : unavailable ? "Couldn't look up this session" : "Session not found")
    return (
      <>
        {title && <ToolbarTitle title={title} subtitle={subtitle || undefined} />}
        <div className="flex min-h-0 flex-col items-center gap-(--s4) overflow-y-auto px-(--s4) py-(--s6)">
          <Empty title={heading}>
            {message ? `${hostName}${seenOn} · ${message}`
              : stale ? "This gateway has no saved row for the session."
              : needsUpdate ? "Update Agentpack to open sessions outside the list."
              : unavailable ? `Couldn't load metadata for the session on ${hostName}.`
              : `There is no saved session with this ID on ${hostName}.`}
          </Empty>
          <div className="flex flex-wrap gap-(--s3)">
            <Link to="/sessions" search={{ q, group, tab }} className={buttonVariants({ size: "sm" })}>Return to sessions</Link>
            {(stale || unavailable) && <Button type="button" size="sm" onClick={() => {
              void sessions.refetch()
              if (!listedInfo) void detail.refetch()
            }}>Retry</Button>}
          </div>
        </div>
      </>
    )
  }
  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="min-h-0 flex-1">
        {ConversationPane && [
          ...(acp ? [{ machine, sessionId }] : []),
          ...warmChats.filter((chat) => chat.machine !== machine || chat.sessionId !== sessionId),
        ].slice(0, 3).map((chat) => {
          const visible = chat.machine === machine && chat.sessionId === sessionId
          const chatInfo = visible ? info : data?.find((session) => session.id === chat.sessionId)
          if (!chatInfo || chatInfo.transport !== "acp") return null
          return (
            <Activity key={`${chat.machine}/${chat.sessionId}`} mode={visible ? "visible" : "hidden"}>
              <ConversationPane
                machine={chat.machine}
                sessionId={chat.sessionId}
                info={chatInfo}
                subagent={visible ? sub : undefined}
                helpers={data?.filter((session) => session.parent_id === chat.sessionId)}
                readOnly={chatInfo.reachable === false || machines.find((item) => item.id === chat.machine)?.online === false}
                title={visible ? title || undefined : undefined}
                subtitle={visible ? subtitle || undefined : undefined}
                onSwitchTerminal={visible ? () => {
                  if (stale || start.isPending || chatInfo.archived_at || viewSwitchNeedsResume(chatInfo)) return
                  start.mutate({
                    machine, directory: chatInfo.directory, harness: chatInfo.harness, mode: chatInfo.mode,
                    view: "terminal", agentSessionId: chatInfo.agent_session_id, switchFrom: chatInfo.id,
                  })
                } : undefined}
              />
            </Activity>
          )
        })}
        {startingTerminal ? <PaneLoading label="Opening terminal…" immediate /> : acp && ConversationPane ? null : !acp && TerminalPane ? (
          <TerminalPane
            key={`${machine}/${sessionId}`}
            machine={machine}
            sessionId={sessionId}
            title={title || undefined}
            subtitle={subtitle || undefined}
            listed
          />
        ) : (
          <PaneLoading label={acp ? "Loading conversation…" : "Loading terminal…"} failed={acp ? conversationFailed : terminalFailed} />
        )}
      </div>
    </div>
  )
}

export const Route = createFileRoute("/_app/sessions/$machine/$sessionId")({
  validateSearch: search({
    q: "text",
    group: "text",
    new: "flag",
    // The subagent open in the main view; Back returns to the parent.
    sub: "text",
  }),
  // Runs on hover too (intent preload): the pane's code and the chat log start loading before the click.
  // Nothing is awaited, so navigation never waits on it.
  loader: ({ context, params }) => {
    const info = context.queryClient.getQueryData<SessionInfo[]>(sessionsQuery(false).queryKey)?.find((item) => item.id === params.sessionId)
    if (!info) return
    if (info.transport !== "acp") {
      void loadPane.terminal().catch(() => undefined)
      return
    }
    void loadPane.conversation().catch(() => undefined)
    if (info.provisionalStart !== "claim") {
      void context.queryClient.prefetchQuery(conversationEventsQuery(params.machine, params.sessionId)).catch(() => undefined)
    }
  },
  component: SessionRoute,
})
