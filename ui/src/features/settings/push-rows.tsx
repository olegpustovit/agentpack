import { useEffect, useState } from "react"
import { useMutation } from "@tanstack/react-query"
import { FieldError } from "@/components/kit/forms"
import { Button } from "@/components/ui/button"
import { Switch } from "@/components/ui/switch"
import { errorText, routeMissing } from "@/lib/api"
import { hud } from "@/lib/hud"
import { Row } from "./panes"
import { currentPushSubscription, pushBlocker, pushBlockerText, subscribePush, testPush, unsubscribePush } from "./push"

function pushError(error: unknown): string {
  // An older gateway build has no push routes.
  if (routeMissing(error)) return "This gateway needs an update for push notifications."
  return errorText(error)
}

// "Push to this device": notifications that arrive with every gateway tab closed.
export function PushRows({ permission, onPermissionChange }: { permission: NotificationPermission | "unsupported"; onPermissionChange: (permission: NotificationPermission) => void }) {
  const blocker = pushBlocker()
  const [subscription, setSubscription] = useState<PushSubscription | null | undefined>(undefined)
  useEffect(() => {
    let live = true
    void currentPushSubscription()
      .catch(() => null)
      .then((found) => {
        if (live) setSubscription(found)
      })
    return () => {
      live = false
    }
  }, [])
  const change = useMutation({
    mutationFn: async ({ permission }: { on: boolean; permission?: Promise<NotificationPermission> }) => {
      if (permission) return subscribePush(permission)
      await unsubscribePush()
      return null
    },
    onSuccess: setSubscription,
  })
  const test = useMutation({
    mutationFn: (target: PushSubscription) => testPush(target),
    onSuccess: () => hud.success("Test sent"),
  })
  const on = Boolean(subscription)
  const failure = change.error ?? test.error
  return (
    <Row label="Push to this device" htmlFor="push-device">
      <Switch
        id="push-device"
        checked={change.isPending ? Boolean(change.variables?.on) : on}
        disabled={Boolean(blocker) || subscription === undefined || change.isPending}
        onCheckedChange={(next) => change.mutate({
          on: next,
          permission: next ? Notification.requestPermission().then((result) => { onPermissionChange(result); return result }) : undefined,
        })}
      />
      <span className="text-(length:--fs-sm) text-(--fg2)">
        {blocker ? "Unavailable" : on ? "On. Arrives even with every tab closed" : "Off"}
      </span>
      {subscription && !blocker && (
        <Button variant="ghost" disabled={test.isPending} onClick={() => test.mutate(subscription)}>
          Send push test
        </Button>
      )}
      {blocker && permission !== "denied" && !(permission === "unsupported" && blocker === "unsupported") && (
        <p data-slot="push-blocker" className="m-0 basis-full max-w-[60ch] text-(length:--fs-xs) text-(--fg2)">
          {pushBlockerText[blocker]}
        </p>
      )}
      {failure && permission !== "denied" && <FieldError className="basis-full">{pushError(failure)}</FieldError>}
    </Row>
  )
}
