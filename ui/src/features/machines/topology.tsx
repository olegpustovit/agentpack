import { useQuery } from "@tanstack/react-query"
import { DataGrid, DataGridCell, DataGridHead, DataGridHeaderCell, DataGridRow, Inspector, Section } from "@/components/kit/data"
import { Banner, RowSkeleton } from "@/components/kit/feedback"
import { useEventsConnected } from "@/features/events/use-events"
import { replicationQuery } from "@/features/library/queries"
import { ToolbarTitle } from "@/features/shell/toolbar-slot"
import { sessionsQuery } from "@/features/sessions/queries"
import { LOCAL } from "@/lib/api"
import { plural } from "@/lib/format"
import { topologyQuery, useMachineQueries } from "./queries"
import {
  DIAGRAM_W,
  GATEWAY_W,
  NODE_H,
  PEER_W,
  PEER_X,
  edgeOf,
  layoutDiagram,
  partitionPeers,
  reasonOf,
  sessionCount,
  stateWord,
} from "./topology-model"

const dotFill: Record<string, string> = { reachable: "var(--ok)", offline: "none" }

function Node({ x, y, width, name, parts, sessions, state, dashed }: { x: number; y: number; width: number; name: string; parts: string[]; sessions?: number; state?: string; dashed?: boolean }) {
  const detail = [...parts, sessions === undefined ? undefined : plural(sessions, "session")].filter(Boolean).join(" · ")
  return (
    <g>
      <rect x={x} y={y} width={width} height={NODE_H} rx={7} fill={state ? "var(--window)" : "var(--chrome)"} stroke="var(--sep)" strokeDasharray={dashed ? "4 3" : undefined} />
      {state && <circle cx={x + 14} cy={y + NODE_H / 2} r={3.5} fill={dotFill[state] ?? "var(--warn)"} stroke={state === "offline" ? "var(--fg3)" : "none"} strokeDasharray={state === "offline" ? "2 1.5" : undefined} />}
      <text x={x + (state ? 26 : 12)} y={y + 17} fill="var(--fg)" fontSize={13} fontWeight={state ? 500 : 600}>
        {name}
      </text>
      <text x={x + (state ? 26 : 12)} y={y + 32} fill="var(--fg2)" fontSize={11}>
        {detail}
      </text>
    </g>
  )
}

