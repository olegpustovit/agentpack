import type { TopologyPeer } from "@/lib/api-types"
import { relativeTime } from "@/lib/format"

// Diagram geometry in SVG user units. Rows are 44 px apart; the gateway sits left, peers stack right.
export const ROW_H = 44
export const NODE_H = 40
export const GATEWAY_W = 180
export const PEER_X = 320
export const PEER_W = 200
export const DIAGRAM_W = PEER_X + PEER_W

const stateWords: Record<string, string> = {
  reachable: "Reachable",
  offline: "Offline",
  "no-gateway": "No gateway",
  "not-allowed": "Not allowed",
}

export function stateWord(state: string): string {
  return stateWords[state] ?? state
}

const unreachableStates: Record<string, true> = { "no-gateway": true, "not-allowed": true }

// Online peers without a gateway, or not allowed, go under the unreachable heading. The rest are diagram nodes.
export function partitionPeers(peers: TopologyPeer[]) {
  return {
    diagram: peers.filter((peer) => unreachableStates[peer.state] !== true),
    unreachable: peers.filter((peer) => unreachableStates[peer.state] === true),
  }
}

const unreachableReasons: Record<string, string> = {
  "no-gateway": "No gateway running on this device",
  "not-allowed": "Not on the sharing allow list",
}

export function reasonOf(peer: TopologyPeer): string {
  if (peer.state === "offline") return peer.last_seen ? `Last seen ${relativeTime(new Date(peer.last_seen))}` : "Not connected"
  return unreachableReasons[peer.state] ?? ""
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
  const labelX = (GATEWAY_W + PEER_X) / 2
  const nodes = peers.map((peer, i) => ({ peer, y: i * ROW_H + (ROW_H - NODE_H) / 2 }))
  const edges = peers.flatMap((peer, i): DiagramEdge[] => {
    if (peer.state !== "reachable") return []
    const cy = i * ROW_H + ROW_H / 2
    const { label, dashed } = edgeOf(peer)
    const d = `M${GATEWAY_W} ${hubY}C${labelX} ${hubY} ${labelX} ${cy} ${PEER_X} ${cy}`
    return [{ peer, d, label, dashed, labelX, labelY: (hubY + cy) / 2 }]
  })
  return { height, hubY, nodes, edges }
}
