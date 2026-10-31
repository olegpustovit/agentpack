import { createFileRoute } from "@tanstack/react-router"
import { z } from "zod"
import { Empty } from "@/components/kit/feedback"

export const Route = createFileRoute("/_app/library/")({
  validateSearch: z.object({
    q: z.string().optional().catch(undefined),
    type: z.string().optional().catch(undefined),
    sort: z.string().optional().catch(undefined),
    add: z.coerce.string().optional().catch(undefined),
  }),
  component: () => <Empty title="Select an item">Pick a skill, plugin, MCP server or agentpack on the left.</Empty>,
})
