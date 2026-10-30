import { createFileRoute } from "@tanstack/react-router"
import { z } from "zod"

export const Route = createFileRoute("/_app/sessions/$machine/$sessionId")({
  validateSearch: z.object({
    q: z.string().optional().catch(undefined),
    group: z.string().optional().catch(undefined),
    new: z.coerce.string().optional().catch(undefined),
  }),
  component: () => <p>session</p>,
})
