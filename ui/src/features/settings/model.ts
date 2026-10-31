import type { PeerReplica, TailnetNode } from "@/lib/api-types"
import { plural } from "@/lib/format"

export function sharingLabel(enabled: boolean): string {
  return enabled ? "Enabled for allowed Tailscale identities" : "Disabled. Local CLI sessions still work."
}

export function allowListLabel(peers: string[] | null | undefined): string {
  return peers?.length ? peers.join(", ") : "No additional machines or tags"
}

// An enrolled peer keeps its receipts even while offline, so the replica wins over discovery.
export function replicationStatus(peer: TailnetNode, replica: PeerReplica | undefined): string {
  if (replica) return `${plural(replica.receipt_count, "verified object receipt")} · ${peer.online ? "online" : "offline"}`
  return peer.online ? "Not enrolled" : "Offline · enrollment unavailable"
}
