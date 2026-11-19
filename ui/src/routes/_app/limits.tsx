import { createFileRoute } from "@tanstack/react-router"
import { search } from "@/lib/search"
import { LimitsView } from "@/features/limits/limits-view"

export const Route = createFileRoute("/_app/limits")({
  validateSearch: search({ machine: "text" }),
  component: LimitsView,
})
