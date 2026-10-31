import { createFileRoute } from "@tanstack/react-router"
import { z } from "zod"
import { AgentpackInspector } from "@/features/library/agentpack-inspector"

export const Route = createFileRoute("/_app/library/agentpack/$id")({
  validateSearch: z.object({
    q: z.string().optional().catch(undefined),
    type: z.string().optional().catch(undefined),
    sort: z.string().optional().catch(undefined),
    add: z.coerce.string().optional().catch(undefined),
    revision: z.string().optional().catch(undefined),
    restore: z.coerce.string().optional().catch(undefined),
    save: z.coerce.string().optional().catch(undefined),
    rename: z.coerce.string().optional().catch(undefined),
  }),
  component: AgentpackRoute,
})

function AgentpackRoute() {
  const { id } = Route.useParams()
  const { revision } = Route.useSearch()
  const navigate = Route.useNavigate()
  // Replace, so moving through history does not stack entries.
  return (
    <AgentpackInspector
      id={id}
      revision={revision}
      onRevision={(next) => void navigate({ search: (prev) => ({ ...prev, revision: next }), replace: true })}
    />
  )
}
