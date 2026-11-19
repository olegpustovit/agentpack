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
