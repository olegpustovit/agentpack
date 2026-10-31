import { createFileRoute } from "@tanstack/react-router"
import { z } from "zod"
import { LoginView } from "@/features/auth/login-view"

export const Route = createFileRoute("/login")({
  validateSearch: z.object({ return_to: z.string().optional().catch(undefined) }),
  component: LoginRoute,
})

function LoginRoute() {
  const { return_to } = Route.useSearch()
  return <LoginView returnTo={return_to} />
}
