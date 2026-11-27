import { ConfirmAlert, ConfirmAlertContent } from "@/components/kit/overlays"
import { harnessName, plural } from "@/lib/format"
import { rowKey, sessionLabel, type ListedSession } from "./group"
import { sessionRules } from "./rules"

// Asks only before work would be lost. Closing idle sessions is undoable, so it never asks (HIG: no alerts for undoable actions).
// `name` titles a project or machine close; the list shows only the agents that would be interrupted.
export function SessionActionAlert({ kind, rows, name, onClose, onConfirm }: {
  kind: "close" | "stop" | "restart"
  rows: ListedSession[]
  name?: string
  onClose: () => void
  onConfirm: () => void
}) {
  const verb = { close: "Close", stop: "Stop", restart: "Restart" }[kind]
  const busy = kind === "close" ? rows.filter((row) => sessionRules(row).busy) : rows
  const title = name ? `${verb} ${name}?`
    : rows.length === 1 ? `${verb} “${sessionLabel(rows[0]!)}”?`
    : `${verb} ${plural(rows.length, "session")}?`
  const label = kind === "close" ? "Stop and Close" : `${verb} ${rows.length > 1 ? "Agents" : "Agent"}`
  const description = kind === "close" ? `${busy.length === 1 ? "This agent is" : `${busy.length} agents are`} still busy and will stop. ${rows.length === 1 ? "The session stays" : `All ${rows.length} sessions stay`} in History.`
    : kind === "stop" ? "Stopped sessions stay in the list so you can resume them."
    : "The current turn may end unless the agent can resume."
  return (
    <ConfirmAlert open onOpenChange={(open) => { if (!open) onClose() }}>
      <ConfirmAlertContent title={title} destructiveLabel={label} onConfirm={onConfirm} description={(
        <>
          {description}
          <span role="list" aria-label={kind === "close" ? "Agents that will stop" : "Affected sessions"} tabIndex={busy.length > 1 ? 0 : undefined}
            className="mt-(--s3) block max-h-[36dvh] overflow-y-auto text-start">
            {busy.map((row) => (
              <span key={rowKey(row)} role="listitem" className="block wrap-anywhere border-t-[0.5px] border-(--line) py-(--s2)">
                {kind === "restart" ? "Restarts" : "Stops"} {harnessName(row.harness)} on {row.machineName} · {sessionLabel(row)}
              </span>
            ))}
          </span>
        </>
      )} />
    </ConfirmAlert>
  )
}