export function TopologyView() {
  const live = useEventsConnected()
  const topology = useQuery(topologyQuery(live))
  const sessions = useMachineQueries(sessionsQuery)
  const data = topology.data
  // Replication enrolment is known locally, so the edge marks it without another peer probe.
  const replication = useQuery(replicationQuery)
  const enrolled = new Set((replication.data?.peers ?? []).map((peer) => peer.node))

  if (!data) {
    return (
      <div className="p-(--s6)">
        {topology.error ? <Banner tone="danger">{`Couldn't load topology: ${topology.error.message}`}</Banner> : <RowSkeleton lines={2} />}
      </div>
    )
  }

  const count = (machine: string, fromTopology?: number) => sessionCount(fromTopology, sessions.byMachine[machine]?.data?.length)
  const { diagram, unreachable } = partitionPeers(data.peers)
  const layout = layoutDiagram(diagram)
  const reachable = data.peers.filter((peer) => peer.state === "reachable").length
  const offline = data.peers.filter((peer) => peer.state === "offline").length
  const gatewaySessions = count(LOCAL)

  const rows = [
    { id: LOCAL, name: data.self.name, os: data.self.os, state: "Gateway", path: "—", sessions: gatewaySessions, reason: "This machine" },
    ...data.peers.map((peer) => ({
      id: peer.id,
      name: peer.name,
      os: peer.os,
      state: stateWord(peer.state),
      path: peer.state === "reachable" ? edgeOf(peer).label : "—",
      sessions: count(peer.id, peer.sessions),
      reason: reasonOf(peer),
    })),
  ]

  return (
    <div className="h-full min-h-0 overflow-y-auto">
      <Inspector>
        {topology.error && (
          <Banner tone="danger" className="col-span-full">
            {`Couldn't refresh topology: ${topology.error.message}`}
          </Banner>
        )}
        <ToolbarTitle title="Topology" subtitle={`${reachable} reachable · ${offline} offline · ${unreachable.length} not reachable by Agentpack`} />
        <h1 className="sr-only">Topology</h1>
        {/* The diagram repeats the grid below, so assistive tech skips it, and phones show only the grid. */}
        <div className="col-span-full max-[760px]:hidden">
          <svg data-slot="topology-diagram" viewBox={`0 0 ${DIAGRAM_W} ${layout.height}`} width={DIAGRAM_W} height={layout.height} className="block h-auto max-w-full" aria-hidden="true" focusable="false">
            <g fill="none" strokeWidth={1.2}>
              {layout.edges.map((edge) => (
                <path key={edge.peer.id} d={edge.d} stroke={edge.dashed ? "var(--fg3)" : "var(--accent)"} strokeDasharray={edge.dashed ? "5 4" : undefined} />
              ))}
            </g>
            <g fill="var(--fg2)" fontSize={11} textAnchor="end" dominantBaseline="middle" stroke="var(--window)" strokeWidth={5} strokeLinejoin="round" paintOrder="stroke">
              {layout.edges.map((edge) => (
                <text key={edge.peer.id} x={edge.labelX} y={edge.labelY}>
                  {[edge.label, edge.peer.rtt_ms && `${edge.peer.rtt_ms} ms`, enrolled.has(edge.peer.id) && "⇄"].filter(Boolean).join(" · ")}
                </text>
              ))}
            </g>
            <Node x={0} y={layout.hubY - NODE_H / 2} width={GATEWAY_W} name={data.self.name} parts={["Gateway", data.self.os]} sessions={gatewaySessions} />
            {layout.nodes.map(({ peer, y }) => (
              <Node
                key={peer.id}
                x={PEER_X}
                y={y}
                width={PEER_W}
                name={peer.name}
                parts={[peer.os, stateWord(peer.state)]}
                sessions={count(peer.id, peer.sessions)}
                state={peer.state}
                dashed={peer.state === "offline"}
              />
            ))}
          </svg>
        </div>
        {unreachable.length > 0 && (
          <Section title="Not reachable by Agentpack" className="col-span-full">
            <ul className="flex flex-col">
              {unreachable.map((peer) => (
                <li key={peer.id} className="flex h-(--row-h) items-center gap-(--s4) border-b-[0.5px] border-(--sep-soft) last:border-b-0">
                  <span className="w-40 shrink-0 truncate">{peer.name}</span>
                  <span className="min-w-0 truncate text-(--fg2)">{reasonOf(peer)}</span>
                </li>
              ))}
            </ul>
          </Section>
        )}
        <Section title="Machines" className="col-span-full">
          <DataGrid aria-label="Machines and connections" className="min-w-180">
            <DataGridHead>
              <DataGridRow>
                <DataGridHeaderCell className="w-44">Machine</DataGridHeaderCell>
                <DataGridHeaderCell className="w-24">OS</DataGridHeaderCell>
                <DataGridHeaderCell className="w-32">State</DataGridHeaderCell>
                <DataGridHeaderCell className="w-28">Path</DataGridHeaderCell>
                <DataGridHeaderCell className="w-24">Sessions</DataGridHeaderCell>
                <DataGridHeaderCell>Reason</DataGridHeaderCell>
              </DataGridRow>
            </DataGridHead>
            <tbody>
              {rows.map((row) => (
                <DataGridRow key={row.id}>
                  <DataGridCell>{row.name}</DataGridCell>
                  <DataGridCell>{row.os}</DataGridCell>
                  <DataGridCell>{row.state}</DataGridCell>
                  <DataGridCell>{row.path}</DataGridCell>
                  <DataGridCell>{row.sessions ?? "—"}</DataGridCell>
                  <DataGridCell>{row.reason}</DataGridCell>
                </DataGridRow>
              ))}
            </tbody>
          </DataGrid>
        </Section>
      </Inspector>
    </div>
  )
}
