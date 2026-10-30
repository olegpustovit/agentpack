import { createFileRoute, Outlet } from "@tanstack/react-router"
import { ListDetail } from "@/features/shell/list-detail"

export const Route = createFileRoute("/_app/library")({
  component: () => (
    <ListDetail list={<p>Library list</p>}>
      <Outlet />
    </ListDetail>
  ),
})
