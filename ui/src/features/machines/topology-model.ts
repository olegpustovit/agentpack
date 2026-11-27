import type { PeerReplica, ReplicationPeers, TailnetNode, TopologyPeer } from "@/lib/api-types"
import { relativeTime } from "@/lib/format"

// Diagram geometry in SVG user units. Rows are 44 px apart; the gateway sits left, peers stack right.
export const ROW_H = 44
export const NODE_H = 40
export const GATEWAY_W = 180
export const PEER_X = 420
export const PEER_W = 200
export const DIAGRAM_W = PEER_X + PEER_W

// The topology shows the same machines as the sidebar, with the same online state; the tailnet adds OS and path.
export function machinePeers(peers: TopologyPeer[], machines: { id: string; name: string; online: boolean }[]): TopologyPeer[] {
  return machines.map((machine) => {
    const peer = peers.find((candidate) => candidate.id === machine.id) ?? { id: machine.id, name: machine.name, os: "", state: "", connection: "", relay: "", sessions: 0 }
    return { ...peer, name: machine.name, state: machine.online ? "reachable" : "offline" }
  })
}

export type MeshState = { text: string; glyph: "running" | "idle" | "offline" | "stopped" | "needsYou" | "needsUpdate"; warn?: boolean; detail?: string; member?: boolean }

// One line for a machine's place in the mesh: whether it syncs, and the one thing to do when it doesn't.
export function meshState(node: TailnetNode, replica: PeerReplica | undefined, contact: ReplicationPeers["contacts"][string] | undefined, lastSeen: string | undefined, sharing: boolean, now: number): MeshState {
  const age = (at: string) => relativeTime(new Date(at), Math.max(now, Date.parse(at)))
  if (!replica) return { text: node.online ? "Not in mesh" : "Not in mesh · offline", glyph: node.online ? "stopped" : "offline" }
  if (contact?.needs_trust || replica.mesh === false) {
    return { text: replica.mesh ? `Add this machine on ${node.name} to sync` : "Waiting for your approval", glyph: "needsYou", warn: true, member: true }
  }
  if (contact?.needs_update || node.needs_update) return { text: "Needs update to sync", glyph: "needsUpdate", warn: true, member: true }
  const seen = lastSeen ?? contact?.at
  if (!node.online) return { text: seen ? `Offline · last seen ${age(seen)}` : "Offline", glyph: "offline", member: true }
  if (contact?.error) return { text: "Sync failed", glyph: "needsYou", warn: true, detail: contact.error, member: true }
  const stale = !sharing || contact?.reachable === false || (!!contact?.at && now - Date.parse(contact.at) > 90_000)
  const synced = contact?.last_sync
  if (synced) return { text: `${stale ? "Last synced" : "Synced"} ${age(synced)}`, glyph: stale ? "idle" : "running", member: true }
  return { text: "Not synced yet", glyph: "idle", member: true }
}

export type Edge = { label: string; dashed: boolean }

// Relay is dashed. A peer with no path info is labelled, not guessed as direct.
export function edgeOf(peer: TopologyPeer): Edge {
  if (peer.connection === "relay") return { label: peer.relay ? `relay ${peer.relay}` : "relay", dashed: true }
  if (peer.connection === "direct") return { label: "direct", dashed: false }
  return { label: "path unknown", dashed: false }
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
