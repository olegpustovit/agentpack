import { createFileRoute, Outlet, redirect } from "@tanstack/react-router"
import { draftsQuery } from "@/features/library/queries"
import { statusQuery } from "@/features/status/queries"
import { EventsProvider } from "@/features/events/use-events"
import { AppShell } from "@/features/shell/shell"
import { SidebarProvider } from "@/features/shell/sidebar"

export const Route = createFileRoute("/_app")({
  beforeLoad: async ({ context }) => {
    const status = await context.queryClient.query(statusQuery)
    if (!status.authenticated) throw redirect({ to: "/login", search: { return_to: undefined } })
    // Gateway-wide and listed by every Library view, so it loads with the shell, not on the first Library open.
    void context.queryClient.query(draftsQuery).catch(() => undefined)
  },
  component: () => (
    <EventsProvider>
      <SidebarProvider>
        <AppShell>
          <Outlet />
        </AppShell>
      </SidebarProvider>
    </EventsProvider>
  ),
})
