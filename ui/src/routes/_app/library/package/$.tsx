import { createFileRoute } from "@tanstack/react-router"
import { z } from "zod"
import { PackageInspector } from "@/features/library/package-inspector"

export const Route = createFileRoute("/_app/library/package/$")({
  validateSearch: z.object({
    q: z.string().optional().catch(undefined),
    type: z.string().optional().catch(undefined),
    sort: z.string().optional().catch(undefined),
    add: z.coerce.string().optional().catch(undefined),
    install: z.coerce.string().optional().catch(undefined),
  }),
  component: PackageRoute,
})

function PackageRoute() {
  const { _splat = "" } = Route.useParams()
  // Keyed by module so a version picked for one package does not carry to the next.
  return <PackageInspector key={_splat} module={_splat} />
}
