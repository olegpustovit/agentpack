import { useQuery } from "@tanstack/react-query"
import { createFileRoute } from "@tanstack/react-router"
import { lazy, Suspense } from "react"
import { z } from "zod"
import { useEventsConnected } from "@/features/events/use-events"
import { useMachines } from "@/features/machines/queries"
import { sessionDetail, sessionLabel } from "@/features/sessions/group"
import { sessionsQuery } from "@/features/sessions/queries"
import { unavailableMessage, UnavailableTerminal } from "@/features/terminal/unavailable"

// Loaded on demand so xterm stays out of the initial bundle.
const TerminalPane = lazy(() => import("@/features/terminal/terminal-pane"))

function SessionRoute() {
  const { machine, sessionId } = Route.useParams()
  const live = useEventsConnected()
  // Fetch only once the machine is known online, as the sidebar does; an offline one shows its cache.
  const host = useMachines().find((item) => item.id === machine)
  const online = host?.online
  const stale = online === false
  const { data } = useQuery({ ...sessionsQuery(machine, live), enabled: online === true })
  const info = data?.find((session) => session.id === sessionId)
  const message = unavailableMessage(info, stale)
  const listed = info && host ? { ...info, machine, machineName: host.name, offline: !host.online } : undefined
  if (message) return <UnavailableTerminal message={message} />
  return (
    <Suspense fallback={<p className="p-(--s4) text-(--fg2)">Loading terminal…</p>}>
      <TerminalPane key={`${machine}/${sessionId}`} machine={machine} sessionId={sessionId} info={info} title={listed && sessionLabel(listed)} subtitle={listed && sessionDetail(listed).split(" · ").slice(listed.name ? 0 : 1).join(" · ")} listed={!data || !!info} />
    </Suspense>
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
