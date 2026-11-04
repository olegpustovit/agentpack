import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { createFileRoute, useNavigate } from "@tanstack/react-router"
import { lazy, Suspense, useEffect } from "react"
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
import { api } from "@/lib/api"
import type { SessionInfo } from "@/lib/api-types"
import { hud } from "@/lib/hud"

// Loaded on demand so xterm and the conversation UI stay out of the initial bundle.
const TerminalPane = lazy(() => import("@/features/terminal/terminal-pane"))
const ConversationPane = lazy(() => import("@/features/conversation/conversation-pane"))

function SessionRoute() {
  const { machine, sessionId } = Route.useParams()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const live = useEventsConnected()
  // Fetch only once the machine is known online, as the sidebar does; an offline one shows its cache.
  const host = useMachines().find((item) => item.id === machine)
  const online = host?.online
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
  if (message) return <UnavailableTerminal message={message} />
  const title = listed && sessionLabel(listed)
  const subtitle = listed && sessionDetail(listed).split(" · ").slice(listed.name ? 0 : 1).join(" · ")
  const acp = info?.transport === "acp"
  const currentView = sessionViewFromTransport(info?.transport)
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
        <Suspense fallback={<p className="p-(--s4) text-(--fg2)">{acp ? "Loading conversation…" : "Loading terminal…"}</p>}>
          {acp ? (
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
          ) : (
            <TerminalPane
              key={`${machine}/${sessionId}`}
              machine={machine}
              sessionId={sessionId}
              info={info}
              title={title || undefined}
              subtitle={subtitle || undefined}
              listed={!data || !!info}
            />
          )}
        </Suspense>
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
