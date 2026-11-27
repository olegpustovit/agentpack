import { createFileRoute } from "@tanstack/react-router"
import { ProjectPage } from "@/features/sessions/project-page"
import { search } from "@/lib/search"

// One folder on one machine: what it has installed, and everything it could add. `install` focuses the search.
export const Route = createFileRoute("/_app/sessions/project/$machine/$folder")({
  validateSearch: search({ install: "flag" }),
  component: ProjectRoute,
})

function ProjectRoute() {
  const { machine, folder } = Route.useParams()
  const { install } = Route.useSearch()
  return <ProjectPage key={`${machine}\0${folder}`} machine={machine} directory={folder} install={install !== undefined} />
}
