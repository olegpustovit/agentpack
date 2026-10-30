import { expect, test } from "@playwright/test"

const identity = { node: { id: "n1", name: "host-a", os: "linux", version: "0" }, auth: "password", tailscale_user: "" }

test.beforeEach(async ({ page }) => {
  await page.route("**/api/status", (route) =>
    route.fulfill({ json: { setup_required: false, authenticated: true } }),
  )
  await page.route("**/api/identity", (route) => route.fulfill({ json: identity }))
  await page.route("**/api/nodes", (route) => route.fulfill({ json: [] }))
})

test("deep link survives reload", async ({ page }) => {
  await page.goto("/library/package/github.com/a/b?type=mcp&q=foo")
  await expect(page.getByText("package")).toBeVisible()
  await page.reload()
  await expect(page).toHaveURL(/\/library\/package\/github\.com\/a\/b\?type=mcp&q=foo$/)
  await expect(page.getByText("package")).toBeVisible()
})

test("unauthenticated deep link redirects to login", async ({ page }) => {
  await page.route("**/api/status", (route) =>
    route.fulfill({ json: { setup_required: false, authenticated: false } }),
  )
  await page.goto("/sessions")
  await expect(page).toHaveURL(/\/login$/)
})
