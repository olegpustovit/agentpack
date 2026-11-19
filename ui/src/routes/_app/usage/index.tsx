import { createFileRoute } from "@tanstack/react-router"
import { UsageView } from "@/features/usage/usage-view"

export const Route = createFileRoute("/_app/usage/")({
  component: () => <UsageView view={{ kind: "overview" }} />,
})
