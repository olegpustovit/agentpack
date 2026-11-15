import { useState } from "react"
import { useQuery } from "@tanstack/react-query"
import { Section } from "@/components/kit/data"
import { RowSkeleton } from "@/components/kit/feedback"
import { FieldGrid } from "@/components/kit/forms"
import { ConfirmAlert, ConfirmAlertContent } from "@/components/kit/overlays"
import { Button } from "@/components/ui/button"
import { identityQuery } from "@/features/machines/queries"
import { useGatewayActions } from "./actions"
import { LoadError, Pane, Row } from "./panes"
import { settingsQuery } from "./queries"

// Copy follows the legacy confirmations; the title names what will stop.
const confirms = {
	all: {
		title: "Sign out everywhere?",
		description: "End every login for this account, including connected assistants. Existing agents keep running.",
		destructiveLabel: "Sign out everywhere",
	},
  agents: {
    title: "Stop all agents on the gateway machine?",
    description: "Their current conversations may end.",
    destructiveLabel: "Stop all agents",
  },
  gateway: {
    title: "Stop this gateway?",
    description:
      "Browser access will disconnect. Existing CLI sessions will keep running. Use agentpack gateway start on the gateway machine to reconnect.",
    destructiveLabel: "Stop gateway",
  },
}

export function AccountPane() {
  const identity = useQuery(identityQuery)
  const settings = useQuery(settingsQuery)
  const actions = useGatewayActions()
  const [confirm, setConfirm] = useState<keyof typeof confirms | null>(null)

  if (identity.error) {
    return <LoadError what="your identity" error={identity.error} retry={() => void identity.refetch()} />
  }
  if (!identity.data) return <RowSkeleton lines={2} />
  const stopped = settings.data?.running === false

  return (
    <>
      <Pane title="Account">
        <FieldGrid>
          <Row label="Signed in as">{`${identity.data.auth}, on ${identity.data.node.name}`}</Row>
          <Row label="Tailscale user">{identity.data.tailscale_user || "None"}</Row>
          <Row label="">
            <p className="text-(length:--fs-sm) text-(--fg2)">
              Sessions are attributed to your account. Signing out leaves existing agents running.
            </p>
            <Button disabled={actions.signOut.isPending} onClick={() => actions.signOut.mutate("current")}>
              Sign out
            </Button>
            <Button disabled={actions.signOut.isPending} onClick={() => setConfirm("all")}>
              Sign out everywhere…
            </Button>
          </Row>
        </FieldGrid>
      </Pane>
      <Section title="Danger zone">
        <div className="flex flex-col gap-(--s4)">
          <div className="flex items-center justify-between gap-(--s6)">
            <p>Stop every agent on this machine. Their conversations may end.</p>
            <Button variant="destructive" disabled={stopped} onClick={() => setConfirm("agents")}>
              Stop all agents…
            </Button>
          </div>
          <div className="flex items-center justify-between gap-(--s6)">
            <p>Stop this gateway. CLI sessions keep running. Run <code>agentpack gateway start</code> to reconnect.</p>
            <Button variant="destructive" disabled={stopped} onClick={() => setConfirm("gateway")}>
              Stop gateway…
            </Button>
          </div>
        </div>
      </Section>
      <ConfirmAlert open={confirm !== null} onOpenChange={(open) => !open && setConfirm(null)}>
        {confirm && (
          <ConfirmAlertContent
            {...confirms[confirm]}
            onConfirm={() => {
              setConfirm(null)
              if (confirm === "all") actions.signOut.mutate("all")
              else if (confirm === "agents") actions.stopAgents.mutate()
              else actions.stopGateway.mutate()
            }}
          />
        )}
      </ConfirmAlert>
    </>
  )
}
