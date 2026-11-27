import { createFileRoute, redirect } from "@tanstack/react-router"
import { SettingsView } from "@/features/settings/settings-view"
import { search } from "@/lib/search"

// Account and Appearance live in General now; mesh enrollment lives on Topology.
function redirectMoved({ params }: { params: { pane: string } }): void {
  if (params.pane === "replication") throw redirect({ to: "/machines/topology", replace: true })
  if (["account", "appearance", "accessibility"].includes(params.pane)) throw redirect({ to: "/settings/$pane", params: { pane: "general" }, replace: true })
}

export const Route = createFileRoute("/_app/settings/$pane")({
  validateSearch: search({ issue: "text", status: "text", callback: "text", oauth_login: "text", connection: "text" }),
  beforeLoad: redirectMoved,
  component: SettingsRoute,
})

function SettingsRoute() {
  const { pane } = Route.useParams()
  return <SettingsView pane={pane} />
}
