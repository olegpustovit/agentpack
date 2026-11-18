import { useState } from "react"
import { useQuery } from "@tanstack/react-query"
import { Section } from "@/components/kit/data"
import { Banner, Empty, RowSkeleton } from "@/components/kit/feedback"
import { ConfirmAlert, ConfirmAlertContent } from "@/components/kit/overlays"
import { Button } from "@/components/ui/button"
import { useEventsConnected } from "@/features/events/use-events"
import { replicationQuery } from "@/features/library/queries"
import { nodesQuery } from "@/features/machines/queries"
import type { PeerReplica, TailnetNode, ReplicationPeers } from "@/lib/api-types"
import { useGatewayActions } from "./actions"
import { replicationStatus } from "./model"
import { LoadError } from "./panes"
import { settingsQuery } from "./queries"

export function ReplicationPane() {
  const live = useEventsConnected()
  const settings = useQuery(settingsQuery)
  const nodes = useQuery(nodesQuery(live, true))
  const replication = useQuery(replicationQuery)
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
  const enrolled = new Map((replication.data.peers ?? []).map((replica) => [replica.node, replica] as const))
  return (
    <>
      <Banner>
        {sharing
          ? "Enroll on both machines to sync accounts, chat history, limits and library data. Every mesh member is fully trusted. Only an owner can enroll or remove machines."
          : "Enable Tailscale sharing before enrolling a machine."}
      </Banner>
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
}: {
  peer: TailnetNode
  replica: PeerReplica | undefined
  contact: ReplicationPeers["contacts"][string] | undefined
  sharing: boolean
}) {
  const { enroll, sync, remove } = useGatewayActions()
  const [removing, setRemoving] = useState(false)
  const [enrolling, setEnrolling] = useState(false)
  const usable = peer.online && sharing
  const busy = enroll.isPending || sync.isPending || remove.isPending

  return (
    <div className="flex min-w-0 items-center gap-(--s4) border-b-[0.5px] border-(--sep) py-(--s3)">
      <div className="min-w-0 flex-1">
        <p className="truncate font-medium">{peer.name}</p>
        <p className="text-(length:--fs-sm) text-(--fg2)">{replicationStatus(peer, replica)}</p>
        {contact?.needs_update && <p className="text-(length:--fs-sm) text-(--fg2)">Needs update</p>}
        {contact?.last_sync && <p className="text-(length:--fs-xs) text-(--fg3)">{`Last sync ${new Date(contact.last_sync).toLocaleString()}`}</p>}
        {contact?.schema_version ? <p className="text-(length:--fs-xs) text-(--fg3)">{`Schema ${contact.schema_version} · Lag ${contact.lag ?? "unknown"} versions`}</p> : null}
        {contact?.error ? (
          <p className="text-(length:--fs-sm) text-(--danger)">{contact.error}</p>
        ) : (
          contact?.at && <p className="text-(length:--fs-xs) text-(--fg3)">{`Last contact ${new Date(contact.at).toLocaleString()}`}</p>
        )}
      </div>
      {replica?.mesh ? (
        <>
          <Button disabled={!usable || busy} onClick={() => sync.mutate(peer)}>
            Sync now
          </Button>
          <Button variant="ghost" disabled={busy} onClick={() => setRemoving(true)}>
            Remove
          </Button>
        </>
      ) : (
        <Button disabled={!usable || busy} onClick={() => setEnrolling(true)}>
          {replica ? "Enable mesh sync" : "Enroll"}
        </Button>
      )}
      <ConfirmAlert open={enrolling} onOpenChange={setEnrolling}>
        {enrolling && <ConfirmAlertContent
          title={`Trust ${peer.name} with this mesh?`}
          description="This machine will receive password hashes, chat history and other shared state."
          destructiveLabel="Trust and enroll"
          onConfirm={() => { setEnrolling(false); enroll.mutate(peer) }}
        />}
      </ConfirmAlert>
      <ConfirmAlert open={removing} onOpenChange={setRemoving}>
        {removing && (
          <ConfirmAlertContent
            title={`Remove ${peer.name} from replication?`}
            description={`Stops syncing accounts, history and library data with ${peer.name}. Previously replicated data remains on both machines.`}
            destructiveLabel="Remove"
            onConfirm={() => {
              setRemoving(false)
              remove.mutate(peer)
            }}
          />
        )}
      </ConfirmAlert>
    </div>
  )
}
