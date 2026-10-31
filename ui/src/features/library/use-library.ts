import { useQueries } from "@tanstack/react-query"
import { useMachineQueries, useMachines } from "@/features/machines/queries"
import type { LatestVersion } from "@/lib/api-types"
import { latestRequest } from "./inspect"
import { projectsQuery } from "@/features/sessions/queries"
import { buildLibrary, type LibraryInput, type LibraryItem } from "./model"
import { agentpacksQuery, latestQuery, packagesQuery } from "./queries"

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
    // An older peer answers the Library API with a raw 404; say it needs an update, as the list does.
    if (queries.some((query) => query.byMachine[machine.id]?.needsUpdate)) return [`${machine.name} needs an update to show its library.`]
    const error = queries.map((query) => query.errors[machine.id]).find(Boolean)
    return error ? [`Couldn't load library from ${machine.name}: ${error.message}`] : []
  })
  return { inputs, items: buildLibrary(inputs), loading: queries.some((query) => query.firstLoadPending), failures }
}

export type LatestByModule = { byModule: Record<string, LatestVersion>; checking: boolean; failed: boolean; refetch: () => void }

// The newest version of each source, keyed by lowercase module. Nothing is asked until `enabled`.
export function useLatest(inputs: LibraryInput[], modules: string[], enabled: boolean): LatestByModule {
  const requests = modules.flatMap((module) => latestRequest(inputs, module) ?? [])
  return useQueries({
    queries: requests.map((request) => ({ ...latestQuery(request), enabled })),
    combine: (results) => {
      const byModule: Record<string, LatestVersion> = {}
      results.forEach((result, index) => {
        if (result.data) byModule[requests[index]!.module.toLowerCase()] = result.data
      })
      return {
        byModule,
        checking: results.some((result) => result.isFetching),
        failed: results.some((result) => result.isError),
        refetch: () => results.forEach((result) => void result.refetch()),
      }
    },
  })
}
