import { createRootRouteWithContext, Outlet } from "@tanstack/react-router"
import type { QueryClient } from "@tanstack/react-query"

export const Route = createRootRouteWithContext<{ queryClient: QueryClient }>()({
  component: () => <Outlet />,
  errorComponent: ({ error }) => <p role="alert">{error instanceof Error ? error.message : String(error)}</p>,
  notFoundComponent: () => <p>Not found</p>,
})
