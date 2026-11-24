import { createRootRouteWithContext, Outlet } from "@tanstack/react-router"
import type { QueryClient } from "@tanstack/react-query"
import { Announcement } from "@/components/kit/feedback"
import { GatewayUnavailable, PageNotFound } from "@/features/auth/recovery"
import { configQuery } from "@/lib/config"
import { statusQuery } from "@/features/status/queries"

export const Route = createRootRouteWithContext<{ queryClient: QueryClient }>()({
  beforeLoad: async ({ context }) => ({
    accountStatus: await context.queryClient.query(statusQuery),
  }),
  loader: ({ context }) => {
    if (context.accountStatus.claimed) return context.queryClient.query(configQuery)
  },
  pendingComponent: () => (
    <main className="grid min-h-dvh place-items-center">
      <Announcement>Connecting to gateway…</Announcement>
    </main>
  ),
  component: () => <Outlet />,
  errorComponent: GatewayUnavailable,
  notFoundComponent: PageNotFound,
})
