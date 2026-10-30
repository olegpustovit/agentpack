import { createFileRoute, Outlet, redirect } from "@tanstack/react-router"
import { statusQuery } from "@/features/status/queries"
import { EventsProvider } from "@/features/events/use-events"

export const Route = createFileRoute("/_app")({
  beforeLoad: async ({ context }) => {
    const status = await context.queryClient.query({ ...statusQuery, staleTime: 0 })
    if (!status.authenticated) throw redirect({ to: "/login", search: { return_to: undefined } })
  },
  component: () => (
    <EventsProvider>
      <div>
        <Outlet />
      </div>
    </EventsProvider>
  ),
})
