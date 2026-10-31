import { useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Laptop, Package, Server, SquareTerminal, Trash2 } from "lucide-react"
import { DataGrid, DataGridCell, DataGridHead, DataGridHeaderCell, DataGridRow, Hash, Inspector, InspectorHead, Section } from "@/components/kit/data"
import { Banner, Empty, RowSkeleton } from "@/components/kit/feedback"
import { ConfirmAlert, ConfirmAlertContent, RowContextMenu, RowContextMenuContent, RowContextMenuItem, RowContextMenuTrigger } from "@/components/kit/overlays"
import { StatusDot } from "@/components/kit/status-dot"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { useEventsConnected } from "@/features/events/use-events"
import { AddPackageSheet } from "@/features/library/add-package-sheet"
import { NewSessionSheet } from "@/features/sessions/new-session-sheet"
import { projectsQuery } from "@/features/sessions/queries"
import { api, LOCAL } from "@/lib/api"
import type { ProjectRow } from "@/lib/api-types"
import { basename, harnessName, relativeTime } from "@/lib/format"
import { hud } from "@/lib/hud"
import { buildMatrix } from "./matrix"
import { machineIdentityQuery, topologyQuery, useMachines, type Machine } from "./queries"

type Action = { kind: "new" | "install"; machine: Machine; project: ProjectRow }
type ForgetTarget = { machine: Machine; project: ProjectRow }

function projectLabel(project: ProjectRow): string {
  return project.name || basename(project.directory)
}

export function MachineDetail({ id }: { id: string }) {
  const machine = useMachines().find((item) => item.id === id)
  if (!machine) return <Empty title="Machine not found">Choose a machine from the list.</Empty>
  return <MachineView machine={machine} />
}

