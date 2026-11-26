import { createFileRoute, Outlet } from "@tanstack/react-router"
import { useQueryClient } from "@tanstack/react-query"
import { useEffect } from "react"
import { restoreUpdateJobs } from "@/features/machines/update-jobs"

export const Route = createFileRoute("/_app/machines")({
  component: MachinesLayout,
})

function MachinesLayout() {
  const queryClient = useQueryClient()
  useEffect(() => restoreUpdateJobs(queryClient), [queryClient])
  return <Outlet />
}
