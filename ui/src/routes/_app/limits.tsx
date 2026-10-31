import { createFileRoute } from "@tanstack/react-router"
import { z } from "zod"
import { LimitsView } from "@/features/limits/limits-view"

export const Route = createFileRoute("/_app/limits")({
  validateSearch: z.object({ machine: z.string().optional().catch(undefined) }),
  component: LimitsView,
})
