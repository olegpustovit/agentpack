import { useState } from "react"
import { useQuery } from "@tanstack/react-query"
import { FormGroup, FormRow } from "@/components/kit/forms"
import { ConfirmAlert, ConfirmAlertContent } from "@/components/kit/overlays"
import { Button } from "@/components/ui/button"
import { identityQuery } from "@/features/machines/queries"
import { statusQuery } from "@/features/status/queries"
import { useGatewayActions } from "./actions"

// Who is signed in, first in General. Running agents survive either sign-out.
export function AccountGroup() {
  const identity = useQuery(identityQuery).data
  const status = useQuery(statusQuery).data
  const actions = useGatewayActions()
  const [confirm, setConfirm] = useState(false)
  const email = status?.email || identity?.auth || ""

  return (
    <FormGroup title="Account">
      <FormRow label={email || "Signed in"}>
        <Button disabled={actions.signOut.isPending} onClick={() => actions.signOut.mutate("current")}>Sign out</Button>
      </FormRow>
      <FormRow label="Sign out everywhere" description="Every browser and connected assistant signs in again.">
        <Button variant="destructive" disabled={actions.signOut.isPending} onClick={() => setConfirm(true)}>Sign out everywhere…</Button>
      </FormRow>
      <ConfirmAlert open={confirm} onOpenChange={setConfirm}>
        <ConfirmAlertContent
          title="Sign out everywhere?"
          description={`End every login for ${email || "this account"} on every browser, including connected assistants. Agents keep running.`}
          destructiveLabel="Sign out everywhere"
          onConfirm={() => {
            setConfirm(false)
            actions.signOut.mutate("all")
          }}
        />
      </ConfirmAlert>
    </FormGroup>
  )
}
