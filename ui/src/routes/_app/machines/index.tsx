import { createFileRoute, redirect } from "@tanstack/react-router"
import { z } from "zod"

export const Route = createFileRoute("/_app/machines/")({
  validateSearch: z.object({ q: z.string().optional().catch(undefined) }),
  beforeLoad: ({ search }) => {
    throw redirect({ to: "/machines/$machine", params: { machine: "local" }, search })
  },
})
