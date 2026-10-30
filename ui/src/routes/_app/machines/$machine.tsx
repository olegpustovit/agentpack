import { createFileRoute } from "@tanstack/react-router"
import { z } from "zod"

export const Route = createFileRoute("/_app/machines/$machine")({
  validateSearch: z.object({ q: z.string().optional().catch(undefined) }),
  component: () => <p>machine</p>,
})
