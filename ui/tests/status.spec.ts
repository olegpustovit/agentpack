import { expect, test } from "@playwright/test"

const identity = { node: { id: "n1", name: "host-a", os: "linux", version: "0" }, auth: "password", tailscale_user: "" }

test.beforeEach(async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 800 })
  await page.route("**/api/status", (route) =>
    route.fulfill({ json: { setup_required: false, authenticated: true } }),
  )
  await page.route("**/api/identity", (route) => route.fulfill({ json: identity }))
})

test("connection item reports connected machines and offline count", async ({ page }) => {
  await page.route("**/api/nodes", (route) =>
    route.fulfill({ json: [{ id: "n2", name: "oracle-vm", online: false }] }),
  )
  await page.goto("/sessions")
  const status = page.getByRole("contentinfo")
  await expect(status.getByRole("button", { name: "Connected · 2 machines · 1 offline" })).toBeVisible()
})

test("connection item says when the gateway is unreachable", async ({ page }) => {
  await page.route("**/api/nodes", (route) => route.abort())
  await page.goto("/sessions")
  await expect(page.getByRole("contentinfo").getByRole("button", { name: "Gateway unreachable" })).toBeVisible()
})

test("connection popover lists machines and offers refresh and topology", async ({ page }) => {
  await page.route("**/api/nodes", (route) =>
    route.fulfill({ json: [{ id: "n2", name: "oracle-vm", online: false }] }),
  )
  await page.goto("/sessions")
  await page.getByRole("button", { name: "Connected · 2 machines · 1 offline" }).click()
  const popover = page.getByRole("dialog")
  await expect(popover.getByText("host-a")).toBeVisible()
  await expect(popover.getByText("oracle-vm")).toBeVisible()
  await expect(popover.getByRole("button", { name: "Refresh" })).toBeVisible()
  await expect(popover.getByRole("link", { name: "View topology" })).toHaveAttribute("href", "/machines/topology")
})

test("limits and settings show no session text in the status bar", async ({ page }) => {
  await page.route("**/api/nodes", (route) => route.fulfill({ json: [] }))
  for (const path of ["/limits", "/settings"]) {
    await page.goto(path)
    await expect(page.getByRole("contentinfo").getByRole("button", { name: "Connected · 1 machine" })).toBeVisible()
    await expect(page.getByRole("contentinfo").getByText("Mouse:")).toHaveCount(0)
  }
})
