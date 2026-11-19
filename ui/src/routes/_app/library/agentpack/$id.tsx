import { createFileRoute } from "@tanstack/react-router"
import { search } from "@/lib/search"
import { AgentpackInspector } from "@/features/library/agentpack-inspector"

export const Route = createFileRoute("/_app/library/agentpack/$id")({
  validateSearch: search({
    q: "text",
    type: "text",
    sort: "text",
    add: "flag",
    revision: "text",
    restore: "flag",
    save: "flag",
    rename: "flag",
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
