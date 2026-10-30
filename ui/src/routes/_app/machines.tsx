import { createFileRoute, Outlet } from "@tanstack/react-router"
import { ListDetail } from "@/features/shell/list-detail"

export const Route = createFileRoute("/_app/machines")({
  component: () => (
    <ListDetail list={<p>Machine list</p>}>
      <Outlet />
    </ListDetail>
  ),
})
