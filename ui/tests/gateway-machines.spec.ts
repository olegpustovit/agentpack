import { expect, test } from "@playwright/test"
import { mockAPI } from "./helpers"

// Machines, Limits, Settings and startup ports of gateway.spec.js.

const limitsPath = /\/api\/limits(\?.*)?$/
const nodesPath = /\/api\/nodes(\?.*)?$/
const settingsPath = /\/api\/settings(\?.*)?$/

test("an agent signs out from the machine page after a confirmation", async ({ page }) => {
  await mockAPI(page)
  let signedIn = true
  let loggedOut = 0
  await page.route("**/api/agents", (route) =>
    route.fulfill({ json: { agents: [{ harness: "codex", installed: true, signed_in: signedIn, label: signedIn ? "dev@example.com" : undefined, sign_out: signedIn, methods: [{ id: "chatgpt", label: "ChatGPT account" }] }] } }),
  )
  await page.route("**/api/agents/codex/logout", (route) => {
    loggedOut++
    signedIn = false
    return route.fulfill({ json: { signed_out: true } })
  })
  await page.goto("/machines/local")
  await page.getByRole("button", { name: "Sign out…" }).click()
  const confirm = page.getByRole("alertdialog")
  await expect(confirm).toContainText("Sign Codex out on Gateway machine?")
  await confirm.getByRole("button", { name: "Sign out" }).click()
  await expect(page.getByText("Not signed in")).toBeVisible()
  expect(loggedOut).toBe(1)
})

test("Limits shows replicated readings after machine discovery", async ({ page }) => {
  await mockAPI(page)
  await page.setViewportSize({ width: 1280, height: 800 })
  let releaseNodes!: () => void
  const nodesReady = new Promise<void>((resolve) => (releaseNodes = resolve))
  await page.route(nodesPath, async (route) => {
    await nodesReady
    await route.fulfill({ json: [{ id: "peer-1", name: "Kyiv grid", online: true }] })
  })
  await page.route(limitsPath, (route) => {
    return route.fulfill({
      json: {
        limits: [
          {
            node_id: "peer-1",
            harness: "codex",
            state: "available",
            windows: [{ id: "weekly", label: "Weekly", remaining_percent: 63 }],
          },
        ],
      },
    })
  })

  await page.goto("/")
  await page.getByRole("link", { name: "System", exact: true }).click()
  await page.getByRole("navigation", { name: "System" }).getByRole("link", { name: /^Limits/ }).click()
  await expect(page.getByText("No limits yet")).toBeVisible()
  releaseNodes()
  await expect(page.getByRole("meter", { name: "Codex left" })).toHaveAttribute("value", "63")
})

test("startup and machine discovery announce pending work", async ({ page }) => {
  await page.clock.install()
  await mockAPI(page)
  let releaseStatus!: () => void
  let releaseNodes!: () => void
  const statusReady = new Promise<void>((resolve) => (releaseStatus = resolve))
  const nodesReady = new Promise<void>((resolve) => (releaseNodes = resolve))
  await page.route(/\/api\/status(\?.*)?$/, async (route) => {
    await statusReady
    await route.fulfill({ json: { claimed: true, legacy: false, email: "owner@example.com", authenticated: true } })
  })
  await page.route(nodesPath, async (route) => {
    await nodesReady
    await route.fulfill({ json: [] })
  })

  const requestingStatus = page.waitForRequest(/\/api\/status(\?.*)?$/)
  await page.goto("/")
  await requestingStatus
  await page.clock.fastForward("00:02")
  await expect(page.getByText("Connecting to gateway…")).toBeVisible()
  releaseStatus()
  await page.clock.fastForward("00:01")
  await expect(page.getByText("Discovering machines…")).toBeVisible()

  releaseNodes()
  await expect(page.getByText("Discovering machines…")).toBeHidden()
})

test("settings errors finish loading and offer a working retry", async ({ page }) => {
  await mockAPI(page)
  let releaseSettings!: () => void
  let fail = true
  const ready = new Promise<void>((resolve) => (releaseSettings = resolve))
  await page.route(settingsPath, async (route) => {
    await ready
    if (fail) return route.fulfill({ status: 502, body: "Gateway is unavailable" })
    return route.fulfill({ json: { running: true, platform: "macOS", sharing_enabled: true, allowed_peers: [] } })
  })

  await page.goto("/settings/general")
  const skeleton = page.locator('[data-slot="skeleton"]')
  await expect(skeleton.first()).toBeVisible()

  releaseSettings()
  await expect(page.getByText("Couldn't load gateway details: Gateway is unavailable")).toBeVisible()
  await expect(skeleton).toHaveCount(0)

  fail = false
  await page.getByRole("button", { name: "Retry", exact: true }).click()
  await expect(page.getByText(/ · macOS$/)).toBeVisible()
  await expect(page.getByRole("button", { name: "Retry", exact: true })).toBeHidden()
})
