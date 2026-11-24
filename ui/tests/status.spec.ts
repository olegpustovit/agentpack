import { expect, test } from "@playwright/test"
import { mockAPI } from "./helpers"

test.beforeEach(async ({ page }) => {
  await mockAPI(page)
  await page.setViewportSize({ width: 1280, height: 800 })
})

test("the toolbar reports when the gateway is unreachable", async ({ page }) => {
  await page.route("**/api/nodes", (route) => route.abort())
  await page.goto("/sessions")
  await expect(page.locator('[data-slot="toolbar"]').getByRole("button", { name: "Gateway unreachable" })).toBeVisible()
})

test("the connection popover lists machines, links to topology, and refreshes connectivity", async ({ page }) => {
  let online = false
  await page.route("**/api/nodes", (route) =>
    route.fulfill({ json: [{ id: "n2", name: "oracle-vm", online }] }),
  )
  await page.goto("/sessions")
  const connection = page.locator('[data-slot="toolbar"]').getByRole("button", { name: "1 machine offline" })
  await connection.click()
  const popover = page.getByRole("dialog")
  await expect(popover.getByText("Gateway machine")).toBeVisible()
  await expect(popover.getByText("oracle-vm")).toBeVisible()
  const topology = popover.getByRole("link", { name: "View topology" })
  await expect(topology).toHaveAttribute("href", "/machines/topology")

  online = true
  await popover.getByRole("button", { name: "Refresh" }).click()
  await expect(connection).toHaveCount(0)
  await expect(popover).toBeHidden()
})
