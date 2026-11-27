import { useState } from "react"
import { Banner } from "@/components/kit/feedback"
import { FormGroup, FormRow } from "@/components/kit/forms"
import { Button } from "@/components/ui/button"
import { Switch } from "@/components/ui/switch"
import { LIMIT_WARN, loadNotifyPrefs, notificationPermission, saveNotifyPrefs, showNotification, type AlertKind } from "@/features/shell/attention"
import { Pane } from "./panes"
import { PushRows } from "./push-rows"

const events: { kind: AlertKind; label: string; note?: string }[] = [
  { kind: "needs", label: "An agent needs you" },
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
      {permission === "denied" && <Banner tone="warn">Notifications are blocked for this site. Allow them in the browser's site settings, then reload.</Banner>}
      {permission === "unsupported" && <Banner tone="warn">This browser can't show notifications. Watch the tab title for sessions that need you.</Banner>}
      <FormGroup title="Delivery">
        <FormRow label="While a gateway tab is open" description={permissionWords[permission]}>
          {permission === "default" && <Button type="button" onClick={() => void Notification.requestPermission().then(setPermission, () => setPermission(notificationPermission()))}>Turn on</Button>}
          {permission === "granted" && (
            <Button type="button" variant="ghost" onClick={() => showNotification("Agentpack", { body: "Notifications are on.", tag: "test" })}>Send test</Button>
          )}
        </FormRow>
        <PushRows permission={permission} onPermissionChange={setPermission} />
      </FormGroup>
      <FormGroup title="Notify me when">
        {events.map((event) => (
          <FormRow key={event.kind} label={event.label} htmlFor={`notify-${event.kind}`} description={event.note}>
            <Switch id={`notify-${event.kind}`} checked={prefs[event.kind]} onCheckedChange={(on) => toggle(event.kind, on)} />
          </FormRow>
        ))}
      </FormGroup>
    </Pane>
  )
}
