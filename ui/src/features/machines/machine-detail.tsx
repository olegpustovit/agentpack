import { useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import { Button } from "@/components/ui/button"
import { Package, Plus, Server, SquareTerminal, Trash2 } from "lucide-react"
import { DataGrid, DataGridCell, DataGridHead, DataGridHeaderCell, DataGridRow, Inspector, InspectorHead, KeyValue, Meter, Section } from "@/components/kit/data"
import { Banner, Empty, RowSkeleton } from "@/components/kit/feedback"
import { StatusGlyph } from "@/components/kit/status-dot"
import { ConfirmAlert, ConfirmAlertContent, Menu, MenuContent, MenuItem, MenuTrigger, Sheet, SheetContent, SheetDescription, SheetTitle, RowContextMenu, RowContextMenuContent, RowContextMenuItem, RowContextMenuTrigger } from "@/components/kit/overlays"
import { ToolbarTitle } from "@/features/shell/toolbar-slot"
import { useIsPhone } from "@/features/shell/use-is-phone"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { useEventsConnected } from "@/features/events/use-events"
import { AddPackageSheet } from "@/features/library/add-package-sheet"
import { agentsQuery, signOutConfirm } from "@/features/agents/queries"
import { SignIn } from "@/features/agents/sign-in"
import { limitsQuery } from "@/features/limits/queries"
import { replicationQuery } from "@/features/library/queries"
import { replicationStatus } from "@/features/settings/model"
import { settingsQuery } from "@/features/settings/queries"
import { needsLogin, remainingPercent, usable, windowsOf } from "@/features/limits/model"
import { NewSessionSheet } from "@/features/sessions/new-session-sheet"
import { projectsQuery, sessionsQuery } from "@/features/sessions/queries"
import { api, LOCAL, routeMissing } from "@/lib/api"
import type { LimitSnapshot, ProjectRow, SessionInfo } from "@/lib/api-types"
import { basename, harnessName, plural, relativeTime, shortAge } from "@/lib/format"
import { hud } from "@/lib/hud"
import { WorktreesSection } from "./worktrees-section"
import { compareAgentpackVersions, identityQuery, machineInfoQuery, machineUpdateQuery, topologyQuery, useAgentpackVersions, useDiscovering, useMachines, useReplicatedQueries, type Machine, type MachineAgent } from "./queries"
import { dismissUpdateJob, isUpdateJobActive, startUpdateJob, updateJobLabel, useUpdateJob } from "./update-jobs"

type Action = { kind: "new" | "install"; project?: ProjectRow }

function projectLabel(project: ProjectRow): string {
  return project.name || basename(project.directory)
}

export function formatBytes(bytes: number): string {
  const gb = bytes / 1024 ** 3
  return gb >= 10 ? `${Math.round(gb)} GB` : `${gb.toFixed(1)} GB`
}

// Live sessions in one project folder, needs-you first because that is what the row is for.
export function runningText(sessions: SessionInfo[]): string | undefined {
  const live = sessions.filter((session) => session.state.running && !session.archived_at && !session.ended_at)
  if (live.length === 0) return undefined
  const waiting = live.filter((session) => session.conversation_state === "needs_you").length
  if (waiting) return `${waiting} needs you`
  // Only a running turn is work; an idle chat or a terminal is just open.
  const working = live.filter((session) => session.conversation_state === "working").length
  return working ? `${working} working` : `${live.length} open`
}

export function MachineDetail({ id }: { id: string }) {
  const machines = useMachines()
  const discovering = useDiscovering()
  const identityPending = useQuery(identityQuery).isPending
  const machine = machines.find((item) => item.id === id)
  if (!machine) {
    if (discovering || identityPending) {
      return (
        <div className="p-(--s6)">
          <p className="mb-(--s3) text-(length:--fs-sm) text-(--fg2)">{`Looking for ${id}…`}</p>
          <RowSkeleton lines={2} />
        </div>
      )
    }
    return (
      <Empty title="Not on this mesh">
        <p>{`${id} isn't on this mesh.`}</p>
        <p className="mt-(--s3)">
          <Link to="/machines/topology" className="text-(--accent) hover:underline">Machines</Link>
        </p>
      </Empty>
    )
  }
  return <MachineView machine={machine} />
}

function MachineView({ machine }: { machine: Machine }) {
  const isPhone = useIsPhone()
  const live = useEventsConnected()
  const queryClient = useQueryClient()
  const info = useQuery({ ...machineInfoQuery(machine.id), enabled: machine.online })
  const topology = useQuery({ ...topologyQuery(live), enabled: machine.id !== LOCAL })
  const versionOf = useAgentpackVersions()
  const updateJob = useUpdateJob(machine.id)
  const updateStatus = useQuery({ ...machineUpdateQuery(machine.id), enabled: machine.online && !isUpdateJobActive(updateJob) })
  // Offline machines are disabled, so these return the last known rows from the cache.
  const projects = useQuery({ ...projectsQuery(machine.id), enabled: machine.online })
  const sessions = useReplicatedQueries(sessionsQuery).byMachine[machine.id]?.data ?? []
  const limits = useReplicatedQueries(limitsQuery).byMachine[machine.id]?.data ?? []
  const replication = useQuery({ ...replicationQuery, enabled: machine.id !== LOCAL })
  const settings = useQuery({ ...settingsQuery, enabled: machine.id === LOCAL })
  const [action, setAction] = useState<Action | null>(null)
  const [forgetting, setForgetting] = useState<ProjectRow | null>(null)
  const checkAgents = useMutation({
    mutationFn: () => Promise.all([
      queryClient.fetchQuery({ ...agentsQuery(machine.id, true), staleTime: 0, retry: false }),
      queryClient.fetchQuery({ ...machineInfoQuery(machine.id, true), staleTime: 0, retry: false }),
    ]),
    onSuccess: ([accounts, machineInfo]) => {
      queryClient.setQueryData(agentsQuery(machine.id).queryKey, accounts)
      queryClient.setQueryData(machineInfoQuery(machine.id).queryKey, machineInfo)
    },
    onError: (error) => hud.error(`Couldn't check agents on ${machine.name}: ${error.message}`),
  })

  const known = versionOf(machine.id)
  const latest = known.latest
  const version = info.data?.version ?? known.version
  const outdated = !!version && !!latest && compareAgentpackVersions(version, latest) < 0
  const needsUpdate = machine.needsUpdate || (machine.id !== LOCAL && [projects.error, info.error].some(routeMissing))
  const updating = isUpdateJobActive(updateJob) || updateStatus.data?.state === "running"

  const update = useMutation({
    mutationFn: () => api<{ started: boolean; version?: string }>("/api/node/update", { machine: machine.id, method: "POST" }),
    onSuccess: (result) => {
      const target = result.version ?? latest ?? "the latest version"
      hud.success(`Updating ${machine.name} to ${target}`)
      startUpdateJob(queryClient, {
        machineId: machine.id,
        machineName: machine.name,
        targetVersion: target,
      })
    },
    onError: (error) => hud.error(`Couldn't update ${machine.name}: ${error.message}`),
  })

  const forget = useMutation({
    mutationFn: (project: ProjectRow) => api<unknown>("/api/recent", { machine: machine.id, method: "DELETE", body: { directory: project.directory } }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: projectsQuery(machine.id).queryKey })
    },
    onError: (error, project) => hud.error(`Couldn't forget ${projectLabel(project)}: ${error.message}`),
  })

  const peer = topology.data?.peers.find((item) => item.id === machine.id)
  const running = sessions.filter((session) => session.state.running && !session.archived_at && !session.ended_at).length
  const status = needsUpdate ? "Needs update" : machine.online ? "Online" : "Offline"
  const route = machine.id === LOCAL ? "this machine" : peer?.connection && [peer.connection, peer.rtt_ms !== undefined && `${peer.rtt_ms} ms`].filter(Boolean).join(", ")
  const subtitle = [status, route, running ? `${plural(running, "session")} running` : undefined].filter(Boolean).join(" · ")
  const cached = projects.data !== undefined

  const updateButton = (outdated || needsUpdate || updating) && (
    <Button size="sm" disabled={!machine.online || updating || update.isPending} onClick={() => update.mutate()}>
      {updating || update.isPending ? "Updating…" : latest ? `Update to ${latest}` : "Update"}
    </Button>
  )

  return (
    <div className="h-full min-h-0 overflow-y-auto">
      <ToolbarTitle title={machine.name} subtitle={[status, info.data && `${info.data.os} ${info.data.arch}`, version && `agentpack ${version}`].filter(Boolean).join(" · ")} />
      <Inspector>
        <InspectorHead icon={<Server aria-hidden />} title={machine.name} subtitle={subtitle}>
          {updateButton}
          <Button variant="primary" size="sm" disabled={!machine.online} onClick={() => setAction({ kind: "new" })}>
            <Plus aria-hidden />
            New session here
          </Button>
        </InspectorHead>
        {!machine.online && (
          <Banner className="col-span-full">
            {cached
              ? peer?.last_seen
                ? `${machine.name} is offline. Projects as of ${relativeTime(new Date(peer.last_seen))}.`
                : `${machine.name} is offline. Showing last known projects.`
              : `${machine.name} is offline. No projects are cached.`}
          </Banner>
        )}
        {needsUpdate && !updating && (
          <Banner tone="warn" className="col-span-full">{`Update Agentpack on ${machine.name} to see its agents and system details.`}</Banner>
        )}
        {updateJob && isUpdateJobActive(updateJob) && (
          <Banner className="col-span-full">
            {`Updating ${machine.name} to ${updateJob.targetVersion} · ${updateJobLabel(updateJob)}`}
          </Banner>
        )}
        {updateJob?.phase === "verified" && (
          <Banner
            className="col-span-full"
            action={<Button type="button" size="sm" variant="ghost" onClick={() => dismissUpdateJob(machine.id)}>Dismiss</Button>}
          >
            {`Verified ${machine.name} is running ${updateJob.targetVersion}.`}
          </Banner>
        )}
        {updateJob?.phase === "timed_out" && (
          <Banner
            tone="warn"
            className="col-span-full"
            action={(
              <span className="flex items-center gap-(--s2)">
                <Button type="button" size="sm" disabled={!machine.online || update.isPending} onClick={() => update.mutate()}>Retry</Button>
                <Link to="/settings/$pane" params={{ pane: "diagnostics" }} className="text-(length:--fs-sm) text-(--accent) hover:underline">View logs</Link>
              </span>
            )}
          >
            {`${machine.name} hasn't reported a new version · Timed out.`}
          </Banner>
        )}
        {(updateJob?.phase === "failed" || (!updateJob && updateStatus.data?.state === "failed")) && (
          <Banner
            tone="danger"
            className="col-span-full"
            action={<Button type="button" size="sm" disabled={!machine.online || update.isPending} onClick={() => update.mutate()}>Retry</Button>}
          >
            {`Couldn't update ${machine.name}: ${updateJob?.error || updateStatus.data?.error || "Update failed"}`}
          </Banner>
        )}
        {info.error && !needsUpdate && (
          <Banner tone="danger" className="col-span-full">{`Couldn't read details from ${machine.name}: ${info.error.message}`}</Banner>
        )}
        {projects.error && !needsUpdate && (
          <Banner tone="danger" className="col-span-full">{`Couldn't load projects from ${machine.name}: ${projects.error.message}`}</Banner>
        )}

        <Section title="Agents" action={info.data?.os === "darwin" && machine.online && (
          <Button type="button" size="sm" disabled={checkAgents.isPending} onClick={() => checkAgents.mutate()}>
            {checkAgents.isPending ? "Checking…" : "Check agents"}
          </Button>
        )}>
          {machine.online && info.isPending && <RowSkeleton lines={2} />}
          {info.data && <AgentsGrid machine={machine} agents={info.data.agents} limits={limits} />}
          {!machine.online && !info.data && <p className="text-(--fg2)">Agents are shown while {machine.name} is online.</p>}
        </Section>

        <Section title="Machine">
          <dl className="grid gap-(--s2)">
            {info.data && (
              <KeyValue label="System">
                {[`${info.data.os} ${info.data.arch}`, plural(info.data.cores, "core"), info.data.memory && formatBytes(info.data.memory)].filter(Boolean).join(" · ")}
              </KeyValue>
            )}
            {info.data?.disk_total ? (
              <KeyValue label="Disk">
                <span className="flex items-center gap-(--s3)">
                  <span className="w-16 shrink-0"><Meter value={info.data.disk_free ?? 0} max={info.data.disk_total} label="Free disk space" /></span>
                  {`${formatBytes(info.data.disk_free ?? 0)} free of ${formatBytes(info.data.disk_total)}`}
                </span>
              </KeyValue>
            ) : null}
            <KeyValue label="agentpack">
              {version ?? "—"}
              {outdated && <span className="text-(--fg2)">{` · ${latest} available`}</span>}
            </KeyValue>
            {machine.id !== LOCAL && replication.data && (
              <KeyValue label="Replication">
                {replicationStatus({ id: machine.id, name: machine.name, online: machine.online }, replication.data.peers?.find((replica) => replica.node === machine.id))}
                {replication.data.contacts?.[machine.id]?.last_sync && (
                  <span className="text-(--fg2)">{` · synced ${relativeTime(new Date(replication.data.contacts[machine.id]!.last_sync!))}`}</span>
                )}
              </KeyValue>
            )}
            {settings.data && (
              <KeyValue label="Sharing">
                {settings.data.sharing_enabled ? settings.data.allowed_peers?.join(", ") || "On" : "Off"}
              </KeyValue>
            )}
            {peer?.last_seen && !machine.online && <KeyValue label="Last seen">{relativeTime(new Date(peer.last_seen))}</KeyValue>}
          </dl>
        </Section>

        <Section title={projects.data ? `Projects · ${projects.data.length}` : "Projects"} className="col-span-full">
          {machine.online && projects.isPending && <RowSkeleton lines={2} />}
          {projects.data?.length === 0 && <Empty title="No projects yet">Folders appear here after an agent runs in them.</Empty>}
          {!!projects.data?.length && isPhone && (
            <ul aria-label="Projects" className="flex flex-col divide-y-[0.5px] divide-(--sep)">
              {projects.data.map((project) => {
                const usableProject = machine.online && project.available
                const note = project.error || (project.available ? "" : project.unavailable_reason || "Unavailable")
                const here = sessions.filter((session) => (session.definition_root || session.directory) === project.directory)
                const runningHere = runningText(here)
                return (
                  <li key={project.directory} className="flex min-w-0 items-start justify-between gap-(--s3) py-(--s3)">
                    <div className="flex min-w-0 flex-1 flex-col gap-(--s1)">
                      <span className="truncate font-medium">{projectLabel(project)}</span>
                      <span className="truncate text-(length:--fs-xs) text-(--fg2)" title={project.directory}>{note || project.directory}</span>
                      <div className="flex flex-wrap items-center gap-x-(--s3) gap-y-(--s1) text-(length:--fs-xs) text-(--fg2)">
                        {runningHere && (
                          <span className="inline-flex items-center gap-(--s1)">
                            <StatusGlyph state={runningHere.endsWith("needs you") ? "needsYou" : runningHere.endsWith("working") ? "running" : "idle"} />
                            {runningHere}
                          </span>
                        )}
                        <span>{plural(project.packages.length, "package")}</span>
                        {project.mcp.length > 0 && <span>{`${project.mcp.length} MCP`}</span>}
                        {project.last_used && <span>{shortAge(new Date(project.last_used))}</span>}
                      </div>
                    </div>
                    <Menu>
                      <MenuTrigger
                        render={(
                          <Button type="button" size="sm" variant="ghost" aria-label={`Actions for ${projectLabel(project)}`}>
                            Actions
                          </Button>
                        )}
                      />
                      <MenuContent align="end">
                        <MenuItem disabled={!usableProject} onClick={() => setAction({ kind: "new", project })}>
                          New session…
                        </MenuItem>
                        <MenuItem disabled={!usableProject} onClick={() => setAction({ kind: "install", project })}>
                          Install…
                        </MenuItem>
                        <MenuItem danger disabled={!machine.online} onClick={() => setForgetting(project)}>
                          Forget
                        </MenuItem>
                      </MenuContent>
                    </Menu>
                  </li>
                )
              })}
            </ul>
          )}
          {!!projects.data?.length && !isPhone && (
            <DataGrid aria-label="Projects">
              <DataGridHead>
                <DataGridRow>
                  <DataGridHeaderCell className="w-48">Project</DataGridHeaderCell>
                  <DataGridHeaderCell>Folder</DataGridHeaderCell>
                  <DataGridHeaderCell className="w-32">Running</DataGridHeaderCell>
                  <DataGridHeaderCell className="w-24 text-right">Packages</DataGridHeaderCell>
                  <DataGridHeaderCell className="w-16 text-right">MCP</DataGridHeaderCell>
                  <DataGridHeaderCell className="w-24 text-right">Last used</DataGridHeaderCell>
                  <th className="w-10 border-b-[0.5px] border-(--sep)"><span className="sr-only">Actions</span></th>
                </DataGridRow>
              </DataGridHead>
              <tbody>
                {projects.data.map((project) => {
                  const usableProject = machine.online && project.available
                  const note = project.error || (project.available ? "" : project.unavailable_reason || "Unavailable")
                  const here = sessions.filter((session) => (session.definition_root || session.directory) === project.directory)
                  const runningHere = runningText(here)
                  return (
                    <RowContextMenu key={project.directory}>
                      <RowContextMenuTrigger render={<DataGridRow />}>
                        <DataGridCell>
                          <Tooltip>
                            <TooltipTrigger render={<span className="block truncate" />}>{projectLabel(project)}</TooltipTrigger>
                            <TooltipContent>
                              {project.launches.map((launch) => (
                                <span key={`${launch.harness}/${launch.mode}`} className="block">
                                  {`${harnessName(launch.harness)} · ${launch.mode || "default"} · ${relativeTime(new Date(launch.last_used))}`}
                                </span>
                              ))}
                              {project.launches.length === 0 && project.directory}
                            </TooltipContent>
                          </Tooltip>
                        </DataGridCell>
                        <DataGridCell className="text-(--fg2)" title={project.directory}>{note || project.directory}</DataGridCell>
                        <DataGridCell>
                          {runningHere ? (
                            <span className="flex items-center gap-(--s2)">
                              <StatusGlyph state={runningHere.endsWith("needs you") ? "needsYou" : runningHere.endsWith("working") ? "running" : "idle"} />
                              {runningHere}
                            </span>
                          ) : <span className="text-(--fg3)">—</span>}
                        </DataGridCell>
                        <DataGridCell className="text-right tabular-nums">{project.packages.length}</DataGridCell>
                        <DataGridCell className="text-right tabular-nums">{project.mcp.length}</DataGridCell>
                        <DataGridCell className="text-right tabular-nums text-(--fg2)">{project.last_used ? shortAge(new Date(project.last_used)) : "—"}</DataGridCell>
                        <DataGridCell className="px-0">
                          <Button variant="ghost" size="icon" aria-label={`New session in ${projectLabel(project)}`} disabled={!usableProject} onClick={() => setAction({ kind: "new", project })}>
                            <Plus aria-hidden />
                          </Button>
                        </DataGridCell>
                      </RowContextMenuTrigger>
                      <RowContextMenuContent>
                        <RowContextMenuItem icon={<SquareTerminal aria-hidden />} disabled={!usableProject} onClick={() => setAction({ kind: "new", project })}>
                          New session…
                        </RowContextMenuItem>
                        <RowContextMenuItem icon={<Package aria-hidden />} disabled={!usableProject} onClick={() => setAction({ kind: "install", project })}>
                          Install…
                        </RowContextMenuItem>
                        <RowContextMenuItem icon={<Trash2 aria-hidden />} disabled={!machine.online} onClick={() => setForgetting(project)}>
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
        <WorktreesSection machine={machine.id} online={machine.online} />
      </Inspector>

      <NewSessionSheet
        open={action?.kind === "new"}
        initialMachine={machine.id}
        initialDirectory={action?.project?.directory}
        onAccepted={() => setAction(null)}
        onOpenChange={(open) => { if (!open) setAction(null) }}
      />
      <AddPackageSheet
        open={action?.kind === "install"}
        initialMachine={machine.id}
        initialDirectory={action?.project?.directory}
        onOpenChange={(open) => { if (!open) setAction(null) }}
      />
      <ConfirmAlert open={forgetting !== null} onOpenChange={(open) => { if (!open) setForgetting(null) }}>
        {forgetting && (
          <ConfirmAlertContent
            title={`Forget ${projectLabel(forgetting)}?`}
            description={`Removes this folder from recent folders on ${machine.name}. Files in the folder are not changed.`}
            destructiveLabel="Forget"
            onConfirm={() => {
              const project = forgetting
              setForgetting(null)
              forget.mutate(project)
            }}
          />
        )}
      </ConfirmAlert>
    </div>
  )
}

function AgentsGrid({ machine, agents, limits }: { machine: Machine; agents: MachineAgent[]; limits: LimitSnapshot[] }) {
  // A peer without /api/agents falls back to what its limit probes imply.
  const queryClient = useQueryClient()
  const accounts = useQuery({ ...agentsQuery(machine.id), enabled: machine.online }).data
  const [signingIn, setSigningIn] = useState<string>()
  const [signingOut, setSigningOut] = useState<string>()
  const signOut = useMutation({
    mutationFn: (harness: string) => api(`/api/agents/${encodeURIComponent(harness)}/logout`, { machine: machine.id, method: "POST" }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: agentsQuery(machine.id).queryKey })
    },
    onError: (error, harness) => hud.error(`Couldn't sign ${harnessName(harness)} out on ${machine.name}: ${routeMissing(error) ? "needs an update" : error.message}`, routeMissing(error) ? undefined : () => signOut.mutate(harness)),
  })
  const signingInAccount = accounts?.find((account) => account.harness === signingIn)
  return (
    <>
      {/* Phones drop Limits (Usage › Limits has them) so the agent name keeps a fixed column. */}
      <DataGrid aria-label="Agents" className="min-w-100 max-[760px]:min-w-0">
        <DataGridHead>
          <DataGridRow>
            <DataGridHeaderCell className="w-28">Agent</DataGridHeaderCell>
            <DataGridHeaderCell className="w-28">Version</DataGridHeaderCell>
            <DataGridHeaderCell>Account</DataGridHeaderCell>
            <DataGridHeaderCell className="w-36 max-[760px]:hidden">Limits</DataGridHeaderCell>
          </DataGridRow>
        </DataGridHead>
        <tbody>
          {agents.map((agent) => {
            const snapshot = limits.find((item) => item.harness === agent.harness)
            const account = accounts?.find((item) => item.harness === agent.harness)
            const left = snapshot && usable(snapshot)
              ? Math.min(...windowsOf(snapshot).map((window) => remainingPercent(window) ?? 100))
              : undefined
            const signedIn = !agent.installed || !snapshot ? undefined : needsLogin(snapshot) ? "Needs login" : usable(snapshot) ? "Yes" : undefined
            return (
              <DataGridRow key={agent.harness}>
                <DataGridCell className="font-medium">{harnessName(agent.harness)}</DataGridCell>
                <DataGridCell className={agent.installed ? "tabular-nums" : "text-(--fg3)"}>{agent.installed ? agent.version || "—" : "Not installed"}</DataGridCell>
                <DataGridCell>
                  {account?.installed ? (
                    account.signedIn ? (
                      <span className="flex min-w-0 items-center gap-(--s2)">
                        <span className="truncate" title={account.label}>{account.label || "Signed in"}</span>
                        {account.signOut && (
                          <Button type="button" size="sm" variant="ghost" className="shrink-0" disabled={signOut.isPending && signOut.variables === agent.harness} onClick={() => setSigningOut(agent.harness)}>Sign out…</Button>
                        )}
                      </span>
                    ) : (
                      <span className="flex items-center gap-(--s2)">
                        {!account.checkRequired && <StatusGlyph state="needsYou" />}
                        <span className="text-(--fg2)">{account.checkRequired ? "Unchecked" : "Not signed in"}</span>
                        {account.methods.length > 0 && (
                          <Button type="button" size="sm" variant="ghost" onClick={() => setSigningIn(agent.harness)}>Sign in…</Button>
                        )}
                      </span>
                    )
                  ) : signedIn === "Needs login" ? (
                    <span className="flex items-center gap-(--s2)"><StatusGlyph state="needsYou" />Needs login</span>
                  ) : signedIn ?? (agent.installed && <span className="text-(--fg3)">—</span>)}
                </DataGridCell>
                <DataGridCell className="max-[760px]:hidden">
                  {left !== undefined ? (
                    <span className="flex items-center gap-(--s2)">
                      <span className="w-16 shrink-0"><Meter value={left} label={`${harnessName(agent.harness)} limit left`} /></span>
                      {`${Math.round(left)}% left`}
                    </span>
                  ) : agent.installed ? <span className="text-(--fg3)">—</span> : null}
                </DataGridCell>
              </DataGridRow>
            )
          })}
        </tbody>
      </DataGrid>
      <ConfirmAlert open={signingOut !== undefined} onOpenChange={(open) => { if (!open) setSigningOut(undefined) }}>
        {signingOut && (
          <ConfirmAlertContent
            {...signOutConfirm(harnessName(signingOut), machine.name)}
            destructiveLabel="Sign out"
            onConfirm={() => {
              const harness = signingOut
              setSigningOut(undefined)
              signOut.mutate(harness)
            }}
          />
        )}
      </ConfirmAlert>
      <Sheet open={signingIn !== undefined} onOpenChange={(open) => !open && setSigningIn(undefined)}>
        {signingIn && (
          <SheetContent>
            <SheetTitle>Sign in to {harnessName(signingIn)}</SheetTitle>
            <SheetDescription>{`Starts on ${machine.name}. ${harnessName(signingIn)} sign-ins are shared with every enrolled machine. Sign-in links expire if not completed in time.`}</SheetDescription>
            <SignIn machine={machine.id} machineName={machine.name} harness={signingIn} methods={signingInAccount?.methods ?? []} />
          </SheetContent>
        )}
      </Sheet>
    </>
  )
}
