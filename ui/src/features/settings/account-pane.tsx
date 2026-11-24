import { useState } from "react"
import { useQuery } from "@tanstack/react-query"
import { RowSkeleton } from "@/components/kit/feedback"
import { FieldGrid } from "@/components/kit/forms"
import { ConfirmAlert, ConfirmAlertContent } from "@/components/kit/overlays"
import { Button } from "@/components/ui/button"
import { identityQuery } from "@/features/machines/queries"
import { statusQuery } from "@/features/status/queries"
import { useGatewayActions } from "./actions"
import { LoadError, Pane, Row } from "./panes"

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
        <FieldGrid>
          <Row label="Signed in as">{status.data?.email || identity.data.auth}</Row>
          <Row label="Tailscale user">{identity.data.tailscale_user || "None"}</Row>
          <Row label="Connected assistants">
            <p className="text-(length:--fs-sm) text-(--fg2)">Sign out everywhere also ends this account’s logins in connected assistants.</p>
          </Row>
          <Row label="">
            <p className="text-(length:--fs-sm) text-(--fg2)">
              Sessions are attributed to your account. Signing out leaves existing agents running.
            </p>
            <Button disabled={actions.signOut.isPending} onClick={() => actions.signOut.mutate("current")}>
              Sign out
            </Button>
            <Button disabled={actions.signOut.isPending} onClick={() => setConfirm(true)}>
              Sign out everywhere…
            </Button>
          </Row>
        </FieldGrid>
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
