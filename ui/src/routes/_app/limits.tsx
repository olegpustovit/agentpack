import { createFileRoute, redirect } from "@tanstack/react-router"

// Limits moved into Usage; old links and bookmarks keep working.
export const Route = createFileRoute("/_app/limits")({
  beforeLoad: () => {
    throw redirect({ to: "/usage/limits", replace: true })
  },
})
