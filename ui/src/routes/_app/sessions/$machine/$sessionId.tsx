import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { createFileRoute, useNavigate } from "@tanstack/react-router"
import { useEffect, useState } from "react"
import { search } from "@/lib/search"
import { conversationEventsQuery } from "@/features/conversation/queries"
import { useEventsConnected } from "@/features/events/use-events"
import { useMachines } from "@/features/machines/queries"
import { sessionDetail, sessionLabel } from "@/features/sessions/group"
import { sessionsQuery } from "@/features/sessions/queries"
import { startSessionBody, type SessionView } from "@/features/sessions/rules"
import { viewSwitchNeedsResume } from "@/features/sessions/view-switch"
import { rememberSession } from "@/features/shell/views"
import { unavailableMessage, UnavailableTerminal } from "@/features/terminal/unavailable"
import { api } from "@/lib/api"
import type { SessionInfo } from "@/lib/api-types"
import { hud } from "@/lib/hud"
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
function usePane(kind: keyof typeof loadPane | undefined): boolean {
  const [, loaded] = useState(0)
  const [failed, setFailed] = useState(false)
  useEffect(() => {
    if (!kind || panes[kind]) return
    // A module fetch can stall without failing (a gateway restarting behind the CDN); offer Reload rather than spin forever.
    const stalled = setTimeout(() => setFailed(true), 15_000)
    loadPane[kind]().then(() => { clearTimeout(stalled); setFailed(false); loaded((count) => count + 1) }, () => setFailed(true))
    return () => clearTimeout(stalled)
  }, [kind])
  return failed
}

function PaneLoading({ label, failed }: { label: string; failed?: boolean }) {
  return (
    <div className="grid h-full place-items-center p-(--s6)">
      {failed ? (
        <div className="flex flex-col items-center gap-(--s4) text-center text-(length:--fs-sm) text-(--fg2)">
          <p>Couldn't load this view. The gateway was probably updated.</p>
          <Button type="button" size="sm" onClick={() => location.reload()}>Reload</Button>
        </div>
      ) : (
        // Fades in late so a fast load never flashes it.
        <p className="flex items-center gap-(--s3) text-(length:--fs-sm) text-(--fg3) opacity-0 [animation:pane-loading_.2s_.3s_forwards]">
          <Spinner /> {label}
        </p>
      )}
    </div>
  )
}

function SessionRoute() {
  const { machine, sessionId } = Route.useParams()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const live = useEventsConnected()
  const host = useMachines().find((item) => item.id === machine)
  const { data } = useQuery(sessionsQuery(machine, live))
  const info = data?.find((session) => session.id === sessionId)
  const stale = info?.reachable === false || host?.online === false
  const message = unavailableMessage(info, stale)
  const { q, group, sub } = Route.useSearch()
  // A loaded list without this session means it ended, so the Sessions view must not return to it.
  const ended = !!data && !info
  useEffect(() => {
    rememberSession(ended ? undefined : { machine, sessionId, search: { q, group } })
  }, [ended, machine, sessionId, q, group])
  const listed = info && host ? { ...info, machine, machineName: host.name, offline: stale } : undefined
  const switchView = useMutation({
    mutationFn: (view: SessionView) => {
      if (!info) throw new Error("session unavailable")
      return api<SessionInfo>("/api/sessions", {
        machine,
        method: "POST",
        body: startSessionBody({
          directory: info.directory,
          harness: info.harness,
          mode: info.mode,
          view,
          agentSessionId: info.agent_session_id,
        }),
      })
    },
    onSuccess: (next) => {
      void queryClient.invalidateQueries({ queryKey: sessionsQuery(machine, false).queryKey })
      void navigate({ to: "/sessions/$machine/$sessionId", params: { machine, sessionId: next.id } })
    },
    onError: (error) => hud.error(`Couldn't start session: ${(error as Error).message}`),
  })
  const paneFailed = usePane(info?.transport === "acp" ? "conversation" : info || data ? "terminal" : undefined)
  if (message) return <UnavailableTerminal message={message} />
  // The pane depends on the transport, so wait for the list rather than guess Terminal
  // (which would load xterm and attach a socket to a chat session).
  if (!data && !stale) return <PaneLoading label="Loading session…" />
  const title = listed && sessionLabel(listed)
  const subtitle = listed && sessionDetail(listed).split(" · ").slice(listed.name ? 0 : 1).join(" · ")
  const acp = info?.transport === "acp"
  const ConversationPane = panes.conversation
  const TerminalPane = panes.terminal
  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="min-h-0 flex-1">
        {acp && ConversationPane ? (
          <ConversationPane
            key={`${machine}/${sessionId}`}
            machine={machine}
            sessionId={sessionId}
            info={info}
            subagent={sub}
            helpers={data?.filter((session) => session.parent_id === sessionId)}
            readOnly={stale}
            title={title || undefined}
            subtitle={subtitle || undefined}
            onSwitchTerminal={() => {
              if (stale || info?.archived_at || viewSwitchNeedsResume(info)) return
              switchView.mutate("terminal")
            }}
          />
        ) : !acp && TerminalPane ? (
          <TerminalPane
            key={`${machine}/${sessionId}`}
            machine={machine}
            sessionId={sessionId}
            info={info}
            title={title || undefined}
            subtitle={subtitle || undefined}
            listed={!data || !!info}
          />
        ) : (
          <PaneLoading label={acp ? "Loading conversation…" : "Loading terminal…"} failed={paneFailed} />
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
    const info = context.queryClient.getQueryData<SessionInfo[]>(sessionsQuery(params.machine, false).queryKey)?.find((item) => item.id === params.sessionId)
    if (!info) return
    if (info.transport !== "acp") {
      void loadPane.terminal().catch(() => undefined)
      return
    }
    void loadPane.conversation().catch(() => undefined)
    void context.queryClient.prefetchQuery(conversationEventsQuery(params.machine, params.sessionId)).catch(() => undefined)
  },
  component: SessionRoute,
})
