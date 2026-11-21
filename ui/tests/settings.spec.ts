import { expect, test, type Page, type Route } from "@playwright/test"
import { mockAPI } from "./helpers"

const identity = {
  node: { id: "n1", name: "host-a", os: "linux", version: "0.9.0" },
  auth: "ada@example.com",
  tailscale_user: "ada@example.com",
}
const nodes = [
  { id: "n2", name: "oracle-vm", online: true },
  { id: "n3", name: "lab-pi", online: false },
]
const replication = {
  peers: [{ node: "n2", mesh: true }],
  contacts: { n2: { at: "2026-10-30T10:00:00Z" } },
}

// Requests that change gateway state, in arrival order.
let calls: { request: string; body: unknown }[] = []

// Answers a gateway request and records anything that is not a GET.
function answer(route: Route, json: unknown = {}, status = 200) {
  const request = route.request()
  if (request.method() !== "GET") {
    calls.push({ request: `${request.method()} ${new URL(request.url()).pathname}`, body: request.postDataJSON() })
  }
  return status === 204 ? route.fulfill({ status }) : route.fulfill({ status, json })
}

// A stateful gateway: sharing and sign-in survive across requests, as they do on the real server.
async function mockGateway(page: Page, options: { signedIn?: boolean; unclaimed?: boolean; legacy?: boolean } = {}) {
  await mockAPI(page, { authenticated: options.signedIn ?? true, unclaimed: options.unclaimed ?? false })
  let signedIn = options.signedIn ?? true
  let sharing = true
  let claimed = !(options.unclaimed ?? false)
  await page.route("**/api/status", (route) =>
    answer(route, { claimed, legacy: options.legacy ?? false, email: signedIn ? "ada@example.com" : "", authenticated: signedIn }),
  )
  await page.route("**/api/config", (route) => claimed ? answer(route, { ui_build_id: "test-build" }) : route.fulfill({ status: 403, body: "unclaimed" }))
  await page.route("**/api/login", (route) => {
    signedIn = true
    return answer(route, { authenticated: true })
  })
  await page.route("**/api/claim", (route) => {
    signedIn = true
    claimed = true
    return answer(route, { authenticated: true })
  })
  await page.route(/\/api\/logout(?:-all)?$/, (route) => {
    signedIn = false
    return answer(route, undefined, 204)
  })
  await page.route("**/api/identity", (route) => answer(route, identity))
  await page.route("**/api/nodes*", (route) => answer(route, nodes))
  await page.route("**/api/events", (route) => route.abort())
  await page.route("**/api/settings", (route) =>
    answer(route, { running: true, platform: "linux", sharing_enabled: sharing, allowed_peers: ["tag:agents"] }),
  )
  await page.route("**/api/settings/sharing", (route) => {
    sharing = route.request().postDataJSON().enabled
    return answer(route, { sharing_enabled: sharing })
  })
  await page.route("**/api/library/replication/peers", (route) =>
    answer(route, route.request().method() === "GET" ? replication : { enrolled: true }),
  )
  await page.route("**/api/library/replication/sync", (route) => answer(route, { synced: true }))
  await page.route("**/api/library/replication/peers/*", (route) => answer(route, { removed: true }))
  await page.route("**/api/node/stop-agents", (route) => answer(route, { stopped: true, skipped: 1 }))
  await page.route("**/api/node/stop", (route) => answer(route, { stopping: true }))
}

async function signIn(page: Page) {
  await page.getByLabel("Email", { exact: true }).fill("ada@example.com")
  await page.getByLabel("Password", { exact: true }).fill("correct horse battery")
  await page.getByRole("button", { name: "Sign in" }).click()
}

test.beforeEach(async ({ page }) => {
  calls = []
  await page.setViewportSize({ width: 1280, height: 800 })
})

test("the settings index opens General", async ({ page }) => {
  await mockGateway(page)
  await page.goto("/settings")
  await expect(page).toHaveURL(/\/settings\/general$/)
})

test("general shows gateway state, platform, version and hostname", async ({ page }) => {
  await mockGateway(page)
  await page.goto("/settings/general")
  await expect(page.getByText("Running", { exact: true })).toBeVisible()
  await expect(page.getByText("linux", { exact: true })).toBeVisible()
  await expect(page.getByText("0.9.0", { exact: true })).toBeVisible()
  await expect(page.getByRole("main").or(page.locator("body")).getByText("host-a", { exact: true }).last()).toBeVisible()
})

