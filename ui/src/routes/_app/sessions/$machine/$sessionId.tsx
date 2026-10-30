import { useQuery } from "@tanstack/react-query"
import { createFileRoute } from "@tanstack/react-router"
import { lazy, Suspense } from "react"
import { z } from "zod"
import { useEventsConnected } from "@/features/events/use-events"
import { sessionsQuery } from "@/features/sessions/queries"

// Loaded on demand so xterm stays out of the initial bundle.
const TerminalPane = lazy(() => import("@/features/terminal/terminal-pane"))

function SessionRoute() {
  const { machine, sessionId } = Route.useParams()
  const live = useEventsConnected()
  const { data } = useQuery(sessionsQuery(machine, live))
  const info = data?.find((session) => session.id === sessionId)
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
