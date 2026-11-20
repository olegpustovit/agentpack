import { createFileRoute } from "@tanstack/react-router"
import { search } from "@/lib/search"
import { SessionsHome } from "@/features/sessions/home"

export const Route = createFileRoute("/_app/sessions/")({
  validateSearch: search({
    q: "text",
    group: "text",
    new: "flag",
  }),
  component: SessionsHome,
})