function MachineView({ machine }: { machine: Machine }) {
  const live = useEventsConnected()
  const machines = useMachines()
  const queryClient = useQueryClient()
  const identity = useQuery({ ...machineIdentityQuery(machine.id), enabled: machine.online })
  const topology = useQuery({ ...topologyQuery(live), enabled: machine.id !== LOCAL })
  // Offline machines are disabled, so this returns the last known projects from the cache.
  const projects = useQuery({ ...projectsQuery(machine.id), enabled: machine.online })
  const [action, setAction] = useState<Action | null>(null)
  const [forgetting, setForgetting] = useState<ForgetTarget | null>(null)

  const forget = useMutation({
    mutationFn: (target: ForgetTarget) =>
      api<unknown>("/api/recent", { machine: target.machine.id, method: "DELETE", body: { directory: target.project.directory } }),
    onSuccess: (_result, target) => {
      void queryClient.invalidateQueries({ queryKey: projectsQuery(target.machine.id).queryKey })
      hud.success(`Forgot ${projectLabel(target.project)}`)
    },
    onError: (error, target) => hud.error(`Couldn't forget ${projectLabel(target.project)}: ${error.message}`),
  })

  const peer = topology.data?.peers.find((item) => item.id === machine.id)
  const facts = [
    identity.data?.node.os,
    machine.id === LOCAL ? "local" : peer?.connection,
    identity.data ? `version ${identity.data.node.version}` : undefined,
  ]
  const matrix = buildMatrix(projects.data ?? [])
  const cached = projects.data !== undefined

  function closeAction(open: boolean) {
    if (!open) setAction(null)
  }

  return (
    <div className="h-full min-h-0 overflow-y-auto">
      <Inspector>
        {!machine.online && (
          <Banner className="col-span-full">
            {cached ? `${machine.name} is offline. Showing last known projects.` : `${machine.name} is offline. No projects are cached.`}
          </Banner>
        )}
        {projects.error && (
          <Banner tone="danger" className="col-span-full">
            {`Couldn't load projects from ${machine.name}: ${projects.error.message}`}
          </Banner>
        )}
        <InspectorHead
          icon={machine.id === LOCAL ? <Laptop aria-hidden className="size-(--s6)" /> : <Server aria-hidden className="size-(--s6)" />}
          title={machine.name}
          subtitle={facts.filter(Boolean).join(" · ")}
        >
          <StatusDot state={machine.online ? "running" : "offline"} />
        </InspectorHead>
        <Section title="Projects and pinned packages" className="col-span-full">
          {machine.online && projects.isPending && <RowSkeleton lines={2} />}
          {projects.data?.length === 0 && <Empty title="No projects yet">Folders appear here after an agent runs in them.</Empty>}
          {matrix.rows.length > 0 && (
            <DataGrid aria-label="Projects and pinned packages">
              <DataGridHead>
                <DataGridRow>
                  <DataGridHeaderCell>Project</DataGridHeaderCell>
                  {matrix.columns.map((column) => (
                    <DataGridHeaderCell key={column.module} title={column.module} className="w-40">
                      {column.name}
                    </DataGridHeaderCell>
                  ))}
                </DataGridRow>
              </DataGridHead>
              <tbody>
                {matrix.rows.map(({ project, pins }) => {
                  const usable = machine.online && project.available
                  const note = project.error || (project.available ? "" : project.unavailable_reason || "Unavailable")
                  return (
                    <RowContextMenu key={project.directory}>
                      <RowContextMenuTrigger render={<DataGridRow />}>
                        <DataGridCell>
                          <Tooltip>
                            <TooltipTrigger render={<button type="button" className="flex max-w-full min-w-0 flex-col text-left" />}>
                              <span className="truncate">{projectLabel(project)}</span>
                              {note && <span className="truncate text-(--fs-sm) text-(--fg2)">{note}</span>}
                            </TooltipTrigger>
                            <TooltipContent>
                              <span className="block">{project.directory}</span>
                              {project.launches.map((launch) => (
                                <span key={`${launch.harness}/${launch.mode}`} className="block">
                                  {`${harnessName(launch.harness)} · ${launch.mode || "default"} · ${relativeTime(new Date(launch.last_used))}`}
                                </span>
                              ))}
                            </TooltipContent>
                          </Tooltip>
                        </DataGridCell>
                        {matrix.columns.map((column) => {
                          const pin = pins.get(column.module)
                          return <DataGridCell key={column.module}>{pin ? <Hash value={pin} /> : <span className="text-(--fg3)">—</span>}</DataGridCell>
                        })}
                      </RowContextMenuTrigger>
                      <RowContextMenuContent>
                        <RowContextMenuItem icon={<SquareTerminal aria-hidden />} disabled={!usable} onClick={() => setAction({ kind: "new", machine, project })}>
                          New session…
                        </RowContextMenuItem>
                        <RowContextMenuItem icon={<Package aria-hidden />} disabled={!usable} onClick={() => setAction({ kind: "install", machine, project })}>
                          Install…
                        </RowContextMenuItem>
                        <RowContextMenuItem icon={<Trash2 aria-hidden />} disabled={!machine.online} onClick={() => setForgetting({ machine, project })}>
                          Forget
                        </RowContextMenuItem>
                      </RowContextMenuContent>
                    </RowContextMenu>
                  )
                })}
              </tbody>
            </DataGrid>
          )}
        </Section>
      </Inspector>

      <NewSessionSheet
        open={action?.kind === "new"}
        machines={machines}
        initialMachine={action?.machine.id}
        initialDirectory={action?.project.directory}
        onOpenChange={closeAction}
      />
      <AddPackageSheet
        open={action?.kind === "install"}
        initialMachine={action?.machine.id}
        initialDirectory={action?.project.directory}
        onOpenChange={closeAction}
      />
      <ConfirmAlert open={forgetting !== null} onOpenChange={(open) => { if (!open) setForgetting(null) }}>
        {forgetting && (
          <ConfirmAlertContent
            title={`Forget ${projectLabel(forgetting.project)}?`}
            description={`Removes this folder from recent folders on ${forgetting.machine.name}. Files in the folder are not changed.`}
            destructiveLabel="Forget"
            onConfirm={() => {
              const target = forgetting
              setForgetting(null)
              forget.mutate(target)
            }}
          />
        )}
      </ConfirmAlert>
    </div>
  )
}
