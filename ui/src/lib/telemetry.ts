type Crumb = { kind: string; name: string; trace_id?: string; status?: number }
type PageContext = { route: string; search: Record<string, unknown>; ui_build_id?: string }
const crumbs: Crumb[] = []
const pending: object[] = []
const seen = new Map<string, number>()
const reported = new WeakSet<object>()
let page = (): PageContext => ({ route: "", search: {} })
let lastTrace = ""
let minute = 0, count = 0
let timer: ReturnType<typeof setTimeout> | undefined

export function breadcrumb(crumb: Crumb) {
  crumbs.push({ ...crumb, name: crumb.name.slice(0, 128) })
  if (crumbs.length > 30) crumbs.shift()
}

export function traceRequest() {
  lastTrace = [...crypto.getRandomValues(new Uint8Array(16))].map((n) => n.toString(16).padStart(2, "0")).join("")
  return `00-${lastTrace}-${lastTrace.slice(0, 16)}-01`
}

export function recordAPI(method: string, path: string, traceparent: string, status: number, cause?: unknown) {
  if (cause && typeof cause === "object") reported.add(cause)
  lastTrace = traceparent.slice(3, 35)
  breadcrumb({ kind: "api", name: `${method} ${path.split("?")[0]}`, trace_id: traceparent.slice(3, 35), status })
  if (!status || status >= 500) reportError(new Error(`API ${method} ${path.split("?")[0]} (${status})`), traceparent)
}

export function reportError(error: unknown, traceparent = lastTrace ? `00-${lastTrace}-${lastTrace.slice(0, 16)}-01` : traceRequest()) {
  if (error instanceof Error && (error.name === "ApiError" || reported.has(error))) return
  const { route, search, ui_build_id } = page()
  const name = (error instanceof Error ? error.name : "Error").slice(0, 80)
  const message = (error instanceof Error ? error.message : "Unhandled rejection").slice(0, 1024)
  const now = Date.now(), key = `${name}:${message}:${route}`
  for (const [key, at] of seen) if (now - at >= 10_000) seen.delete(key)
  if (seen.has(key)) return
  if (now - minute >= 60_000) { minute = now; count = 0 }
  if (count >= 20) return
  count++
  seen.set(key, now)
  pending.push({ name, message, stack: error instanceof Error ? error.stack?.slice(0, 2048) : "", traceparent, route, ui_build_id,
    search: Object.fromEntries(Object.entries(search).slice(0, 10).filter(([key]) => !/pass|secret|token|auth|cookie|prompt|text|body|query|search|^q$|return_to/i.test(key)).map(([key, value]) => [key.slice(0, 64), String(value).slice(0, 128)])),
    viewport: [innerWidth, innerHeight], breadcrumbs: crumbs.slice(),
  })
  timer ??= setTimeout(() => flushTelemetry(), 200)
}

export function flushTelemetry(beacon = false) {
  clearTimeout(timer)
  timer = undefined
  while (pending.length) {
    const events = pending.splice(0, 5)
    let body = JSON.stringify({ events })
    while (new Blob([body]).size > (64 << 10) && events.length > 1) {
      pending.unshift(events.pop()!)
      body = JSON.stringify({ events })
    }
    if (beacon && navigator.sendBeacon?.("/api/telemetry", new Blob([body], { type: "application/json" }))) continue
    void fetch("/api/telemetry", { method: "POST", headers: { "Content-Type": "application/json" }, body, keepalive: beacon }).catch(() => {})
  }
}

export function installTelemetry(context: () => PageContext) {
  page = context
  window.addEventListener("error", (event) => reportError(event.error))
  window.addEventListener("unhandledrejection", (event) => reportError(event.reason))
  window.addEventListener("pagehide", () => flushTelemetry(true))
  document.addEventListener("click", (event) => {
    const control = event.target instanceof Element ? event.target.closest("button,a,[role=button],[role=menuitem]") : null
    if (control?.closest('[aria-label="Conversation"],[data-slot="agent-message"],[data-slot="user-bubble"]')) return
    const label = control?.getAttribute("aria-labelledby")
    const name = control?.getAttribute("aria-label") || (label ? label.split(/\s+/).map((id) => document.getElementById(id)?.textContent).join(" ") : control?.textContent)
    if (name) breadcrumb({ kind: "click", name: name.trim().slice(0, 80) })
  }, { capture: true, passive: true })
}
