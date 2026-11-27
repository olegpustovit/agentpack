import { useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import { Button } from "@/components/ui/button"
import { Server } from "lucide-react"
import { DataGrid, DataGridCell, DataGridHead, DataGridHeaderCell, DataGridRow, Inspector, InspectorHead, KeyValue, Meter, Section } from "@/components/kit/data"
import { Banner, Empty, RowSkeleton } from "@/components/kit/feedback"
import { StatusGlyph } from "@/components/kit/status-dot"
import { ConfirmAlert, ConfirmAlertContent, Sheet, SheetContent, SheetDescription, SheetTitle } from "@/components/kit/overlays"
import { ToolbarTitle } from "@/features/shell/toolbar-slot"
import { useEventsConnected } from "@/features/events/use-events"
import { agentsQuery, signOutConfirm } from "@/features/agents/queries"
import { SignIn } from "@/features/agents/sign-in"
import { sessionsQuery } from "@/features/sessions/queries"
import { api, LOCAL, routeMissing } from "@/lib/api"
import { harnessName, plural, relativeTime } from "@/lib/format"
import { hud } from "@/lib/hud"
import { WorktreesSection } from "./worktrees-section"
import { compareAgentpackVersions, identityQuery, machineInfoQuery, machineUpdateQuery, topologyQuery, useAgentpackVersions, useDiscovering, useMachines, useReplicatedQueries, type Machine, type MachineAgent } from "./queries"
import { dismissUpdateJob, isUpdateJobActive, startUpdateJob, updateJobLabel, useUpdateJob } from "./update-jobs"

export function formatBytes(bytes: number): string {
  const gb = bytes / 1024 ** 3
  return gb >= 10 ? `${Math.round(gb)} GB` : `${gb.toFixed(1)} GB`
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
  const live = useEventsConnected()
  const queryClient = useQueryClient()
  const info = useQuery({ ...machineInfoQuery(machine.id), enabled: machine.online })
  const topology = useQuery({ ...topologyQuery(live), enabled: machine.id !== LOCAL })
  const versionOf = useAgentpackVersions()
  const updateJob = useUpdateJob(machine.id)
  const updateStatus = useQuery({ ...machineUpdateQuery(machine.id), enabled: machine.online && !isUpdateJobActive(updateJob) })
  // Offline machines are disabled, so these return the last known rows from the cache.
  const sessions = useReplicatedQueries(sessionsQuery).byMachine[machine.id]?.data ?? []
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
  const needsUpdate = machine.needsUpdate || (machine.id !== LOCAL && routeMissing(info.error))
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

  const peer = topology.data?.peers.find((item) => item.id === machine.id)
  const running = sessions.filter((session) => session.state.running && !session.archived_at && !session.ended_at).length
  const status = needsUpdate ? "Needs update" : machine.online ? "Online" : "Offline"
  const route = machine.id === LOCAL ? "this machine" : peer?.connection && [peer.connection, peer.rtt_ms !== undefined && `${peer.rtt_ms} ms`].filter(Boolean).join(", ")
  const subtitle = [status, route, running ? `${plural(running, "session")} running` : undefined].filter(Boolean).join(" · ")

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
        </InspectorHead>
        {!machine.online && (
          <Banner className="col-span-full">
            {peer?.last_seen ? `${machine.name} is offline. Last seen ${relativeTime(new Date(peer.last_seen))}.` : `${machine.name} is offline.`}
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

        <Section title="Agents" action={info.data?.os === "darwin" && machine.online && (
          <Button type="button" size="sm" disabled={checkAgents.isPending} onClick={() => checkAgents.mutate()}>
            {checkAgents.isPending ? "Checking…" : "Check agents"}
          </Button>
        )}>
          {machine.online && info.isPending && <RowSkeleton lines={2} />}
          {info.data && <AgentsGrid machine={machine} agents={info.data.agents} />}
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
          </dl>
        </Section>

        <WorktreesSection machine={machine.id} online={machine.online} />
      </Inspector>

    </div>
  )
}

function AgentsGrid({ machine, agents }: { machine: Machine; agents: MachineAgent[] }) {
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
      <DataGrid aria-label="Agents" className="min-w-100 max-[760px]:min-w-0">
        <DataGridHead>
          <DataGridRow>
            <DataGridHeaderCell className="w-28">Agent</DataGridHeaderCell>
            <DataGridHeaderCell className="w-28">Version</DataGridHeaderCell>
            <DataGridHeaderCell>Account</DataGridHeaderCell>
          </DataGridRow>
        </DataGridHead>
        <tbody>
          {agents.map((agent) => {
            const account = accounts?.find((item) => item.harness === agent.harness)
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
                  ) : agent.installed && <span className="text-(--fg3)">—</span>}
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
