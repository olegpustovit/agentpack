import { useState } from "react"
import { useQuery } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import { FormGroup, FormRow } from "@/components/kit/forms"
import { StatusGlyph } from "@/components/kit/status-dot"
import { Banner, Empty, RowSkeleton } from "@/components/kit/feedback"
import { ConfirmAlert, ConfirmAlertContent, Menu, MenuContent, MenuItem, MenuTrigger, RowMoreButton } from "@/components/kit/overlays"
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
  const replication = useQuery({ ...replicationQuery, refetchInterval: live ? false : 30_000 })
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
    <div className="flex min-w-0 flex-col gap-(--s7)">
      {!sharing && <Banner tone="warn" action={<Link className="text-(--accent)" to="/settings/$pane" params={{ pane: "sharing" }}>Open Sharing</Link>}>Turn on Tailscale sharing before enrolling a machine.</Banner>}
      {removedPeer && <Banner tone="warn" action={<Link className="text-(--accent)" to="/settings/$pane" params={{ pane: "connections" }}>Open Vault</Link>}>
        {`${removedPeer} keeps the MCP logins it already received. Sign out of OAuth logins in Vault and rotate other credentials at their providers.`}
      </Banner>}
      <FormGroup title="Machines" footer={canManage ? undefined : "Only the gateway owner can enroll or remove machines."}>
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
      </FormGroup>
    </div>
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
  const dot = !replica ? (peer.online ? "stopped" : "offline") : contact?.needs_update ? "needsUpdate" : trust || contact?.error ? "needsYou" : stale ? (peer.online ? "idle" : "offline") : "running"
  const busy = enroll.isPending || sync.isPending || remove.isPending

  return (
    <FormRow
      label={<span className="flex items-center gap-(--s3)"><StatusGlyph state={dot} />{peer.name}</span>}
      description={[status, contact?.schema_version ? `schema ${contact.schema_version}` : "", unavailable && replica?.mesh ? "" : unavailable].filter(Boolean).join(" · ")}
      detail={(trust || (contact?.error && !trust)) && <>
        {trust && <p className="text-(length:--fs-sm) text-(--fg2)">{replica?.mesh ? `Enroll this gateway on ${peer.name} to begin syncing.` : "Enroll to approve sharing with this machine."}</p>}
        {contact?.error && !trust && <p className="break-words text-(length:--fs-sm) text-(--danger-word)">{contact.error}</p>}
      </>}
    >
      {replica?.mesh ? (
        <>
          <Button disabled={Boolean(unavailable) || busy} onClick={() => sync.mutate(peer)}>Sync now</Button>
          <Menu>
            <MenuTrigger render={<RowMoreButton aria-label={`${peer.name} actions`} className="opacity-100" />} />
            <MenuContent align="end">
              <MenuItem disabled>{syncTime ? `Last sync ${syncTime.toLocaleString()}` : "Never synced"}</MenuItem>
              {contact?.at && <MenuItem disabled>{`Last contact ${new Date(contact.at).toLocaleString()}`}</MenuItem>}
              <MenuItem danger disabled={!canManage || busy} onClick={() => setRemoving(true)}>Remove from replication…</MenuItem>
            </MenuContent>
          </Menu>
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
            description={<>{`Stops syncing with ${peer.name}. Previously replicated data remains on both machines, including shared MCP logins. After removal, use `}<Link className="text-(--accent) underline" to="/settings/$pane" params={{ pane: "connections" }}>Vault</Link>{" to sign out of OAuth logins. Rotate other credentials at their providers."}</>}
            destructiveLabel="Remove"
            onConfirm={() => {
              setRemoving(false)
              remove.mutate(peer, { onSuccess: onRemoved })
            }}
          />
        )}
      </ConfirmAlert>
    </FormRow>
  )
}
