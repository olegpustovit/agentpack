import { createFileRoute } from "@tanstack/react-router"
import { looseSearch } from "@/lib/search"
import { LimitsView } from "@/features/limits/limits-view"

export const Route = createFileRoute("/_app/usage/limits")({
  validateSearch: looseSearch({ machine: "text" }),
  component: LimitsView,
})
