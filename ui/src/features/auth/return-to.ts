const sections = new Map([
  ["sessions", "Sessions"],
  ["library", "Library"],
  ["machines", "Machines"],
  ["usage", "Usage"],
  ["limits", "Limits"],
  ["settings", "Settings"],
  ["system", "System"],
])

export const consentPath = "/oauth/authorize"

export function safeReturnTo(value: string | undefined): string | undefined {
  if (!value?.startsWith("/") || value.startsWith("//")) return
  const [path] = value.split(/[?#]/, 1)
  if (!path || path.includes("\\") || /%(?:2f|5c)/i.test(path)) return
  let url: URL
  try {
    url = new URL(value, location.origin)
  } catch {
    return
  }
  // Reject inputs whose path changes when the browser resolves the URL.
  if (url.origin !== location.origin || url.pathname !== path) return
  if (path === "/" || path === consentPath || sections.has(path.split("/")[1] ?? "")) {
    return path + url.search + url.hash
  }
}

export function returnToLabel(href: string): string {
  const [path = "/"] = href.split(/[?#]/, 1)
  if (path === consentPath) return "Connect an assistant"
  const [, section = "", machine, session] = path.split("/")
  if (section === "sessions" && machine && session) {
    return `Session ${session} on ${machine === "local" ? "this machine" : machine}`
  }
  return machine ? path : sections.get(section) ?? "Sessions"
}
