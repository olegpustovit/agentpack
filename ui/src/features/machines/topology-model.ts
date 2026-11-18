import type { TopologyPeer } from "@/lib/api-types"
import { relativeTime } from "@/lib/format"

// Diagram geometry in SVG user units. Rows are 44 px apart; the gateway sits left, peers stack right.
export const ROW_H = 44
export const NODE_H = 40
export const GATEWAY_W = 180
export const PEER_X = 420
export const PEER_W = 200
export const DIAGRAM_W = PEER_X + PEER_W

const stateWords: Record<string, string> = {
  reachable: "Reachable",
  offline: "Offline",
}

export function stateWord(state: string): string {
  return stateWords[state] ?? state
}

// The topology shows the same machines as the sidebar, with the same online state; the tailnet adds OS and path.
export function machinePeers(peers: TopologyPeer[], machines: { id: string; name: string; online: boolean }[]): TopologyPeer[] {
  return machines.map((machine) => {
    const peer = peers.find((candidate) => candidate.id === machine.id) ?? { id: machine.id, name: machine.name, os: "", state: "", connection: "", relay: "", sessions: 0 }
    return { ...peer, name: machine.name, state: machine.online ? "reachable" : "offline" }
  })
}

export function reasonOf(peer: TopologyPeer): string {
  if (peer.state === "offline") return peer.last_seen ? `Last seen ${relativeTime(new Date(peer.last_seen))}` : "Not connected"
  return ""
}

export type Edge = { label: string; dashed: boolean }

// Relay is dashed. A peer with no path info is labelled, not guessed as direct.
export function edgeOf(peer: TopologyPeer): Edge {
  if (peer.connection === "relay") return { label: peer.relay ? `relay ${peer.relay}` : "relay", dashed: true }
  if (peer.connection === "direct") return { label: "direct", dashed: false }
  return { label: "path unknown", dashed: false }
}

// The topology endpoint always reports 0 sessions, so the sessions query count is the real source.
export function sessionCount(fromTopology: number | undefined, fromQuery: number | undefined): number | undefined {
  return fromTopology || fromQuery
}

export type DiagramEdge = { peer: TopologyPeer; d: string; label: string; dashed: boolean; labelX: number; labelY: number }
export type Diagram = { height: number; hubY: number; nodes: { peer: TopologyPeer; y: number }[]; edges: DiagramEdge[] }

// Only reachable peers get an edge. Offline peers are nodes with no path.
export function layoutDiagram(peers: TopologyPeer[]): Diagram {
  const height = Math.max(peers.length, 1) * ROW_H
  const hubY = height / 2
  const midX = (GATEWAY_W + PEER_X) / 2
  const nodes = peers.map((peer, i) => ({ peer, y: i * ROW_H + (ROW_H - NODE_H) / 2 }))
  const edges = peers.flatMap((peer, i): DiagramEdge[] => {
    if (peer.state !== "reachable") return []
    const cy = i * ROW_H + ROW_H / 2
    const { label, dashed } = edgeOf(peer)
    const d = `M${GATEWAY_W} ${hubY}C${midX} ${hubY} ${midX} ${cy} ${PEER_X} ${cy}`
    return [{ peer, d, label, dashed, labelX: PEER_X - 8, labelY: cy - 8 }]
  })
  return { height, hubY, nodes, edges }
}
