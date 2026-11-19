import { createFileRoute } from "@tanstack/react-router"
import { search } from "@/lib/search"
import { McpInspector } from "@/features/library/mcp-inspector"

export const Route = createFileRoute("/_app/library/mcp/$name")({
  validateSearch: search({
    q: "text",
    type: "text",
    sort: "text",
    add: "flag",
  }),
  component: McpRoute,
})

function McpRoute() {
  const { name } = Route.useParams()
  return <McpInspector name={name} />
}
