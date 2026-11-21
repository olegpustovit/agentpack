// getRandomValues also works on gateways served over plain HTTP.
export function newIntentID(): string {
  const bytes = crypto.getRandomValues(new Uint8Array(16))
  return Array.from(bytes, (byte) => byte.toString(16).padStart(2, "0")).join("")
}
