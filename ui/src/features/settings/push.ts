// Web Push for this device. The gateway (internal/network/push.go) stores one subscription per
// browser, bound to the signed-in account, and notifies it when a session needs you or finishes,
// even with every tab closed. public/sw.js renders the notification and answers permissions.
import { api } from "@/lib/api"

export type PushBlocker = "insecure" | "install" | "unsupported" | "denied"

type PushEnvironment = {
  secure: boolean
  serviceWorker: boolean
  pushManager: boolean
  notification: boolean
  permission: NotificationPermission | undefined
  ios: boolean
}

function environment(): PushEnvironment {
  return {
    secure: window.isSecureContext,
    serviceWorker: "serviceWorker" in navigator,
    pushManager: "PushManager" in window,
    notification: "Notification" in window,
    permission: "Notification" in window ? Notification.permission : undefined,
    // iPadOS reports a Mac user agent; touch points tell them apart.
    ios: /iPhone|iPad|iPod/.test(navigator.userAgent) || (navigator.userAgent.includes("Macintosh") && navigator.maxTouchPoints > 1),
  }
}

// What stops push on this device, or undefined when it can be turned on.
export function pushBlocker(env: PushEnvironment = environment()): PushBlocker | undefined {
  // Plain http://100.x has no service worker at all, so this check comes first.
  if (!env.secure) return "insecure"
  // iOS exposes PushManager only to an app added to the Home Screen.
  if (!env.serviceWorker || !env.pushManager || !env.notification) return env.ios ? "install" : "unsupported"
  if (env.permission === "denied") return "denied"
  return undefined
}

export const pushBlockerText: Record<PushBlocker, string> = {
  insecure: "Push needs an HTTPS address. Open this gateway through Public access or its Tailscale certificate (https://…ts.net).",
  install: "On iPhone and iPad, add agentpack to the Home Screen (Share › Add to Home Screen) and turn this on there.",
  unsupported: "This browser can't receive push notifications.",
  denied: "Notifications are blocked for this site. Allow them in the browser's site settings.",
}

// VAPID keys travel as unpadded base64url; PushManager wants the raw bytes.
export function decodeKey(value: string): Uint8Array<ArrayBuffer> {
  const base64 = value.replace(/-/g, "+").replace(/_/g, "/").padEnd(Math.ceil(value.length / 4) * 4, "=")
  return Uint8Array.from(atob(base64), (char) => char.charCodeAt(0))
}

async function registration(): Promise<ServiceWorkerRegistration> {
  // main.tsx registers /sw.js; register again in case that failed or has not run yet.
  return (await navigator.serviceWorker.getRegistration("/")) ?? navigator.serviceWorker.register("/sw.js")
}

export async function currentPushSubscription(): Promise<PushSubscription | null> {
  if (pushBlocker()) return null
  const found = await navigator.serviceWorker.getRegistration("/")
  return (await found?.pushManager.getSubscription()) ?? null
}

// Asks for permission when needed, then registers this browser with the gateway.
export async function subscribePush(): Promise<PushSubscription> {
  if ((await Notification.requestPermission()) !== "granted") throw new Error(pushBlockerText.denied)
  const { public_key } = await api<{ public_key: string }>("/api/push/key")
  const reg = await registration()
  let subscription = await reg.pushManager.getSubscription()
  // A subscription made for another gateway key cannot be reused.
  const existing = subscription?.options.applicationServerKey
  if (subscription && existing && !sameKey(new Uint8Array(existing), decodeKey(public_key))) {
    await subscription.unsubscribe()
    subscription = null
  }
  subscription ??= await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: decodeKey(public_key) })
  await api("/api/push/subscription", { method: "POST", body: subscription.toJSON() })
  return subscription
}

export async function unsubscribePush(): Promise<void> {
  const subscription = await currentPushSubscription()
  if (!subscription) return
  await api("/api/push/subscription", { method: "DELETE", body: { endpoint: subscription.endpoint } })
  await subscription.unsubscribe()
}

export async function testPush(subscription: PushSubscription): Promise<void> {
  await api("/api/push/test", { method: "POST", body: { endpoint: subscription.endpoint } })
}

function sameKey(a: Uint8Array, b: Uint8Array): boolean {
  return a.length === b.length && a.every((byte, index) => byte === b[index])
}
