import { useQuery } from "@tanstack/react-query"
import { createFileRoute } from "@tanstack/react-router"
import { lazy, Suspense } from "react"
import { z } from "zod"
import { useEventsConnected } from "@/features/events/use-events"
import { useMachines } from "@/features/machines/queries"
import { sessionsQuery } from "@/features/sessions/queries"
import { unavailableTerminal, UnavailableTerminal } from "@/features/terminal/unavailable"

// Loaded on demand so xterm stays out of the initial bundle.
const TerminalPane = lazy(() => import("@/features/terminal/terminal-pane"))

function SessionRoute() {
  const { machine, sessionId } = Route.useParams()
  const live = useEventsConnected()
  // Fetch only once the machine is known online, as the sidebar does; an offline one shows its cache.
  const online = useMachines().find((item) => item.id === machine)?.online
  const stale = online === false
  const { data } = useQuery({ ...sessionsQuery(machine, live), enabled: online === true })
  const info = data?.find((session) => session.id === sessionId)
  const unavailable = unavailableTerminal(info, stale)
  if (unavailable) return <UnavailableTerminal unavailable={unavailable} />
  return (
    <Suspense fallback={<p className="p-(--s4) text-(--fg2)">Loading terminal…</p>}>
      <TerminalPane key={`${machine}/${sessionId}`} machine={machine} sessionId={sessionId} info={info} listed={!data || !!info} />
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