test("the sharing switch sends the new state and says when it applies", async ({ page }) => {
  await mockGateway(page)
  await page.goto("/settings/sharing")
  const sharing = page.getByRole("switch", { name: "Tailscale sharing" })
  await expect(sharing).toBeChecked()
  await expect(page.getByText("tag:agents")).toBeVisible()

  await sharing.click()

  await expect.poll(() => calls).toEqual([{ request: "POST /api/settings/sharing", body: { enabled: false } }])
  await expect(page.getByText("Disabled. Local CLI sessions still work.")).toBeVisible()
  await expect(
    page.getByText("Sharing change applies within five seconds. Local CLI sessions keep running."),
  ).toBeVisible()
})

test("a catch-all proxy suggests nothing; the owner types the address and it is checked", async ({ page }) => {
  await mockGateway(page)
  await page.route("**/api/settings", (route) => answer(route, { running: true, platform: "linux", sharing_enabled: true, public: { enabled: false, catch_all: true } }))
  await page.route("**/api/settings/public", (route) => {
    calls.push({ request: "POST /api/settings/public", body: route.request().postDataJSON() })
    return route.fulfill({ contentType: "application/x-ndjson", body: JSON.stringify({ check: "reachable", ok: false, detail: "couldn't reach mine.example" }) + "\n" + JSON.stringify({ public: { enabled: false, catch_all: true } }) + "\n" })
  })
  await page.goto("/settings/sharing")
  const toggle = page.getByRole("switch", { name: "Public access" })
  await expect(page.getByText("Detected")).toBeHidden()
  await expect(toggle).toBeDisabled()
  await page.getByRole("textbox", { name: "Public address" }).fill("https://mine.example/")
  await toggle.click()
  await expect.poll(() => calls).toEqual([{ request: "POST /api/settings/public", body: { enabled: true, origin: "https://mine.example" } }])
  await expect(page.getByRole("list", { name: "Public access checks" }).getByLabel("Failed")).toHaveCount(1)
})

test("public access turns on the detected address after its checks stream in", async ({ page }) => {
  await mockGateway(page)
  const origin = "https://agentpack.example.com"
  await page.route("**/api/settings", (route) =>
    answer(route, { running: true, platform: "linux", sharing_enabled: true, public: { enabled: false, candidate: origin } }),
  )
  await page.route("**/api/settings/public", (route) => {
    calls.push({ request: "POST /api/settings/public", body: route.request().postDataJSON() })
    const checks = ["reachable", "sign_in", "cookies"].map((check) => ({ check, ok: true, detail: `${check} ok` }))
    const lines = [...checks, { check: "mcp", ok: true, detail: "OAuth metadata answers" }, { public: { enabled: true, origin } }]
    return route.fulfill({ contentType: "application/x-ndjson", body: lines.map((line) => JSON.stringify(line)).join("\n") + "\n" })
  })
  await page.goto("/settings/sharing")
  await expect(page.getByText("agentpack.example.com", { exact: true })).toBeVisible()

  await page.getByRole("switch", { name: "Public access" }).click()

  await expect.poll(() => calls).toEqual([{ request: "POST /api/settings/public", body: { enabled: true, origin } }])
  const checks = page.getByRole("list", { name: "Public access checks" })
  await expect(checks.getByRole("listitem")).toHaveCount(4)
  await expect(checks.getByLabel("Passed")).toHaveCount(4)
  await expect(page.getByText(`On · ${origin}`)).toBeVisible()
  await expect(page.getByRole("switch", { name: "Public access" })).toBeChecked()
  await expect(page.getByText(`${origin}/mcp`)).toBeVisible()
})

test("replication rows show enrollment, enroll only online peers and sync one", async ({ page }) => {
  await mockGateway(page)
  await page.goto("/settings/replication")
  await expect(page.getByText("Enrolled · online")).toBeVisible()
  await expect(page.getByText("Offline · enrollment unavailable")).toBeVisible()
  await expect(page.getByRole("button", { name: "Enroll" })).toBeDisabled()

  await page.getByRole("button", { name: "Sync now" }).click()

  await expect.poll(() => calls).toEqual([{ request: "POST /api/library/replication/sync", body: { peer: "n2" } }])
})

