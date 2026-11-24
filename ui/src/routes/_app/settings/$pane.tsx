import { createFileRoute } from "@tanstack/react-router"
import { SettingsView } from "@/features/settings/settings-view"
import { search } from "@/lib/search"

export const Route = createFileRoute("/_app/settings/$pane")({
  validateSearch: search({ issue: "text", status: "text", callback: "text", oauth_login: "text", connection: "text" }),
  component: SettingsRoute,
})

function SettingsRoute() {
  const { pane } = Route.useParams()
  return <SettingsView pane={pane} />
}
