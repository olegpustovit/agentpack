import { createFileRoute } from "@tanstack/react-router"
import { z } from "zod"

export const Route = createFileRoute("/login")({
  validateSearch: z.object({ return_to: z.string().optional().catch(undefined) }),
  component: () => <p>login</p>,
})
