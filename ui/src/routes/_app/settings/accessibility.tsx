import { createFileRoute, redirect } from "@tanstack/react-router"

// Accessibility is now a group in Appearance; old links keep working.
export const Route = createFileRoute("/_app/settings/accessibility")({
  beforeLoad: () => {
    throw redirect({ to: "/settings/$pane", params: { pane: "appearance" }, replace: true })
  },
})
