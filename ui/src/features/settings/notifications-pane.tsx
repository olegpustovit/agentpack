import { useState } from "react"
import { Banner } from "@/components/kit/feedback"
import { FieldGrid } from "@/components/kit/forms"
import { StatusGlyph } from "@/components/kit/status-dot"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { attentionTitle, LIMIT_WARN, loadNotifyPrefs, saveNotifyPrefs, showNotification, usePermission, type AlertKind } from "@/features/shell/attention"
import { Pane, Row } from "./panes"
import { PushRows } from "./push-rows"

const events: { kind: AlertKind; label: string; note?: string }[] = [
  { kind: "needs", label: "An agent needs you", note: "permission, question or plan" },
  { kind: "finished", label: "A turn finishes" },
  { kind: "limit", label: `A limit drops below ${LIMIT_WARN}%` },
  { kind: "offline", label: "A machine goes offline" },
]

const permissionWords = { granted: "Allowed", denied: "Blocked", default: "Not asked yet", unsupported: "Not supported" }

export function NotificationsPane() {
  const { permission, ask } = usePermission()
  const [prefs, setPrefs] = useState(loadNotifyPrefs)
  function toggle(kind: AlertKind, on: boolean) {
    const next = { ...prefs, [kind]: on }
    saveNotifyPrefs(next)
    setPrefs(next)
  }
  return (
    <Pane title="Notifications">
      {permission === "denied" ? (
        <Banner tone="warn" className="mb-(--s5)">Notifications are blocked for this site. Allow them in the browser's site settings, then reload.</Banner>
      ) : permission === "unsupported" ? (
        <Banner tone="warn" className="mb-(--s5)">This browser can't show notifications. The tab title still counts the sessions that need you.</Banner>
      ) : (
        <Banner className="mb-(--s5)">This browser notifies while a gateway tab is open in the background. Push to this device also covers closed tabs.</Banner>
      )}
      <FieldGrid>
        <Row label="This browser">
          <span aria-hidden className="contents"><StatusGlyph state={permission === "granted" ? "running" : "stopped"} /></span>
          <span>{permissionWords[permission]}</span>
          {permission === "default" && <Button type="button" onClick={ask}>Turn on</Button>}
          {permission === "granted" && (
            <Button type="button" onClick={() => showNotification("Agentpack", { body: "Notifications are on.", tag: "test" })}>Send a test</Button>
          )}
        </Row>
        <Row label="Notify me when">
          <div className="flex flex-col gap-(--s3)">
            {events.map((event) => (
              <label key={event.kind} className="flex items-center gap-(--s3)">
                <Checkbox checked={prefs[event.kind]} onCheckedChange={(on) => toggle(event.kind, on)} />
                {event.label}
                {event.note && <span className="text-(--fg3)">{`· ${event.note}`}</span>}
              </label>
            ))}
          </div>
        </Row>
        <PushRows />
        <Row label="Tab title">
          <div className="flex flex-col gap-(--s2)">
            <span>{attentionTitle(2)}</span>
            <span className="text-(length:--fs-sm) text-(--fg2)">The count of sessions that need you, also shown on the installed app's icon badge.</span>
          </div>
        </Row>
      </FieldGrid>
    </Pane>
  )
}

