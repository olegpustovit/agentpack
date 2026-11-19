import { createFileRoute } from "@tanstack/react-router"
import { search } from "@/lib/search"
import { LoginView } from "@/features/auth/login-view"

export const Route = createFileRoute("/login")({
  validateSearch: search({ return_to: "text" }),
  component: LoginRoute,
})

function LoginRoute() {
  const { return_to } = Route.useSearch()
  return <LoginView returnTo={return_to} />
}
