import { createFileRoute } from "@tanstack/react-router"
import { z } from "zod"

export const Route = createFileRoute("/_app/limits")({
  validateSearch: z.object({ machine: z.string().optional().catch(undefined) }),
  component: () => <p>limits</p>,
})
