import { createFileRoute } from "@tanstack/react-router"

export const Route = createFileRoute("/_app/settings/$pane")({
  component: () => <p>settings</p>,
})
