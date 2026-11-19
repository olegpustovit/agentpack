import { createFileRoute } from "@tanstack/react-router"
import { search } from "@/lib/search"
import { Empty } from "@/components/kit/feedback"

export const Route = createFileRoute("/_app/library/")({
  validateSearch: search({
    q: "text",
    type: "text",
    sort: "text",
    add: "flag",
  }),
  component: () => <Empty title="Select an item">Pick a skill, plugin, MCP server or agentpack on the left.</Empty>,
})
