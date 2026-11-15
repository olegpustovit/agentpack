import { createRootRouteWithContext, Outlet } from "@tanstack/react-router"
import type { QueryClient } from "@tanstack/react-query"
import { Announcement } from "@/components/kit/feedback"
import { configQuery } from "@/lib/config"
import { statusQuery } from "@/features/status/queries"

export const Route = createRootRouteWithContext<{ queryClient: QueryClient }>()({
  loader: async ({ context }) => {
    const status = await context.queryClient.query(statusQuery)
    if (status.claimed) return context.queryClient.query(configQuery)
  },
  pendingComponent: () => (
    <main className="grid min-h-dvh place-items-center">
      <Announcement>Connecting to gateway…</Announcement>
    </main>
  ),
  component: () => <Outlet />,
  errorComponent: ({ error }) => <p role="alert">{error instanceof Error ? error.message : String(error)}</p>,
  notFoundComponent: () => <p>Not found</p>,
})
