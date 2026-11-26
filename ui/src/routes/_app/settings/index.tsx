import { createFileRoute, redirect } from "@tanstack/react-router"
import { phoneQuery } from "@/features/shell/use-is-phone"

export const Route = createFileRoute("/_app/settings/")({
  beforeLoad: () => {
    if (window.matchMedia(phoneQuery).matches) throw redirect({ to: "/system", replace: true })
    throw redirect({ to: "/settings/$pane", params: { pane: "general" }, replace: true })
  },
})
