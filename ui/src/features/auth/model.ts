import type { Status } from "@/lib/api-types"

export type LoginFields = { email: string; password: string; confirm: string; currentPassword: string }
export type LoginErrors = Partial<Record<keyof LoginFields, string>>

export function loginErrors(status: Pick<Status, "claimed" | "legacy">, fields: LoginFields): LoginErrors {
  const errors: LoginErrors = {}
  const bytes = (value: string) => new TextEncoder().encode(value).length
  if (!fields.email.trim() || bytes(fields.email.trim()) > 254) errors.email = "Enter your email address (up to 254 bytes)."
  if (!status.claimed) {
    if (bytes(fields.password) < 12 || bytes(fields.password) > 72) errors.password = "Use 12 to 72 bytes for your password."
    if (fields.confirm !== fields.password) errors.confirm = "Passwords do not match."
    if (status.legacy) {
      if (!fields.currentPassword) errors.currentPassword = "Enter the gateway's current password."
      else if (fields.password !== fields.currentPassword) errors.password = "Legacy claiming keeps the current password. Enter it here too."
    }
  } else if (!fields.password) errors.password = "Enter your password."
  return errors
}
