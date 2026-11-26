import { lazy, Suspense } from "react"
import { useQuery } from "@tanstack/react-query"
import { createFileRoute, Outlet, redirect, useRouterState } from "@tanstack/react-router"
import { RowSkeleton } from "@/components/kit/feedback"
import { safeReturnTo } from "@/features/auth/return-to"
import { EventsProvider } from "@/features/events/use-events"
import { draftsQuery } from "@/features/library/queries"
import { AppShell } from "@/features/shell/shell"
import { SidebarProvider } from "@/features/shell/sidebar"
import { routePosition } from "@/features/shell/views"

// Usage, machines and settings share one sidebar, kept mounted so its scroll and search survive.
const SystemLayout = lazy(() => import("@/features/system/system-layout"))

export const Route = createFileRoute("/_app")({
  beforeLoad: ({ context, location }) => {
    if (!context.accountStatus.authenticated) {
      throw redirect({ to: "/login", search: { return_to: safeReturnTo(location.href) } })
    }
  },
  component: AppLayout,
})

function AppLayout() {
  // Keep the shell's drafts read subscribed so reconnect catch-up refreshes it before Library opens.
  useQuery(draftsQuery)
  return (
    <EventsProvider>
      <SidebarProvider>
        <AppShell>
          <View />
        </AppShell>
      </SidebarProvider>
    </EventsProvider>
  )
}

function View() {
  // The rendered match, not the URL: the URL moves first, and the page being left must not be rewrapped.
  const system = useRouterState({ select: (state) => routePosition(state.matches.at(-1)?.pathname ?? "").view === "system" })
  if (!system) return <Outlet />
  return (
    <Suspense fallback={<RowSkeleton lines={2} />}>
      <SystemLayout><Outlet /></SystemLayout>
    </Suspense>
  )
}
