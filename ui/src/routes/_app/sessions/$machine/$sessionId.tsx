import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { createFileRoute, useNavigate } from "@tanstack/react-router"
import { useEffect, useState } from "react"
import { z } from "zod"
import { useEventsConnected } from "@/features/events/use-events"
import { useMachines } from "@/features/machines/queries"
import { sessionDetail, sessionLabel } from "@/features/sessions/group"
import { sessionsQuery } from "@/features/sessions/queries"
import { startSessionBody, type SessionView } from "@/features/sessions/rules"
import { SessionViewSwitch } from "@/features/sessions/session-view-switch"
import { sessionViewFromTransport, viewSwitchNeedsResume } from "@/features/sessions/view-switch"
import { rememberSession } from "@/features/shell/views"
import { unavailableMessage, UnavailableTerminal } from "@/features/terminal/unavailable"
import { api, LOCAL } from "@/lib/api"
import type { SessionInfo } from "@/lib/api-types"
import { hud } from "@/lib/hud"

// Loaded on demand so xterm and the conversation UI stay out of the initial bundle. Not
// React.lazy: React holds a Suspense reveal for ~300 ms, which every session open paid.
type ConversationPaneType = typeof import("@/features/conversation/conversation-pane").default
type TerminalPaneType = typeof import("@/features/terminal/terminal-pane").default
const panes: { conversation?: ConversationPaneType; terminal?: TerminalPaneType } = {}
const loadPane = {
  conversation: () => import("@/features/conversation/conversation-pane").then((module) => (panes.conversation = module.default)),
  terminal: () => import("@/features/terminal/terminal-pane").then((module) => (panes.terminal = module.default)),
}

function usePane(kind: keyof typeof loadPane | undefined) {
  const [, loaded] = useState(0)
  useEffect(() => {
    if (kind && !panes[kind]) void loadPane[kind]().then(() => loaded((count) => count + 1))
  }, [kind])
}

function SessionRoute() {
  const { machine, sessionId } = Route.useParams()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const live = useEventsConnected()
  // Fetch only once the machine is known online, as the sidebar does; an offline one shows its cache.
  // This machine is online by definition, so its list does not wait for the machine list.
  const host = useMachines().find((item) => item.id === machine)
  const online = machine === LOCAL || host?.online
  const stale = online === false
  const { data } = useQuery({ ...sessionsQuery(machine, live), enabled: online === true })
  const info = data?.find((session) => session.id === sessionId)
  const message = unavailableMessage(info, stale)
  const { q, group } = Route.useSearch()
  // A loaded list without this session means it ended, so the Sessions view must not return to it.
  const ended = !!data && !info
  useEffect(() => {
    rememberSession(ended ? undefined : { machine, sessionId, search: { q, group } })
  }, [ended, machine, sessionId, q, group])
  const listed = info && host ? { ...info, machine, machineName: host.name, offline: !host.online } : undefined
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
  usePane(info?.transport === "acp" ? "conversation" : info || data || stale ? "terminal" : undefined)
  if (message) return <UnavailableTerminal message={message} />
  // The pane depends on the transport, so wait for the list rather than guess Terminal
  // (which would load xterm and attach a socket to a chat session).
  if (!data && !stale) return <p className="p-(--s4) text-(--fg2)">Loading session…</p>
  const title = listed && sessionLabel(listed)
  const subtitle = listed && sessionDetail(listed).split(" · ").slice(listed.name ? 0 : 1).join(" · ")
  const acp = info?.transport === "acp"
  const currentView = sessionViewFromTransport(info?.transport)
  const ConversationPane = panes.conversation
  const TerminalPane = panes.terminal
  return (
    <div className="flex h-full min-h-0 flex-col">
      <SessionViewSwitch
        info={info}
        pending={switchView.isPending}
        onSwitch={(view) => {
          if (view === currentView || !info) return
          if (viewSwitchNeedsResume(info)) return
          switchView.mutate(view)
        }}
      />
      <div className="min-h-0 flex-1">
        {acp && ConversationPane ? (
          <ConversationPane
            key={`${machine}/${sessionId}`}
            machine={machine}
            sessionId={sessionId}
            info={info}
            title={title || undefined}
            subtitle={subtitle || undefined}
            onSwitchTerminal={() => {
              if (viewSwitchNeedsResume(info)) return
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
          <p className="p-(--s4) text-(--fg2)">{acp ? "Loading conversation…" : "Loading terminal…"}</p>
        )}
      </div>
    </div>
  )
}

export const Route = createFileRoute("/_app/sessions/$machine/$sessionId")({
  validateSearch: z.object({
    q: z.string().optional().catch(undefined),
    group: z.string().optional().catch(undefined),
    new: z.coerce.string().optional().catch(undefined),
  }),
  component: SessionRoute,
})
