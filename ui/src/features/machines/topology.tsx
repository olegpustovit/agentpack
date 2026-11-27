import { useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Link, useNavigate } from "@tanstack/react-router"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { hud } from "@/lib/hud"
import { useIsPhone } from "@/features/shell/use-is-phone"
import { DataGrid, DataGridCell, DataGridHead, DataGridHeaderCell, DataGridRow, Inspector, Section } from "@/components/kit/data"
import { Banner, RowSkeleton } from "@/components/kit/feedback"
import { ConfirmAlert, ConfirmAlertContent, Menu, MenuContent, MenuItem, MenuTrigger, RowMoreButton } from "@/components/kit/overlays"
import { StatusGlyph } from "@/components/kit/status-dot"
import { useEventsConnected } from "@/features/events/use-events"
import { replicationQuery } from "@/features/library/queries"
import { useGatewayActions } from "@/features/settings/actions"
import { settingsQuery } from "@/features/settings/queries"
import { ToolbarSlot, ToolbarTitle } from "@/features/shell/toolbar-slot"
import { api, errorText, LOCAL } from "@/lib/api"
import type { TailnetNode } from "@/lib/api-types"
import { useNow } from "@/lib/use-now"
import { machinesToUpdate, nodesQuery, topologyQuery, useAgentpackVersions, useMachines } from "./queries"
import { getUpdateJob, isUpdateJobActive, startUpdateJob, updateJobLabel, useUpdateJob } from "./update-jobs"
import { DIAGRAM_W, GATEWAY_W, NODE_H, PEER_W, PEER_X, layoutDiagram, machinePeers, meshState, type MeshState } from "./topology-model"

const dotFill: Record<string, string> = { reachable: "var(--ok)", offline: "none" }

function Node({ x, y, width, name, detail, state, dashed, onOpen }: { x: number; y: number; width: number; name: string; detail: string; state?: string; dashed?: boolean; onOpen: () => void }) {
  return (
    <g onClick={onOpen} className="cursor-pointer [&:hover>rect]:stroke-(--fg3)">
      <rect x={x} y={y} width={width} height={NODE_H} rx={7} fill={state ? "var(--window)" : "var(--chrome)"} stroke="var(--sep)" strokeDasharray={dashed ? "4 3" : undefined} />
      {state && <circle cx={x + 14} cy={y + NODE_H / 2} r={3.5} fill={dotFill[state] ?? "var(--warn)"} stroke={state === "offline" ? "var(--fg3)" : "none"} strokeDasharray={state === "offline" ? "2 1.5" : undefined} />}
      <text x={x + (state ? 26 : 12)} y={y + 17} fill="var(--fg)" fontSize={13} fontWeight={state ? 500 : 600}>{name}</text>
      <text x={x + (state ? 26 : 12)} y={y + 32} fill="var(--fg2)" fontSize={11}>{detail}</text>
    </g>
  )
}

