import { createFileRoute } from "@tanstack/react-router"
import { z } from "zod"

export const Route = createFileRoute("/_app/library/package/$")({
  validateSearch: z.object({
    q: z.string().optional().catch(undefined),
    type: z.string().optional().catch(undefined),
    sort: z.string().optional().catch(undefined),
    add: z.string().optional().catch(undefined),
    install: z.string().optional().catch(undefined),
  }),
  component: () => <p>package</p>,
})