test("appearance sets the theme and keeps it across a reload", async ({ page }) => {
  await mockGateway(page)
  await page.goto("/settings/appearance")
  await page.getByRole("button", { name: "Dark" }).click()
  await expect(page.locator("html")).toHaveAttribute("data-theme", "dark")

  await page.reload()
  await expect(page.locator("html")).toHaveAttribute("data-theme", "dark")

  await page.getByRole("button", { name: "System" }).click()
  await expect(page.locator("html")).not.toHaveAttribute("data-theme")
})

test("account shows the signed-in identity and the attribution sentence", async ({ page }) => {
  await mockGateway(page)
  await page.goto("/settings/account")
  await expect(page.getByText("ada@example.com, on host-a", { exact: true })).toBeVisible()
  await expect(
    page.getByText(
      "Sessions are attributed to your account. Signing out leaves existing agents running.",
    ),
  ).toBeVisible()
})

test("stop all agents asks first and sends nothing until confirmed", async ({ page }) => {
  await mockGateway(page)
  await page.goto("/settings/account")

  await page.getByRole("button", { name: "Stop all agents" }).click()
  const dialog = page.getByRole("alertdialog")
  await expect(dialog).toContainText("Stop all agents on the gateway machine?")
  expect(calls).toEqual([])

  await dialog.getByRole("button", { name: "Stop all agents" }).click()
  await expect.poll(() => calls).toEqual([{ request: "POST /api/node/stop-agents", body: null }])
  await expect(page.getByText("Stopped local agents; 1 ACP session owned by another device remains running.")).toBeVisible()
})

test("stop gateway asks first, then shows the gateway as stopped", async ({ page }) => {
  await mockGateway(page)
  await page.goto("/settings/account")

  await page.getByRole("button", { name: "Stop gateway" }).click()
  const dialog = page.getByRole("alertdialog")
  await expect(dialog).toContainText("Stop this gateway?")
  expect(calls).toEqual([])

  await dialog.getByRole("button", { name: "Stop gateway" }).click()
  await expect.poll(() => calls).toEqual([{ request: "POST /api/node/stop", body: null }])
  await expect(page.getByRole("button", { name: "Stop gateway" })).toBeDisabled()
})

test("sign out calls the logout endpoint and returns to the login card", async ({ page }) => {
  await mockGateway(page)
  await page.goto("/settings/account")

  await page.getByRole("button", { name: "Sign out", exact: true }).click()

  await expect(page).toHaveURL(/\/login$/)
  await expect.poll(() => calls).toEqual([{ request: "POST /api/logout", body: null }])
})

test("login with a return_to that leaves the gateway ignores it", async ({ page }) => {
  let external = false
  await page.route("https://evil.example/**", (route) => {
    external = true
    return route.fulfill({ body: "evil" })
  })
  await mockGateway(page, { signedIn: false })
  await page.goto("/login?return_to=//evil.example")

  await signIn(page)

  await expect(page).toHaveURL(/\/sessions$/)
  expect(external).toBe(false)
})

test("login with a consent return_to navigates there in full", async ({ page }) => {
  await mockGateway(page, { signedIn: false })
  let fullNavigation = false
  await page.route(/\/oauth\/authorize\?x=1$/, (route) => {
    fullNavigation = route.request().isNavigationRequest()
    return route.fulfill({ contentType: "text/html", body: "<p>Consent</p>" })
  })
  await page.goto(`/login?return_to=${encodeURIComponent("/oauth/authorize?x=1")}`)

  await signIn(page)

  await expect(page.getByText("Consent")).toBeVisible()
  await expect(page).toHaveURL(/\/oauth\/authorize\?x=1$/)
  expect(fullNavigation).toBe(true)
})

test("a wrong password says so under the field and stays on the card", async ({ page }) => {
  await mockGateway(page, { signedIn: false })
  await page.route("**/api/login", (route) => route.fulfill({ status: 401, body: "invalid password" }))
  await page.goto("/login")

  await signIn(page)

  await expect(page.getByRole("alert")).toContainText("invalid password")
  await expect(page).toHaveURL(/\/login$/)
})

