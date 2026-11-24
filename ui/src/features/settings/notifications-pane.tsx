import { useState } from "react"
import { Section } from "@/components/kit/data"
import { Banner } from "@/components/kit/feedback"
import { FieldGrid } from "@/components/kit/forms"
import { StatusGlyph } from "@/components/kit/status-dot"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { attentionTitle, LIMIT_WARN, loadNotifyPrefs, notificationPermission, saveNotifyPrefs, showNotification, type AlertKind } from "@/features/shell/attention"
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
  const [permission, setPermission] = useState(notificationPermission)
  const [prefs, setPrefs] = useState(loadNotifyPrefs)
  function toggle(kind: AlertKind, on: boolean) {
    const next = { ...prefs, [kind]: on }
    saveNotifyPrefs(next)
    setPrefs(next)
  }
  return (
    <Pane title="Notifications">
      <div className="flex flex-col gap-(--s5)">
        <Section title="Delivery on this browser">
          {permission === "denied" && <Banner tone="warn">Notifications are blocked for this site. Allow them in the browser's site settings, then reload.</Banner>}
          {permission === "unsupported" && <Banner tone="warn">This browser can't show notifications. Open the gateway in a browser that supports notifications or watch the tab title for sessions that need you.</Banner>}
          <FieldGrid>
            <Row label="While a gateway tab is open">
              <span aria-hidden className="contents"><StatusGlyph state={permission === "granted" ? "running" : "stopped"} /></span>
              <span>{permissionWords[permission]}</span>
              {permission === "default" && <Button type="button" onClick={() => void Notification.requestPermission().then(setPermission, () => setPermission(notificationPermission()))}>Turn on</Button>}
              {permission === "granted" && (
                <Button type="button" onClick={() => showNotification("Agentpack", { body: "Notifications are on.", tag: "test" })}>Send browser test</Button>
              )}
            </Row>
          </FieldGrid>
        </Section>
        <Section title="Delivery with tabs closed">
          <FieldGrid><PushRows permission={permission} onPermissionChange={setPermission} /></FieldGrid>
        </Section>
        <Section title="Events">
          {permission !== "granted" && <p className="text-(length:--fs-sm) text-(--fg2)">Applies once delivery is on.</p>}
          <FieldGrid>
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
            <Row label="Example tab title">
              <div className="flex flex-col gap-(--s2)">
                <span>{attentionTitle(2)}</span>
                <span className="text-(length:--fs-sm) text-(--fg2)">The count of sessions that need you, also shown on the installed app's icon badge.</span>
              </div>
            </Row>
          </FieldGrid>
        </Section>
      </div>
    </Pane>
  )
}