// The mesh on one page: the graph, then every machine with its version and sync, where it is enrolled and removed.
export function TopologyView() {
  const isPhone = useIsPhone()
  const live = useEventsConnected()
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const topology = useQuery(topologyQuery(live))
  const machines = useMachines()
  const discovered = useQuery(nodesQuery(live, true)).data ?? []
  const replication = useQuery({ ...replicationQuery, refetchInterval: live ? false : 30_000 }).data
  const settings = useQuery(settingsQuery).data
  const versionOf = useAgentpackVersions()
  const now = useNow(true, 15_000)
  const [removed, setRemoved] = useState("")
  const data = topology.data
  const targets = machinesToUpdate(machines)
  const updateAll = useMutation({
    mutationFn: async () => {
      const update = async (machine: (typeof machines)[number]) => {
        if (isUpdateJobActive(getUpdateJob(machine.id))) return
        try {
          const result = await api<{ version: string }>("/api/node/update", { machine: machine.id, method: "POST" })
          startUpdateJob(queryClient, { machineId: machine.id, machineName: machine.name, targetVersion: result.version })
        } catch (error) {
          hud.error(`Couldn't update ${machine.name}: ${errorText(error)}`)
        }
      }
      await Promise.all(targets.filter((machine) => machine.id !== LOCAL).map(update))
      const gateway = targets.find((machine) => machine.id === LOCAL)
      if (gateway) await update(gateway)
    },
  })

  if (!data) {
    return (
      <div className="p-(--s6)">
        {topology.error ? <Banner tone="danger">{`Couldn't load topology: ${topology.error.message}`}</Banner> : <RowSkeleton lines={2} />}
      </div>
    )
  }

  const peers = machinePeers(data.peers, machines.filter((machine) => machine.id !== LOCAL))
  const layout = layoutDiagram(peers)
  const reachable = peers.filter((peer) => peer.state === "reachable").length
  const enrolled = new Map((replication?.peers ?? []).map((peer) => [peer.node, peer] as const))
  const sharing = settings?.sharing_enabled !== false
  const canManage = settings?.can_manage !== false
  const open = (machine: string) => void navigate({ to: "/machines/$machine", params: { machine } })
  const schema = machines.find((machine) => machine.id === LOCAL)?.schema
  // A different schema is why two machines stop syncing; the release check reads it here.
  const schemaOf = (node: TailnetNode) => {
    const theirs = replication?.contacts?.[node.id]?.schema_version ?? node.schema_version
    return theirs && schema && theirs !== schema ? `schema ${theirs}` : undefined
  }
  // Machines on the mesh, then tailnet gateways that could join it.
  const others: TailnetNode[] = [
    ...machines.filter((machine) => machine.id !== LOCAL),
    ...discovered.filter((node) => !machines.some((machine) => machine.id === node.id)),
  ]
  const rows = [
    { node: { id: LOCAL, name: data.self.name, online: true, main: data.self.main } as TailnetNode, mesh: { text: "This machine", glyph: "running" } as MeshState },
    ...others.map((node) => ({
      node,
      mesh: meshState(node, enrolled.get(node.id), replication?.contacts?.[node.id], peers.find((peer) => peer.id === node.id)?.last_seen, sharing, now),
    })),
  ]

  return (
    <div className="h-full min-h-0 overflow-y-auto">
      <Inspector className="min-[1260px]:grid-cols-1">
        <ToolbarTitle title="Topology" subtitle={`${reachable} reachable · ${peers.length - reachable} offline`} />
        <ToolbarSlot>
          {(targets.length > 0 || updateAll.isPending) && (
            <Button size="sm" disabled={updateAll.isPending} onClick={() => updateAll.mutate()}>
              {updateAll.isPending ? "Updating…" : `Update all (${targets.length})`}
            </Button>
          )}
        </ToolbarSlot>
        {topology.error && <Banner tone="danger">{`Couldn't refresh topology: ${topology.error.message}`}</Banner>}
        {!sharing && <Banner tone="warn" action={<Link className="text-(--accent)" to="/settings/$pane" params={{ pane: "sharing" }}>Open Sharing</Link>}>Turn on Tailscale sharing to add machines.</Banner>}
        {removed && (
          <Banner tone="warn" action={<Link className="text-(--accent)" to="/settings/$pane" params={{ pane: "connections" }}>Open Vault</Link>}>
            {`${removed} keeps the MCP logins it already received. Sign out of OAuth logins in Vault and rotate other credentials at their providers.`}
          </Banner>
        )}
        {/* The list below says the same, so assistive tech skips the diagram; phones show only the list. */}
        <div className="max-[760px]:hidden">
          <svg data-slot="topology-diagram" viewBox={`0 0 ${DIAGRAM_W} ${layout.height}`} width={DIAGRAM_W} height={layout.height} className="block h-auto max-w-full" aria-hidden="true" focusable="false">
            <g fill="none" strokeWidth={1.2}>
              {layout.edges.map((edge) => (
                <path key={edge.peer.id} d={edge.d} stroke={edge.dashed ? "var(--fg3)" : "var(--accent)"} strokeDasharray={edge.dashed ? "5 4" : undefined} />
              ))}
            </g>
            <g fill="var(--fg2)" fontSize={11} textAnchor="end" dominantBaseline="middle" stroke="var(--window)" strokeWidth={5} strokeLinejoin="round" paintOrder="stroke">
              {layout.edges.map((edge) => (
                <text key={edge.peer.id} x={edge.labelX} y={edge.labelY}>{[edge.label, edge.peer.rtt_ms && `${edge.peer.rtt_ms} ms`].filter(Boolean).join(" · ")}</text>
              ))}
            </g>
            <Node x={0} y={layout.hubY - NODE_H / 2} width={GATEWAY_W} name={data.self.name} detail={[data.self.os, versionOf(LOCAL).version].filter(Boolean).join(" · ")} onOpen={() => open(LOCAL)} />
            {layout.nodes.map(({ peer, y }) => (
              <Node key={peer.id} x={PEER_X} y={y} width={PEER_W} name={peer.name} detail={[peer.os, versionOf(peer.id).version ?? peer.version].filter(Boolean).join(" · ")} state={peer.state} dashed={peer.state === "offline"} onOpen={() => open(peer.id)} />
            ))}
          </svg>
        </div>
        <Section title="Machines" className="max-[760px]:border-t-0 max-[760px]:pt-0">
          <DataGrid aria-label="Machines" className="max-[760px]:min-w-0">
            {!isPhone && (
              <DataGridHead>
                <DataGridRow>
                  <DataGridHeaderCell className="w-56">Machine</DataGridHeaderCell>
                  <DataGridHeaderCell className="w-56">Version</DataGridHeaderCell>
                  <DataGridHeaderCell>Sync</DataGridHeaderCell>
                  <th className="w-24 border-b-[0.5px] border-(--sep)"><span className="sr-only">Actions</span></th>
                </DataGridRow>
              </DataGridHead>
            )}
            <tbody>
              {rows.map((row) => (
                <MachineLine key={row.node.id} node={row.node} schema={schemaOf(row.node)} mesh={row.mesh} enrolled={enrolled.get(row.node.id)?.mesh === true} version={versionOf(row.node.id)} sharing={sharing} canManage={canManage} phone={isPhone} onRemoved={() => setRemoved(row.node.name)} />
              ))}
            </tbody>
          </DataGrid>
        </Section>
      </Inspector>
    </div>
  )
}

