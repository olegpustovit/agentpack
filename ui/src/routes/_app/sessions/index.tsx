import { createFileRoute } from "@tanstack/react-router"
import { z } from "zod"
import { Empty } from "@/components/kit/feedback"

export const Route = createFileRoute("/_app/sessions/")({
  validateSearch: z.object({
    q: z.string().optional().catch(undefined),
    group: z.string().optional().catch(undefined),
    new: z.coerce.string().optional().catch(undefined),
  }),
  component: () => <Empty title="Select a session">Pick a session on the left to open its terminal.</Empty>,
})
