import { useMachineQueries, useMachines } from "@/features/machines/queries"
import { projectsQuery } from "@/features/sessions/queries"
import { buildLibrary, type LibraryInput, type LibraryItem } from "./model"
import { agentpacksQuery, packagesQuery } from "./queries"

// The list's three queries merged the same way, so an inspector sees what the list sees.
export function useLibrary(): { inputs: LibraryInput[]; items: LibraryItem[]; loading: boolean; failures: string[] } {
  const machines = useMachines()
  const packages = useMachineQueries(packagesQuery)
  const agentpacks = useMachineQueries(agentpacksQuery)
  const projects = useMachineQueries(projectsQuery)
  const queries = [packages, agentpacks, projects]
  const inputs: LibraryInput[] = machines.map((machine) => ({
    machine,
    packages: packages.byMachine[machine.id]?.data ?? [],
    agentpacks: agentpacks.byMachine[machine.id]?.data ?? [],
    projects: projects.byMachine[machine.id]?.data ?? [],
  }))
  const failures = machines.flatMap((machine) => {
    const error = queries.map((query) => query.errors[machine.id]).find(Boolean)
    return error ? [`Couldn't load library from ${machine.name}: ${error.message}`] : []
  })
  return { inputs, items: buildLibrary(inputs), loading: queries.some((query) => query.firstLoadPending), failures }
}
