import { createFileRoute, redirect } from "@tanstack/react-router"
import { search } from "@/lib/search"

// Limits moved into Usage; old links and bookmarks keep working.
export const Route = createFileRoute("/_app/limits")({
  validateSearch: search({ machine: "text" }),
  beforeLoad: ({ search }) => {
    throw redirect({ to: "/usage/limits", search, replace: true })
  },
})
