import { createFileRoute, Outlet } from "@tanstack/react-router"
import { ListDetail } from "@/features/shell/list-detail"

export const Route = createFileRoute("/_app/sessions")({
  component: () => (
    <ListDetail list={<p>Session list</p>}>
      <Outlet />
    </ListDetail>
  ),
})
