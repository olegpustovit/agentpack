import { createFileRoute } from "@tanstack/react-router"
import { z } from "zod"

export const Route = createFileRoute("/_app/library/agentpack/$id")({
  validateSearch: z.object({
    q: z.string().optional().catch(undefined),
    type: z.string().optional().catch(undefined),
    sort: z.string().optional().catch(undefined),
    add: z.string().optional().catch(undefined),
    revision: z.string().optional().catch(undefined),
    restore: z.string().optional().catch(undefined),
  }),
  component: () => <p>agentpack</p>,
})
