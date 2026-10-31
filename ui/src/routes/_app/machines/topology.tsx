import { createFileRoute } from "@tanstack/react-router"
import { TopologyView } from "@/features/machines/topology"

export const Route = createFileRoute("/_app/machines/topology")({
  component: TopologyView,
})
