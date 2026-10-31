import { useQuery } from "@tanstack/react-query"
import { Network } from "lucide-react"
import { DataGrid, DataGridCell, DataGridHead, DataGridHeaderCell, DataGridRow, Inspector, InspectorHead, Section } from "@/components/kit/data"
import { Banner, RowSkeleton } from "@/components/kit/feedback"
import { useEventsConnected } from "@/features/events/use-events"
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

function Node({ x, y, width, name, parts, sessions, dashed }: { x: number; y: number; width: number; name: string; parts: string[]; sessions?: number; dashed?: boolean }) {
  const detail = [...parts, sessions === undefined ? undefined : plural(sessions, "session")].filter(Boolean).join(" · ")
  return (
    <g>
      <rect x={x} y={y} width={width} height={NODE_H} rx={7} fill="var(--window)" stroke="var(--sep)" strokeDasharray={dashed ? "4 3" : undefined} />
      <text x={x + 12} y={y + 17} fill="var(--fg)" fontSize={13} fontWeight={600}>
        {name}
      </text>
      <text x={x + 12} y={y + 32} fill="var(--fg2)" fontSize={11}>
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
        <InspectorHead
          icon={<Network aria-hidden className="size-(--s6)" />}
          title="Topology"
          subtitle={`${reachable} reachable · ${offline} offline · ${unreachable.length} not reachable by Agentpack`}
        />
        {/* The diagram repeats the grid below, so assistive tech skips it, and phones show only the grid. */}
        <div className="col-span-full max-[760px]:hidden">
          <svg data-slot="topology-diagram" viewBox={`0 0 ${DIAGRAM_W} ${layout.height}`} width={DIAGRAM_W} height={layout.height} className="block h-auto max-w-full" aria-hidden="true" focusable="false">
            <g fill="none" stroke="var(--fg3)" strokeWidth={1.2}>
              {layout.edges.map((edge) => (
                <path key={edge.peer.id} d={edge.d} strokeDasharray={edge.dashed ? "5 4" : undefined} />
              ))}
            </g>
            <g fill="var(--fg2)" fontSize={12} textAnchor="middle" dy="0.35em" stroke="var(--window)" strokeWidth={5} strokeLinejoin="round" paintOrder="stroke">
              {layout.edges.map((edge) => (
                <text key={edge.peer.id} x={edge.labelX} y={edge.labelY}>
                  {edge.label}
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
                dashed={peer.state === "offline"}
              />
            ))}
          </svg>
        </div>
        {unreachable.length > 0 && (
          <Section title="Not reachable by Agentpack" className="col-span-full">
            <ul className="flex flex-col gap-(--s2) text-(--fs-sm)">
              {unreachable.map((peer) => (
                <li key={peer.id}>
                  <span className="font-medium">{peer.name}</span> <span className="text-(--fg2)">{reasonOf(peer)}</span>
                </li>
              ))}
            </ul>
          </Section>
        )}
        <Section title="Machines" className="col-span-full">
          <DataGrid aria-label="Machines and connections">
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
