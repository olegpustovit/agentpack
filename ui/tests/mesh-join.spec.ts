import { expect, test } from "@playwright/test"
import { mockAPI } from "./helpers"

test("a joining gateway shows approval, account sync, timeout and readiness", async ({ page }) => {
  await page.clock.install()
  await mockAPI(page, { authenticated: false })
  let phase = "waiting_for_approval"
  await page.route("**/api/status", (route) => route.fulfill({
    json: { claimed: true, joining: phase !== "ready", join_phase: phase, node_name: "atlas", authenticated: false, email: "", legacy: false },
  }))
  await page.goto("/login")
  await expect(page.getByRole("heading", { name: "Waiting for approval" })).toBeVisible()
  await expect(page.getByText("atlas · this machine")).toBeVisible()
  await expect(page.getByText(/^Last checked/)).toBeVisible()
  await expect(page.getByRole("textbox", { name: "Email", exact: true })).toHaveCount(0)
  await expect(page.getByRole("button", { name: "Claim gateway", exact: true })).toHaveCount(0)

  phase = "waiting_for_accounts"
  await page.clock.runFor(1_100)
  await expect(page.getByRole("heading", { name: "Waiting for accounts" })).toBeVisible()

  await page.clock.fastForward(60_100)
  await expect(page.getByRole("heading", { name: "Join check timed out" })).toBeVisible()
  phase = "ready"
  await page.getByRole("button", { name: "Retry", exact: true }).click()

  await expect(page.getByRole("heading", { name: "Ready to sign in" })).toBeVisible()
  await expect(page.getByRole("textbox", { name: "Email", exact: true })).toBeVisible()
  await expect(page.getByRole("button", { name: "Claim gateway", exact: true })).toHaveCount(0)
})
