import { createFileRoute, notFound } from "@tanstack/react-router"
import { viewKinds } from "@/features/usage/model"
import { UsageView } from "@/features/usage/usage-view"

// kind is project (folder ID), machine (node ID) or agent (harness).
export const Route = createFileRoute("/_app/usage/$kind/$id")({
  beforeLoad: ({ params }) => {
    if (!(viewKinds as readonly string[]).includes(params.kind)) throw notFound()
  },
  component: UsageDrill,
})

// Keyed so a new source starts without the previous one's pinned day or sort.
function UsageDrill() {
  const { kind, id } = Route.useParams()
  return <UsageView key={`${kind}:${id}`} view={{ kind: kind as (typeof viewKinds)[number], id }} />
}
