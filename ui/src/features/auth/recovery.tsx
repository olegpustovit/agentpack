import { useTransition } from "react"
import { Link, useRouter, useRouterState } from "@tanstack/react-router"
import { Button } from "@/components/ui/button"
import { AuthCard } from "./auth-card"
import { returnToLabel, safeReturnTo } from "./return-to"

const linkStyle = "text-(--accent) underline underline-offset-4"

export function GatewayUnavailable() {
  const router = useRouter()
  const location = useRouterState({ select: (state) => state.location })
  const [retrying, retry] = useTransition()
  const returnTo = location.search.return_to
  const next = safeReturnTo(typeof returnTo === "string" ? returnTo : undefined) ?? safeReturnTo(location.href)
  const destination = next ? returnToLabel(next) : location.pathname === "/login" ? "Sign in" : location.pathname
  return (
    <AuthCard>
      <div className="flex flex-col gap-(--s2)">
        <h1 className="text-(length:--fs-lg) font-semibold">Couldn’t reach the gateway</h1>
        <p className="text-(--fg2)">Agentpack couldn’t finish opening this page. Check that the gateway is running and try again.</p>
      </div>
      <p>Destination <strong><bdi>{destination}</bdi></strong></p>
      <Button type="button" variant="primary" disabled={retrying} onClick={() => retry(async () => { await router.invalidate() })}>
        {retrying ? "Retrying…" : "Retry"}
      </Button>
      <Link className={linkStyle} to="/sessions">Go to Sessions</Link>
    </AuthCard>
  )
}

export function PageNotFound() {
  const path = useRouterState({ select: (state) => state.location.pathname })
  return (
    <AuthCard>
      <h1 className="text-(length:--fs-lg) font-semibold">No page at <bdi>{path}</bdi></h1>
      <nav aria-label="Go to a section" className="flex flex-wrap gap-(--s5)">
        <Link className={linkStyle} to="/sessions">Sessions</Link>
        <Link className={linkStyle} to="/library">Library</Link>
        <Link className={linkStyle} to="/settings">Settings</Link>
      </nav>
    </AuthCard>
  )
}
