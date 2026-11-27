import { expect, test, type Page } from "@playwright/test"
import { mockAPI, session } from "./helpers"

async function signIn(page: Page) {
  await page.getByLabel("Email", { exact: true }).fill("owner@example.com")
  await page.getByLabel("Password", { exact: true }).fill("a-long-test-password")
  await page.getByRole("button", { name: "Sign in", exact: true }).click()
}

test("sign-in returns to a saved session link with its search and fragment", async ({ page }) => {
  const requests = await mockAPI(page, { authenticated: false })
  const destination = `/sessions/local/${session.id}?q=limits#reply`
  await page.goto(destination)
  await expect(page.getByText("After sign-in", { exact: true })).toBeVisible()
  await expect(page.getByText(`Session ${session.id} on this machine`, { exact: true })).toBeVisible()
  expect(new URL(page.url()).searchParams.get("return_to")).toBe(destination)
  expect(requests.some(({ path }) => path === "/api/identity" || path === "/api/sessions")).toBe(false)

  await signIn(page)

  await expect(page).toHaveURL(new URL(destination, page.url()).href)
})

test("a 401 during an expired browser login keeps the open page as the destination", async ({ page }) => {
  await mockAPI(page)
  let signedIn = true
  let expired = true
  await page.route("**/api/status", (route) => route.fulfill({
    json: { claimed: true, legacy: false, authenticated: signedIn, email: signedIn ? "owner@example.com" : "" },
  }))
  await page.route("**/api/identity", (route) => {
    if (!expired) return route.fallback()
    signedIn = false
    return route.fulfill({ status: 401, body: "login required" })
  })
  await page.route("**/api/login", (route) => {
    signedIn = true
    expired = false
    return route.fallback()
  })

  const destination = "/settings/general#identity"
  await page.goto(destination)
  await expect(page.getByRole("heading", { name: "Sign in to Agentpack" })).toBeVisible()
  expect(new URL(page.url()).searchParams.get("return_to")).toBe(destination)
  await signIn(page)
  await expect(page).toHaveURL(new URL(destination, page.url()).href)
})

test("Retry repeats the gateway status load and keeps the intended destination", async ({ page }) => {
  await mockAPI(page, { authenticated: false })
  let unavailable = true
  await page.route("**/api/status", (route) =>
    unavailable ? route.fulfill({ status: 503, body: "gateway restarting" }) : route.fallback(),
  )
  const destination = "/sessions/local/chat?q=limits"
  await page.goto(`/login?return_to=${encodeURIComponent(destination)}`)
  await expect(page.getByRole("heading", { name: "Couldn’t reach the gateway" })).toBeVisible()
  await expect(page.getByText("Destination Session chat on this machine")).toBeVisible()
  await expect(page.getByRole("link", { name: "Go to Sessions" })).toHaveAttribute("href", "/sessions")

  unavailable = false
  await page.getByRole("button", { name: "Retry", exact: true }).click()

  await expect(page.getByRole("heading", { name: "Sign in to Agentpack" })).toBeVisible()
  expect(new URL(page.url()).searchParams.get("return_to")).toBe(destination)
})

test("an unknown route offers three ways back into Agentpack", async ({ page }) => {
  await mockAPI(page)
  await page.goto("/nope")
  await expect(page.getByRole("heading", { level: 1, name: "No page at /nope" })).toBeVisible()
  const nav = page.getByRole("navigation", { name: "Go to a section" })
  await expect(nav.getByRole("link", { name: "Sessions" })).toHaveAttribute("href", "/sessions")
  await expect(nav.getByRole("link", { name: "Library" })).toHaveAttribute("href", "/library")
  await expect(nav.getByRole("link", { name: "Settings" })).toHaveAttribute("href", "/settings")
})
