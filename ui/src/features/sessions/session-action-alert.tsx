import { ConfirmAlert, ConfirmAlertContent } from "@/components/kit/overlays"
import { harnessName, plural } from "@/lib/format"
import { rowKey, sessionLabel, type ListedSession } from "./group"
import { sessionRules } from "./rules"

export function SessionActionAlert({ kind, rows, onClose, onConfirm }: {
  kind: "archive" | "stop" | "restart"
  rows: ListedSession[]
  onClose: () => void
  onConfirm: () => void
}) {
  const verb = { archive: "Archive", stop: "Stop", restart: "Restart" }[kind]
  const title = rows.length === 1
    ? `${verb} “${sessionLabel(rows[0]!)}”?`
    : `${verb} ${plural(rows.length, "session")}?`
  const label = kind === "archive" ? "Stop and archive" : `${verb} ${rows.length > 1 ? "agents" : "agent"}`
  const description = kind === "archive" ? "Archived sessions leave the list and stay read-only in history."
    : kind === "stop" ? "Stopped sessions stay in the list so you can resume them."
    : "The current turn may end unless the agent can resume."
  return (
    <ConfirmAlert open onOpenChange={(open) => { if (!open) onClose() }}>
      <ConfirmAlertContent title={title} destructiveLabel={label} onConfirm={onConfirm} description={(
        <>
          {description}
          <span role="list" aria-label="Affected sessions" tabIndex={rows.length > 1 ? 0 : undefined}
            className="mt-(--s3) block max-h-[36dvh] overflow-y-auto text-start">
            {rows.map((row) => {
              const action = kind === "restart" ? "Restarts" : kind === "stop" || sessionRules(row).stop ? "Stops" : "Hides"
              return <span key={rowKey(row)} role="listitem" className="block wrap-anywhere border-t-[0.5px] border-(--line) py-(--s2)">{action} {harnessName(row.harness)} on {row.machineName} · {sessionLabel(row)}</span>
            })}
          </span>
        </>
      )} />
    </ConfirmAlert>
  )
}
