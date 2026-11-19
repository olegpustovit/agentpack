import { createFileRoute } from "@tanstack/react-router"
import { search } from "@/lib/search"
import { Empty } from "@/components/kit/feedback"

export const Route = createFileRoute("/_app/sessions/")({
  validateSearch: search({
    q: "text",
    group: "text",
    new: "flag",
  }),
  component: () => <Empty title="Select a session">Pick a session on the left to open its terminal.</Empty>,
})
