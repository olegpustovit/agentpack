import { createFileRoute, redirect } from "@tanstack/react-router"
import { SettingsView } from "@/features/settings/settings-view"
import { phoneQuery } from "@/features/shell/use-is-phone"

// Desktop opens General beside the list; a phone shows the section list as the index.
export const Route = createFileRoute("/_app/settings/")({
  beforeLoad: () => {
    if (!window.matchMedia(phoneQuery).matches) throw redirect({ to: "/settings/$pane", params: { pane: "general" } })
  },
  component: () => <SettingsView pane="" />,
})
