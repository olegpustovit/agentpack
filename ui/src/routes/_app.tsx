import { createFileRoute, Outlet, redirect } from "@tanstack/react-router"
import { statusQuery } from "@/features/status/queries"
import { EventsProvider } from "@/features/events/use-events"
import { AppShell } from "@/features/shell/shell"
import { SidebarProvider } from "@/features/shell/sidebar"

export const Route = createFileRoute("/_app")({
  beforeLoad: async ({ context }) => {
    const status = await context.queryClient.query({ ...statusQuery, staleTime: 0 })
    if (!status.authenticated) throw redirect({ to: "/login", search: { return_to: undefined } })
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
