import { createFileRoute, redirect } from "@tanstack/react-router"
import { phoneQuery } from "@/features/shell/use-is-phone"

// A phone shows the System list here (the layout's sidebar); a desktop already has it, so it opens Usage.
export const Route = createFileRoute("/_app/system")({
  beforeLoad: () => {
    if (!window.matchMedia(phoneQuery).matches) throw redirect({ to: "/usage", replace: true })
  },
  component: () => null,
})
