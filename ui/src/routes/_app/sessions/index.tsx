import { createFileRoute } from "@tanstack/react-router"
import { z } from "zod"

export const Route = createFileRoute("/_app/sessions/")({
  validateSearch: z.object({
    q: z.string().optional().catch(undefined),
    group: z.string().optional().catch(undefined),
    new: z.string().optional().catch(undefined),
  }),
  component: () => <p>sessions</p>,
})
