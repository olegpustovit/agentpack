import { createFileRoute, redirect } from "@tanstack/react-router"
import { phoneQuery } from "@/features/shell/use-is-phone"

export const Route = createFileRoute("/_app/machines/")({
  beforeLoad: () => {
    throw redirect({ to: window.matchMedia(phoneQuery).matches ? "/system" : "/machines/topology", replace: true })
  },
})
