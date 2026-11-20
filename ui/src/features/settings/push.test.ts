import { describe, expect, it } from "vitest"
import { decodeKey, pushBlocker } from "./push"

const ready = { secure: true, serviceWorker: true, pushManager: true, notification: true, permission: "default" as const, ios: false }

describe("push requirements", () => {
  it("names the missing requirement", () => {
    expect(pushBlocker(ready)).toBeUndefined()
    expect(pushBlocker({ ...ready, secure: false, serviceWorker: false })).toBe("insecure")
    expect(pushBlocker({ ...ready, pushManager: false, ios: true })).toBe("install")
    expect(pushBlocker({ ...ready, pushManager: false })).toBe("unsupported")
    expect(pushBlocker({ ...ready, permission: "denied" })).toBe("denied")
  })

  it("decodes unpadded base64url VAPID keys", () => {
    expect([...decodeKey("-_8")]).toEqual([0xfb, 0xff])
    expect(decodeKey("BAAA").length).toBe(3)
  })
})
