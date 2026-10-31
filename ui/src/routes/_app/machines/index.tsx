import { createFileRoute } from "@tanstack/react-router"
import { z } from "zod"
import { Empty } from "@/components/kit/feedback"

// Phones render the list alone (ListDetail shows children only once a detail is open), so this pane is desktop-only.
export const Route = createFileRoute("/_app/machines/")({
  validateSearch: z.object({ q: z.string().optional().catch(undefined) }),
  component: () => <Empty title="Select a machine">Pick a machine on the left to see its projects and pinned packages.</Empty>,
})
