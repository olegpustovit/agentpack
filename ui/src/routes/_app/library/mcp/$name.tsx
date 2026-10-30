import { createFileRoute } from "@tanstack/react-router"
import { z } from "zod"

export const Route = createFileRoute("/_app/library/mcp/$name")({
  validateSearch: z.object({
    q: z.string().optional().catch(undefined),
    type: z.string().optional().catch(undefined),
    sort: z.string().optional().catch(undefined),
    add: z.string().optional().catch(undefined),
  }),
  component: () => <p>mcp</p>,
})
