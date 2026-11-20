// App shell cache. Hashed /assets/* never change, so they are served from cache first; the page shell
// paints from cache and refreshes behind it. API calls are never touched, so data always comes from the gateway.
const CACHE = "agentpack-shell-v1"

self.addEventListener("install", () => self.skipWaiting())
self.addEventListener("activate", (event) => {
  event.waitUntil(
    caches.keys()
      .then((keys) => Promise.all(keys.filter((key) => key !== CACHE).map((key) => caches.delete(key))))
      .then(() => self.clients.claim()),
  )
})

async function cached(request, key) {
  const cache = await caches.open(CACHE)
  const hit = await cache.match(key)
  const fresh = fetch(request).then((response) => {
    if (response.ok && response.type === "basic") void cache.put(key, response.clone())
    return response
  })
  if (hit) {
    fresh.catch(() => undefined)
    return hit
  }
  return fresh
}

self.addEventListener("fetch", (event) => {
  const { request } = event
  if (request.method !== "GET") return
  const url = new URL(request.url)
  if (url.origin !== location.origin || /^\/(api\/|oauth|mcp|healthz|\.well-known)/.test(url.pathname)) return
  // Every route is the same SPA shell; after a deploy the stale shell's missing chunk triggers the app's one reload.
  if (request.mode === "navigate") event.respondWith(cached(request, "/"))
  else if (url.pathname.startsWith("/assets/")) event.respondWith(caches.match(request).then((hit) => hit ?? cached(request, request)))
})

// Web Push (internal/network/push.go). Payloads carry labels and IDs only, never prompt or reply text.
self.addEventListener("push", (event) => {
  let data = {}
  try {
    data = event.data ? event.data.json() : {}
  } catch {
    data = {}
  }
  const actions = Array.isArray(data.actions) ? data.actions : []
  event.waitUntil(
    self.registration.showNotification(data.title || "agentpack", {
      body: data.body || "",
      tag: data.tag,
      renotify: Boolean(data.tag),
      icon: "/icon-192.png",
      data,
      actions: actions.map(({ action, title }) => ({ action, title })),
    }),
  )
})

self.addEventListener("notificationclick", (event) => {
  const data = event.notification.data || {}
  event.notification.close()
  const choice = (data.actions || []).find((item) => item.action === event.action)
  // Allow once / Deny answer in place; if the answer fails (already answered, signed out), open the session.
  event.waitUntil(choice && data.session && data.request_id ? answer(data, choice).catch(() => focusSession(data)) : focusSession(data))
})

async function answer(data, choice) {
  const url = new URL(`/api/sessions/${encodeURIComponent(data.session)}/conversation/permission`, location.origin)
  if (data.machine && data.machine !== "local") url.searchParams.set("node", data.machine)
  const response = await fetch(url, {
    method: "POST",
    credentials: "same-origin",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ request_id: data.request_id, option_id: choice.option_id }),
  })
  if (!response.ok) throw new Error(`HTTP ${response.status}`)
}

async function focusSession(data) {
  const target = new URL(data.url || "/sessions", location.origin)
  if (target.origin !== location.origin) return
  const windows = await self.clients.matchAll({ type: "window", includeUncontrolled: true })
  const open = windows.find((client) => new URL(client.url).origin === location.origin)
  if (!open) return self.clients.openWindow(target.href)
  await open.focus()
  return open.navigate(target.href).catch(() => self.clients.openWindow(target.href))
}

// The browser rotated the subscription: hand the new endpoint to the gateway under the same account.
// It subscribes with the mesh's current key, which the gateway records for the new endpoint.
self.addEventListener("pushsubscriptionchange", (event) => {
  const options = event.oldSubscription && event.oldSubscription.options
  if (!options) return
  event.waitUntil(
    fetch("/api/push/key", { credentials: "same-origin" })
      .then((response) => (response.ok ? response.json() : {}))
      .catch(() => ({}))
      .then(({ public_key }) => self.registration.pushManager.subscribe(public_key ? { userVisibleOnly: true, applicationServerKey: public_key } : options))
      .then((subscription) =>
        fetch("/api/push/subscription", {
          method: "POST",
          credentials: "same-origin",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(subscription.toJSON()),
        }),
      ),
  )
})
