import { useState, type ComponentProps } from "react"
import { Link } from "@tanstack/react-router"
import { SearchField } from "@/components/kit/list"
import { menuRow, menuSurface } from "@/components/kit/overlays"
import { Button } from "@/components/ui/button"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { useMachineQueries, useMachines } from "@/features/machines/queries"
import { projectsQuery } from "@/features/sessions/queries"
import { canStart, mergeProjects, placeLabel } from "@/features/sessions/rules"
import type { MCPServerInput } from "@/lib/api-types"
import { cn } from "@/lib/utils"

type ProjectInstall = { spec: string; server?: never } | { server: MCPServerInput; spec?: never }

declare module "@tanstack/history" {
  interface HistoryState {
    projectInstall?: ProjectInstall
  }
}

export function InstallProjectMenu({ name, target, ...button }: { name: string; target: ProjectInstall } & ComponentProps<typeof Button>) {
  const [open, setOpen] = useState(false)
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger render={<Button type="button" {...button} />} />
      <PopoverContent aria-label="Choose a project" align="end" className={cn(menuSurface, "w-72 gap-(--s1)")}>
        {open && <ProjectChoices name={name} target={target} onChoose={() => setOpen(false)} />}
      </PopoverContent>
    </Popover>
  )
}

function ProjectChoices({ name, target, onChoose }: { name: string; target: ProjectInstall; onChoose: () => void }) {
  const [filter, setFilter] = useState("")
  const machines = useMachines().filter((machine) => machine.online)
  const recent = useMachineQueries(projectsQuery)
  const rows = Object.fromEntries(machines.map(({ id }) => [id, recent.byMachine[id]?.data]))
  const projects = mergeProjects(machines, rows).flatMap(({ name, places }) =>
    places.filter(canStart).map((place) => ({ place, label: `${name} · ${placeLabel(place, places)}` })),
  ).sort((a, b) => (Date.parse(b.place.row.last_used) || 0) - (Date.parse(a.place.row.last_used) || 0))
  const needle = filter.trim().toLowerCase()
  const matches = projects.filter(({ label, place }) => `${label} ${place.row.directory}`.toLowerCase().includes(needle))

  return (
    <>
      {(projects.length > 8 || filter !== "") && <SearchField aria-label="Search projects" placeholder="Search projects" value={filter} onChange={(event) => setFilter(event.target.value)} />}
      <div className="max-h-64 overflow-y-auto">
        {matches.map(({ place, label }) => (
          <Link key={place.key} to="/sessions/project/$machine/$folder" params={{ machine: place.machine, folder: place.row.directory }}
            search={{ install: 1, find: name }} state={{ projectInstall: target }} onClick={onChoose} title={place.row.directory}
            className={cn(menuRow, "flex items-center hover:bg-(--hover) pointer-coarse:min-h-11")}>
            <span className="truncate">{label}</span>
          </Link>
        ))}
        {matches.length === 0 && <p role="status" className="px-(--s2) py-(--s2) text-(--fg2)">{recent.firstLoadPending ? "Loading projects…" : needle ? "No matching projects" : "No recent projects on online machines"}</p>}
      </div>
    </>
  )
}