function MachineLine({ node, schema, mesh, enrolled, version, sharing, canManage, phone, onRemoved }: {
  node: TailnetNode
  schema?: string
  mesh: MeshState
  enrolled: boolean
  version: { version?: string; latest?: string; outdated: boolean }
  sharing: boolean
  canManage: boolean
  phone: boolean
  onRemoved: () => void
}) {
  const queryClient = useQueryClient()
  const { enroll, sync, remove } = useGatewayActions()
  const job = useUpdateJob(node.id)
  const [confirm, setConfirm] = useState<"enroll" | "remove" | null>(null)
  const update = useMutation({
    mutationFn: () => api<{ version?: string }>("/api/node/update", { machine: node.id, method: "POST" }),
    onSuccess: (result) => startUpdateJob(queryClient, { machineId: node.id, machineName: node.name, targetVersion: result.version ?? version.latest ?? "the latest version" }),
    onError: (error) => hud.error(`Couldn't update ${node.name}: ${errorText(error)}`, () => update.mutate()),
  })
  const local = node.id === LOCAL
  const busy = enroll.isPending || sync.isPending || remove.isPending
  const blocked = !canManage || !sharing || !node.online
  const name = (
    <span className="inline-flex min-w-0 items-center gap-(--s2)">
      <StatusGlyph state={mesh.glyph} />
      {!mesh.member && !local ? <span className="truncate">{node.name}</span> : <Link to="/machines/$machine" params={{ machine: node.id }} className="truncate font-medium hover:underline">{node.name}</Link>}
      {node.main && <Badge variant="secondary">Main</Badge>}
    </span>
  )
  const versionText = job && isUpdateJobActive(job) ? `Updating · ${updateJobLabel(job)}` : [version.version ?? node.version ?? "—", schema].filter(Boolean).join(" · ")
  const versionCell = (
    <span className="inline-flex flex-wrap items-center gap-(--s3)">
      {versionText}
      {version.outdated && !isUpdateJobActive(job) && <Button size="sm" disabled={!node.online || update.isPending} onClick={() => update.mutate()}>Update</Button>}
    </span>
  )
  const actions = local ? null : enrolled ? (
    <Menu>
      <MenuTrigger render={<RowMoreButton aria-label={`${node.name} actions`} className="opacity-100" />} />
      <MenuContent align="end">
        <MenuItem disabled={blocked || busy} onClick={() => sync.mutate(node)}>Sync now</MenuItem>
        <MenuItem danger disabled={!canManage || busy} onClick={() => setConfirm("remove")}>Remove from mesh…</MenuItem>
      </MenuContent>
    </Menu>
  ) : (
    <Button size="sm" disabled={blocked || busy} onClick={() => setConfirm("enroll")}>{mesh.member ? "Approve" : "Add"}</Button>
  )
  const alerts = (
    <ConfirmAlert open={confirm !== null} onOpenChange={(next) => !next && setConfirm(null)}>
      {confirm === "enroll" && <ConfirmAlertContent
        title={`Add ${node.name} to this mesh?`}
        description={`${node.name} gets shared accounts and password hashes, chat history, limits, Library data and MCP logins. Add this machine on ${node.name} too to start syncing.`}
        destructiveLabel="Add"
        destructive={false}
        onConfirm={() => { setConfirm(null); enroll.mutate(node) }}
      />}
      {confirm === "remove" && <ConfirmAlertContent
        title={`Remove ${node.name} from the mesh?`}
        description={`Stops syncing with ${node.name}. Both machines keep what they already have, including MCP logins: sign out of OAuth logins in Vault and rotate other credentials at their providers.`}
        destructiveLabel="Remove"
        onConfirm={() => { setConfirm(null); remove.mutate(node, { onSuccess: onRemoved }) }}
      />}
    </ConfirmAlert>
  )
  if (phone) {
    return (
      <tr className="border-b-[0.5px] border-(--sep-soft)">
        <td className="py-(--s3)">
          <div className="flex min-w-0 items-center justify-between gap-(--s3)">{name}{actions}</div>
          <p className="text-(length:--fs-sm) text-(--fg2)">{versionCell} · {mesh.text}</p>
          {alerts}
        </td>
      </tr>
    )
  }
  return (
    <DataGridRow>
      <DataGridCell>{name}</DataGridCell>
      <DataGridCell>{versionCell}</DataGridCell>
      <DataGridCell className={mesh.warn ? "text-(--warn-word)" : "text-(--fg2)"} title={mesh.detail}>{mesh.text}</DataGridCell>
      <DataGridCell className="text-right">{actions}{alerts}</DataGridCell>
    </DataGridRow>
  )
}
