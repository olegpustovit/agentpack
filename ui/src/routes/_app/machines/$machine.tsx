import { createFileRoute } from "@tanstack/react-router"
import { MachineDetail } from "@/features/machines/machine-detail"

export const Route = createFileRoute("/_app/machines/$machine")({
  component: MachineRoute,
})

// Keyed by machine so each machine starts with no open sheet or confirm.
function MachineRoute() {
  const { machine } = Route.useParams()
  return <MachineDetail key={machine} id={machine} />
}
