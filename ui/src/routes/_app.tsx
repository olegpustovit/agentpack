import { useQuery } from "@tanstack/react-query"
import { createFileRoute, Outlet, redirect } from "@tanstack/react-router"
import { safeReturnTo } from "@/features/auth/return-to"
import { EventsProvider } from "@/features/events/use-events"
import { draftsQuery } from "@/features/library/queries"
import { AppShell } from "@/features/shell/shell"
import { SidebarProvider } from "@/features/shell/sidebar"

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
          <Outlet />
        </AppShell>
      </SidebarProvider>
    </EventsProvider>
  )
}
