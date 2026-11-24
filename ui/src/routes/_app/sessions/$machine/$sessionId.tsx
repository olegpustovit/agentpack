import { useQuery } from "@tanstack/react-query"
import { createFileRoute } from "@tanstack/react-router"
import { Activity, useEffect, useState } from "react"
import { search } from "@/lib/search"
import { conversationEventsQuery } from "@/features/conversation/queries"
import { useEventsConnected } from "@/features/events/use-events"
import { useMachines } from "@/features/machines/queries"
import { sessionDetail, sessionLabel } from "@/features/sessions/group"
import { sessionsQuery } from "@/features/sessions/queries"
import { useStartSession } from "@/features/sessions/start"
import { viewSwitchNeedsResume } from "@/features/sessions/view-switch"
import { rememberSession } from "@/features/shell/views"
import { unavailableMessage, UnavailableTerminal } from "@/features/terminal/unavailable"
import { api, LOCAL } from "@/lib/api"
import type { SessionInfo } from "@/lib/api-types"
import { Spinner } from "@/components/kit/feedback"
import { Button } from "@/components/ui/button"

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
  const { data } = useQuery(sessionsQuery(live))
  const listedInfo = data?.find((session) => session.id === sessionId)
  const detail = useQuery({
    queryKey: ["m", LOCAL, "session-row", machine, sessionId],
    queryFn: async () => {
      const row = await api<SessionInfo>(`/api/sessions/${encodeURIComponent(sessionId)}`, { machine })
      return row?.id === sessionId && row.state ? row : null
    },
    enabled: data !== undefined && !listedInfo,
    staleTime: 15_000,
    retry: false,
  })
  const info = listedInfo ?? detail.data ?? undefined
  const loading = !info && (!data || detail.isPending)
  const stale = info?.reachable === false || host?.online === false
  const message = unavailableMessage(info, stale)
  const { q, group, sub } = Route.useSearch()
  const ended = !!data && !loading && !info
  useEffect(() => {
    rememberSession(ended ? undefined : { machine, sessionId, search: { q, group } })
  }, [ended, machine, sessionId, q, group])
  const listed = info && host ? { ...info, machine, machineName: host.name, offline: stale } : undefined
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
  const { pane: TerminalPane, failed: terminalFailed } = usePane(loadPane.terminal, panes.terminal, !acp && !loading)
  if (message) return <UnavailableTerminal message={message} />
  // The pane depends on the transport, so wait for the list rather than guess Terminal
  // (which would load xterm and attach a socket to a chat session).
  if (loading && !stale) return <PaneLoading label="Loading session…" />
  const title = listed && sessionLabel(listed)
  const subtitle = listed && sessionDetail(listed).split(" · ").slice(listed.name ? 0 : 1).join(" · ")
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
            listed={!data || !!info}
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
