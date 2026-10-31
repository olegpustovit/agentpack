// Only the OAuth consent page may follow a login by full navigation. Anything else is ignored.
export function safeReturnTo(value: string | undefined): string | undefined {
  if (!value?.startsWith("/oauth/authorize")) return undefined
  const url = new URL(value, location.origin)
  return url.pathname === "/oauth/authorize" ? url.href : undefined
}
