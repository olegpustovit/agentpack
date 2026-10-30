import { toast } from "sonner"

// Plain functions so any module can report an outcome without a hook.
// Failures stay until dismissed, per the alert model.
export const hud = {
  success(message: string) {
    toast.success(message)
  },
  error(message: string, retry?: () => void) {
    toast.error(message, {
      duration: Number.POSITIVE_INFINITY,
      action: retry ? { label: "Retry", onClick: retry } : undefined,
    })
  },
}
