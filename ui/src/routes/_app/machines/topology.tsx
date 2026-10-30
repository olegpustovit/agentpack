import { createFileRoute } from "@tanstack/react-router"

export const Route = createFileRoute("/_app/machines/topology")({
  component: () => <p>topology</p>,
})
