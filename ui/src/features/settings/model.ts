import type { PeerReplica, TailnetNode } from "@/lib/api-types"

export function sharingLabel(enabled: boolean): string {
  return enabled ? "Enabled for allowed Tailscale identities" : "Disabled. Local CLI sessions still work."
}

export function allowListLabel(peers: string[] | null | undefined): string {
  return peers?.length ? peers.join(", ") : "No additional machines or tags"
}

// Enrollment persists when a machine goes offline.
export function replicationStatus(peer: TailnetNode, replica: PeerReplica | undefined): string {
  if (replica) return `${replica.mesh ? "Enrolled" : "Needs mesh approval"} · ${peer.online ? "online" : "offline"}`
  return peer.online ? "Not enrolled" : "Offline · enrollment unavailable"
}
