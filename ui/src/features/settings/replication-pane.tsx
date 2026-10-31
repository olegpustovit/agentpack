import { useState } from "react"
import { useQuery } from "@tanstack/react-query"
import { Section } from "@/components/kit/data"
import { Banner, Empty, RowSkeleton } from "@/components/kit/feedback"
import { ConfirmAlert, ConfirmAlertContent } from "@/components/kit/overlays"
import { Button } from "@/components/ui/button"
import { useEventsConnected } from "@/features/events/use-events"
import { replicationQuery } from "@/features/library/queries"
import { nodesQuery } from "@/features/machines/queries"
import type { PeerReplica, TailnetNode } from "@/lib/api-types"
import { useGatewayActions } from "./actions"
import { replicationStatus } from "./model"
import { LoadError } from "./panes"
import { settingsQuery } from "./queries"

export function ReplicationPane() {
  const live = useEventsConnected()
  const settings = useQuery(settingsQuery)
  const nodes = useQuery(nodesQuery(live))
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
          ? "Both machines must enroll each other before registry objects replicate."
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
  contact: { at: string; error?: string } | undefined
  sharing: boolean
}) {
  const { enroll, sync, remove } = useGatewayActions()
  const [removing, setRemoving] = useState(false)
  const usable = peer.online && sharing
  const busy = enroll.isPending || sync.isPending || remove.isPending

  return (
    <div className="flex min-w-0 items-center gap-(--s4) border-b-[0.5px] border-(--sep) py-(--s3)">
      <div className="min-w-0 flex-1">
        <p className="truncate font-medium">{peer.name}</p>
        <p className="text-(--fs-sm) text-(--fg2)">{replicationStatus(peer, replica)}</p>
        {contact?.error ? (
          <p className="text-(--fs-sm) text-(--danger)">{contact.error}</p>
        ) : (
          contact?.at && <p className="text-(--fs-xs) text-(--fg3)">{`Last contact ${new Date(contact.at).toLocaleString()}`}</p>
        )}
      </div>
      {replica ? (
        <>
          <Button disabled={!usable || busy} onClick={() => sync.mutate(peer)}>
            Sync now
          </Button>
          <Button variant="ghost" disabled={!usable || busy} onClick={() => setRemoving(true)}>
            Remove
          </Button>
        </>
      ) : (
        <Button disabled={!usable || busy} onClick={() => enroll.mutate(peer)}>
          Enroll
        </Button>
      )}
      <ConfirmAlert open={removing} onOpenChange={setRemoving}>
        {removing && (
          <ConfirmAlertContent
            title={`Remove ${peer.name} from replication?`}
            description={`Stops replicating library objects with ${peer.name}. Enroll it again to resume.`}
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