test("first run claims the owner account and signs in", async ({ page }) => {
  await mockGateway(page, { signedIn: false, unclaimed: true })
  if (process.env.CAPTURE) await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto("/login")
  await expect(page.getByRole("heading", { name: "Claim your gateway" })).toBeVisible()

  await page.getByLabel("Email", { exact: true }).fill("ada@example.com")
  await page.getByLabel("Password", { exact: true }).fill("correct horse battery")
  await page.getByLabel("Confirm password", { exact: true }).fill("correct horse battery")
  if (process.env.CAPTURE) {
    await page.screenshot({ path: test.info().outputPath("desktop.png"), animations: "disabled" })
    await page.setViewportSize({ width: 390, height: 844 })
    await page.emulateMedia({ colorScheme: "dark" })
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    await page.screenshot({ path: test.info().outputPath("mobile.png"), animations: "disabled" })
  }
  await page.getByRole("button", { name: "Claim gateway" }).click()

  await expect.poll(() => calls).toEqual([
    { request: "POST /api/claim", body: { email: "ada@example.com", password: "correct horse battery" } },
  ])
  await expect(page).toHaveURL(/\/sessions$/)
  await page.goto("/settings/account")
  if (process.env.CAPTURE) {
    await expect(page.getByRole("button", { name: "Sign out", exact: true })).toBeVisible()
    await page.screenshot({ path: test.info().outputPath("account-mobile.png"), animations: "disabled" })
    await page.setViewportSize({ width: 1440, height: 900 })
    await page.emulateMedia({ colorScheme: "light" })
    await page.screenshot({ path: test.info().outputPath("account-desktop.png"), animations: "disabled" })
  }
  await page.getByRole("button", { name: "Sign out", exact: true }).click()
  await expect(page).toHaveURL(/\/login$/)
  await signIn(page)
  await expect(page).toHaveURL(/\/sessions$/)
  expect(calls.map((call) => call.request)).toEqual(["POST /api/claim", "POST /api/logout", "POST /api/login"])
})

test("a failed claim keeps the consent return_to", async ({ page }) => {
  await mockGateway(page, { signedIn: false, unclaimed: true })
  await page.route("**/api/claim", (route) => route.fulfill({ status: 401, body: "gateway already claimed" }))
  await page.goto(`/login?return_to=${encodeURIComponent("/oauth/authorize?x=1")}`)

  await page.getByLabel("Email", { exact: true }).fill("ada@example.com")
  await page.getByLabel("Password", { exact: true }).fill("correct horse battery")
  await page.getByLabel("Confirm password", { exact: true }).fill("correct horse battery")
  await page.getByRole("button", { name: "Claim gateway" }).click()

  await expect(page.getByRole("alert")).toContainText("gateway already claimed")
  await expect(page).toHaveURL(/return_to=/)
})


test("sign out everywhere confirms before revoking account logins", async ({ page }) => {
  await mockGateway(page)
  await page.goto("/settings/account")
  await page.getByRole("button", { name: "Sign out everywhere…", exact: true }).click()
  const dialog = page.getByRole("alertdialog")
  await expect(dialog).toContainText("Sign out everywhere?")
  expect(calls).toEqual([])
  await dialog.getByRole("button", { name: "Sign out everywhere", exact: true }).click()
  await expect(page).toHaveURL(/\/login$/)
  expect(calls).toEqual([{ request: "POST /api/logout-all", body: null }])
})

test("legacy claiming asks for the current password and keeps it", async ({ page }) => {
  await mockGateway(page, { signedIn: false, unclaimed: true, legacy: true })
  await page.route("**/api/claim", (route) => route.fulfill({ status: 401, body: "invalid current password" }))
  await page.goto("/login")
  await page.getByLabel("Email", { exact: true }).fill("ada@example.com")
  await page.getByLabel("Current gateway password", { exact: true }).fill("correct horse battery")
  await page.getByLabel("Password", { exact: true }).fill("correct horse battery")
  await page.getByLabel("Confirm password", { exact: true }).fill("correct horse battery")
  await page.getByRole("button", { name: "Claim gateway", exact: true }).click()
  await expect(page.getByRole("alert")).toContainText("invalid current password")
  await expect(page).toHaveURL(/\/login$/)
})
