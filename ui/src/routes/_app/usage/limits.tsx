import { createFileRoute } from "@tanstack/react-router"
import { LimitsView } from "@/features/limits/limits-view"

export const Route = createFileRoute("/_app/usage/limits")({
  component: LimitsView,
})
