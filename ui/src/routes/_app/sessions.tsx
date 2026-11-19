import { createFileRoute, Outlet } from "@tanstack/react-router"
import { looseSearch } from "@/lib/search"
import { ListDetail } from "@/features/shell/list-detail"
import { SessionList } from "@/features/sessions/session-list"

export const Route = createFileRoute("/_app/sessions")({
  // Loose, so the child routes' own search keys pass through this layout.
  // machine and folder preselect where New session starts.
  validateSearch: looseSearch({
    q: "text",
    group: "text",
    new: "flag",
    machine: "text",
    folder: "text",
  }),
  component: () => (
    <ListDetail list={<SessionList />}>
      <Outlet />
    </ListDetail>
  ),
})
