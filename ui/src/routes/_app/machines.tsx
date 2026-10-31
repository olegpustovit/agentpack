import { createFileRoute, Outlet } from "@tanstack/react-router"
import { ListDetail } from "@/features/shell/list-detail"
import { MachineList } from "@/features/machines/machine-list"

export const Route = createFileRoute("/_app/machines")({
  component: () => (
    <ListDetail list={<MachineList />}>
      <Outlet />
    </ListDetail>
  ),
})
