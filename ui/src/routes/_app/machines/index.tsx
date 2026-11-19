import { createFileRoute, redirect } from "@tanstack/react-router"
import { search } from "@/lib/search"
import { Empty } from "@/components/kit/feedback"

// Desktop opens the topology. Phones stay here, where ListDetail shows the list alone.
export const Route = createFileRoute("/_app/machines/")({
  validateSearch: search({ q: "text" }),
  beforeLoad: () => {
    if (!matchMedia("(max-width: 760px)").matches) throw redirect({ to: "/machines/topology", replace: true })
  },
  component: () => <Empty title="Select a machine">Pick a machine on the left to see its projects and pinned packages.</Empty>,
})
