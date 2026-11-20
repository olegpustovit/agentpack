import { createFileRoute } from "@tanstack/react-router"
import { search } from "@/lib/search"
import { DiscoverView } from "@/features/library/discover-view"

export const Route = createFileRoute("/_app/library/discover")({
  validateSearch: search({
    q: "text",
    type: "text",
    sort: "text",
    add: "flag",
  }),
  component: DiscoverView,
})
