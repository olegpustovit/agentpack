import { useEffect, useState } from "react"
import { useMutation } from "@tanstack/react-query"
import { FieldError } from "@/components/kit/forms"
import { Button } from "@/components/ui/button"
import { Switch } from "@/components/ui/switch"
import { errorText } from "@/features/library/mutations"
import { ApiError } from "@/lib/api"
import { hud } from "@/lib/hud"
import { Row } from "./panes"
import { currentPushSubscription, pushBlocker, pushBlockerText, subscribePush, testPush, unsubscribePush } from "./push"

function pushError(error: unknown): string {
  // An older gateway build has no push routes.
  if (error instanceof ApiError && error.status === 404) return "This gateway needs an update for push notifications."
  return errorText(error)
}

// "Push to this device": notifications that arrive with every gateway tab closed.
export function PushRows() {
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
    mutationFn: async (on: boolean) => {
      if (on) return subscribePush()
      await unsubscribePush()
      return null
    },
    onSuccess: (next) => {
      setSubscription(next)
      hud.success(next ? "Push on for this device" : "Push off for this device")
    },
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
        checked={change.isPending ? Boolean(change.variables) : on}
        disabled={Boolean(blocker) || subscription === undefined || change.isPending}
        onCheckedChange={(next) => change.mutate(next)}
      />
      <span className="text-(length:--fs-sm) text-(--fg2)">
        {blocker ? "Unavailable" : on ? "On. Arrives even with every tab closed" : "Off"}
      </span>
      {subscription && (
        <Button variant="ghost" disabled={test.isPending} onClick={() => test.mutate(subscription)}>
          Send a test
        </Button>
      )}
      {blocker && (
        <p data-slot="push-blocker" className="m-0 basis-full max-w-[60ch] text-(length:--fs-xs) text-(--fg2)">
          {pushBlockerText[blocker]}
        </p>
      )}
      {failure && <FieldError className="basis-full">{pushError(failure)}</FieldError>}
    </Row>
  )
}
