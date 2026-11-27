import { createFileRoute } from "@tanstack/react-router"
import { ProjectPage } from "@/features/sessions/project-page"
import { search } from "@/lib/search"

// One folder on one machine: what it has installed, and everything it could add. `install` focuses the search.
export const Route = createFileRoute("/_app/sessions/project/$machine/$folder")({
  validateSearch: (input): { install?: number; find?: string } => {
    const { install, find } = search({ install: "flag", find: "text" })(input)
    return { install: install === undefined ? undefined : 1, find }
  },
  component: ProjectRoute,
})

function ProjectRoute() {
  const { machine, folder } = Route.useParams()
  const { install, find } = Route.useSearch()
  return <ProjectPage key={`${machine}\0${folder}\0${find ?? ""}\0${install ?? ""}`} machine={machine} directory={folder} install={install !== undefined} find={find} />
}
