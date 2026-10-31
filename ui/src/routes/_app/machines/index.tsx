import { createFileRoute, redirect } from "@tanstack/react-router"
import { z } from "zod"
import { Empty } from "@/components/kit/feedback"

// Desktop opens the local machine. Phones stay here, where ListDetail shows the list alone.
export const Route = createFileRoute("/_app/machines/")({
  validateSearch: z.object({ q: z.string().optional().catch(undefined) }),
  beforeLoad: () => {
    if (!matchMedia("(max-width: 760px)").matches) throw redirect({ to: "/machines/$machine", params: { machine: "local" }, replace: true })
  },
  component: () => <Empty title="Select a machine">Pick a machine on the left to see its projects and pinned packages.</Empty>,
})
