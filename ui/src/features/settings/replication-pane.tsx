import { useState } from "react"
import { useQuery } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import { Section } from "@/components/kit/data"
import { Banner, Empty, RowSkeleton } from "@/components/kit/feedback"
import { ConfirmAlert, ConfirmAlertContent } from "@/components/kit/overlays"
import { Button } from "@/components/ui/button"
import { useEventsConnected } from "@/features/events/use-events"
import { replicationQuery } from "@/features/library/queries"
import { nodesQuery } from "@/features/machines/queries"
import type { PeerReplica, TailnetNode, ReplicationPeers } from "@/lib/api-types"
import { relativeTime } from "@/lib/format"
import { useNow } from "@/lib/use-now"
import { useGatewayActions } from "./actions"
import { replicationStatus } from "./model"
import { LoadError } from "./panes"
import { settingsQuery } from "./queries"

export function ReplicationPane() {
  const live = useEventsConnected()
  const settings = useQuery(settingsQuery)
  const nodes = useQuery(nodesQuery(live, true))
  const replication = useQuery({ ...replicationQuery, refetchInterval: 30_000 })
  const [removedPeer, setRemovedPeer] = useState("")
  const failed = settings.error ?? nodes.error ?? replication.error
  if (failed) {
    return (
      <LoadError
        what="replication"
        error={failed}
        retry={() => {
          void settings.refetch()
          void nodes.refetch()
          void replication.refetch()
        }}
      />
    )
  }
  if (!settings.data || !nodes.data || !replication.data) return <RowSkeleton lines={2} />

  const sharing = settings.data.sharing_enabled
  const canManage = settings.data.can_manage !== false
  const enrolled = new Map((replication.data.peers ?? []).map((replica) => [replica.node, replica] as const))
  return (
    <>
      <Banner>
        {sharing
          ? "Enroll on both machines to sync accounts, chat history, limits and library data. Every mesh member is fully trusted. Only an owner can enroll or remove machines."
          : "Enable Tailscale sharing before enrolling a machine."}
      </Banner>
      {removedPeer && <Banner tone="warn" action={<Link className="text-(--accent)" to="/settings/$pane" params={{ pane: "connections" }}>Open Connections</Link>}>
        {`${removedPeer} keeps the MCP logins it already received. Sign out of OAuth logins in Connections and rotate other credentials at their providers.`}
      </Banner>}
      <Section title="Machines">
        {nodes.data.length === 0 ? (
          <Empty title="No allowed Tailscale peers">
            None are discovered right now. Check the allow list on the Sharing pane.
          </Empty>
        ) : (
          nodes.data.map((peer) => (
            <PeerRow
              key={peer.id}
              peer={peer}
              replica={enrolled.get(peer.id)}
              contact={replication.data.contacts?.[peer.id]}
              sharing={sharing}
              canManage={canManage}
              onRemoved={() => setRemovedPeer(peer.name)}
            />
          ))
        )}
      </Section>
    </>
  )
}

function PeerRow({
  peer,
  replica,
  contact,
  sharing,
  canManage,
  onRemoved,
}: {
  peer: TailnetNode
  replica: PeerReplica | undefined
  contact: ReplicationPeers["contacts"][string] | undefined
  sharing: boolean
  canManage: boolean
  onRemoved: () => void
}) {
  const { enroll, sync, remove } = useGatewayActions()
  const [removing, setRemoving] = useState(false)
  const [enrolling, setEnrolling] = useState(false)
  const now = useNow(Boolean(contact?.at), 15_000)
  const stale = !sharing || !peer.online || contact?.reachable === false || (contact?.at && now - Date.parse(contact.at) > 90_000)
  const trust = contact?.needs_trust || replica?.mesh === false
  const synced = contact?.last_sync
  const syncTime = synced ? new Date(synced) : undefined
  const status = trust ? "Waiting for trust" : contact?.needs_update ? "Needs update" : stale && synced ? "Stale"
    : contact?.error ? "Sync failed" : syncTime ? `Synced ${relativeTime(syncTime, Math.max(now, syncTime.getTime()))}` : replicationStatus(peer, replica)
  const unavailable = !canManage ? "Only the gateway owner can change enrollment." : !sharing ? "Turn on Tailscale sharing to sync or enroll." : !peer.online ? "Sync and enrollment require the machine to be online." : ""
  const busy = enroll.isPending || sync.isPending || remove.isPending

  return (
    <div className="flex min-w-0 flex-wrap items-center gap-(--s4) border-b-[0.5px] border-(--sep) py-(--s3)">
      <div className="min-w-0 flex-1 max-[760px]:basis-full">
        <p className="truncate font-medium">{peer.name}</p>
        <p className="text-(length:--fs-sm) text-(--fg2)">{status}</p>
        {trust && <p className="text-(length:--fs-sm) text-(--fg2)">{replica?.mesh ? `Enroll this gateway on ${peer.name} to begin syncing.` : "Enroll to approve sharing with this machine."}</p>}
        {syncTime && <p className="text-(length:--fs-xs) text-(--fg2)">{`Last successful sync ${syncTime.toLocaleString()}`}</p>}
        {unavailable && <p className="text-(length:--fs-xs) text-(--fg2)">{unavailable}</p>}
        {contact?.schema_version ? <p className="text-(length:--fs-xs) text-(--fg3)">{`Schema ${contact.schema_version} · Lag ${contact.lag ?? "unknown"} versions`}</p> : null}
        {contact?.error && !trust ? (
          <p className="break-words text-(length:--fs-sm) text-(--danger-word)">{contact.error}</p>
        ) : (
          contact?.at && <p className="text-(length:--fs-xs) text-(--fg3)">{`Last contact ${new Date(contact.at).toLocaleString()}`}</p>
        )}
      </div>
      {replica?.mesh ? (
        <>
          <Button disabled={Boolean(unavailable) || busy} onClick={() => sync.mutate(peer)}>
            Sync now
          </Button>
          <Button variant="ghost" disabled={!canManage || busy} onClick={() => setRemoving(true)}>
            Remove
          </Button>
        </>
      ) : (
        <Button disabled={Boolean(unavailable) || busy} onClick={() => setEnrolling(true)}>
          {replica ? "Enable mesh sync" : "Enroll"}
        </Button>
      )}
      <ConfirmAlert open={enrolling} onOpenChange={setEnrolling}>
        {enrolling && <ConfirmAlertContent
          title={`Trust ${peer.name} with this mesh?`}
          description={`Trust ${peer.name} with shared accounts and password hashes, chat history, limits, Library data and shared MCP logins. Enroll this gateway on ${peer.name} as well to start syncing.`}
          destructiveLabel="Trust and enroll"
          destructive={false}
          onConfirm={() => { setEnrolling(false); enroll.mutate(peer) }}
        />}
      </ConfirmAlert>
      <ConfirmAlert open={removing} onOpenChange={setRemoving}>
        {removing && (
          <ConfirmAlertContent
            title={`Remove ${peer.name} from replication?`}
            description={<>{`Stops syncing with ${peer.name}. Previously replicated data remains on both machines, including shared MCP logins. After removal, use `}<Link className="text-(--accent) underline" to="/settings/$pane" params={{ pane: "connections" }}>Connections</Link>{" to sign out of OAuth logins. Rotate other credentials at their providers."}</>}
            destructiveLabel="Remove"
            onConfirm={() => {
              setRemoving(false)
              remove.mutate(peer, { onSuccess: onRemoved })
            }}
          />
        )}
      </ConfirmAlert>
    </div>
  )
}
