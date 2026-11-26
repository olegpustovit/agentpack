import { createFileRoute, notFound, redirect } from "@tanstack/react-router"

// Sources became filters; old links keep working. Project links carried inode IDs, so they open unfiltered.
export const Route = createFileRoute("/_app/usage/$kind/$id")({
  beforeLoad: ({ params: { kind, id }, search }) => {
    if (kind !== "project" && kind !== "machine" && kind !== "agent") throw notFound()
    const { range, metric } = search
    throw redirect({ to: "/usage", search: { range, metric, ...(kind === "project" ? {} : { [kind]: id }) }, replace: true })
  },
})
