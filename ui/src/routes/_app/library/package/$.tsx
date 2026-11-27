import { createFileRoute } from "@tanstack/react-router"
import { search } from "@/lib/search"
import { PackageInspector } from "@/features/library/package-inspector"

export const Route = createFileRoute("/_app/library/package/$")({
  validateSearch: search({
    q: "text",
    type: "text",
    sort: "text",
    add: "flag",
  }),
  component: PackageRoute,
})

function PackageRoute() {
  const { _splat = "" } = Route.useParams()
  // Keyed by module so a version picked for one package does not carry to the next.
  return <PackageInspector key={_splat} module={_splat} />
}
