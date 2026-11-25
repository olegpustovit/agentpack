import { useState } from "react"
import { useQuery } from "@tanstack/react-query"
import { RowSkeleton } from "@/components/kit/feedback"
import { FormGroup, FormRow } from "@/components/kit/forms"
import { ConfirmAlert, ConfirmAlertContent } from "@/components/kit/overlays"
import { Button } from "@/components/ui/button"
import { identityQuery } from "@/features/machines/queries"
import { statusQuery } from "@/features/status/queries"
import { useGatewayActions } from "./actions"
import { LoadError, Pane } from "./panes"

// Identity only: who is signed in and where their logins live. Stopping this machine is in General.
export function AccountPane() {
  const identity = useQuery(identityQuery)
  const status = useQuery(statusQuery)
  const actions = useGatewayActions()
  const [confirm, setConfirm] = useState(false)

  if (identity.error) {
    return <LoadError what="your identity" error={identity.error} retry={() => void identity.refetch()} />
  }
  if (!identity.data) return <RowSkeleton lines={2} />

  return (
    <>
      <Pane title="Account">
        <FormGroup>
          <FormRow label="Signed in as" description="Sessions you start are attributed to this account.">{status.data?.email || identity.data.auth}</FormRow>
          <FormRow label="Tailscale user">{identity.data.tailscale_user || "None"}</FormRow>
        </FormGroup>
        <FormGroup title="Sign out" footer="Signing out leaves running agents running.">
          <FormRow label="This browser">
            <Button disabled={actions.signOut.isPending} onClick={() => actions.signOut.mutate("current")}>Sign out</Button>
          </FormRow>
          <FormRow label="Everywhere" description="Every browser and connected assistant (Claude.ai, ChatGPT) signs in again.">
            <Button variant="destructive" disabled={actions.signOut.isPending} onClick={() => setConfirm(true)}>Sign out everywhere…</Button>
          </FormRow>
        </FormGroup>
      </Pane>
      <ConfirmAlert open={confirm} onOpenChange={setConfirm}>
        <ConfirmAlertContent
          title="Sign out everywhere?"
          description={`End every login for ${status.data?.email || "this account"} on every browser, including connected assistants. Agents keep running.`}
          destructiveLabel="Sign out everywhere"
          onConfirm={() => {
            setConfirm(false)
            actions.signOut.mutate("all")
          }}
        />
      </ConfirmAlert>
    </>
  )
}
