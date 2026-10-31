import { createFileRoute, Outlet, redirect } from "@tanstack/react-router"
import { Announcement } from "@/components/kit/feedback"
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
  // The shell has not mounted while the first status check is pending.
  pendingComponent: () => (
    <main className="grid min-h-dvh place-items-center">
      <Announcement>Connecting to gateway…</Announcement>
    </main>
  ),
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
