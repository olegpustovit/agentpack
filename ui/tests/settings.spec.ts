import { expect, test, type Page, type Route } from "@playwright/test"

const identity = {
  node: { id: "n1", name: "host-a", os: "linux", version: "0.9.0" },
  auth: "root",
  tailscale_user: "ada@example.com",
}
const nodes = [
  { id: "n2", name: "oracle-vm", online: true },
  { id: "n3", name: "lab-pi", online: false },
]
const replication = {
  peers: [{ node: "n2", receipt_count: 3, digests: [] }],
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
async function mockGateway(page: Page, options: { signedIn?: boolean; setup?: boolean } = {}) {
  let signedIn = options.signedIn ?? true
  let sharing = true
  await page.route("**/api/status", (route) =>
    answer(route, { setup_required: options.setup ?? false, authenticated: signedIn }),
  )
  await page.route("**/api/login", (route) => {
    signedIn = true
    return answer(route, { authenticated: true })
  })
  await page.route("**/api/setup", (route) => {
    signedIn = true
    return answer(route, { authenticated: true })
  })
  await page.route("**/api/logout", (route) => {
    signedIn = false
    return answer(route, undefined, 204)
  })
  await page.route("**/api/identity", (route) => answer(route, identity))
  await page.route("**/api/nodes", (route) => answer(route, nodes))
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
  await page.route("**/api/node/stop-agents", (route) => answer(route, { stopped: true }))
  await page.route("**/api/node/stop", (route) => answer(route, { stopping: true }))
}

async function signIn(page: Page) {
  await page.getByLabel("Root account password").fill("correct horse battery")
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

test("replication rows show receipts, enroll only online peers and sync one", async ({ page }) => {
  await mockGateway(page)
  await page.goto("/settings/replication")
  await expect(page.getByText("3 verified object receipts · online")).toBeVisible()
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
  await expect(page.getByText("ada@example.com")).toBeVisible()
  await expect(
    page.getByText(
      "Sessions are attributed per machine and Tailscale user. Everyone using this root password appears as root.",
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

  await page.getByRole("button", { name: "Sign out" }).click()

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

test("first run asks for the setup token and creates the root account", async ({ page }) => {
  await mockGateway(page, { signedIn: false, setup: true })
  await page.goto("/login")
  await expect(page.getByRole("heading", { name: "Set up your gateway" })).toBeVisible()

  await page.getByLabel("Setup token").fill("abc123")
  await page.getByLabel("Root account password").fill("correct horse battery")
  await page.getByRole("button", { name: "Create root account" }).click()

  await expect.poll(() => calls).toEqual([
    { request: "POST /api/setup", body: { password: "correct horse battery", token: "abc123" } },
  ])
  await expect(page).toHaveURL(/\/sessions$/)
})

test("a wrong setup token keeps the consent return_to", async ({ page }) => {
  await mockGateway(page, { signedIn: false, setup: true })
  await page.route("**/api/setup", (route) => route.fulfill({ status: 401, body: "invalid setup token" }))
  await page.goto(`/login?return_to=${encodeURIComponent("/oauth/authorize?x=1")}`)

  await page.getByLabel("Setup token").fill("wrong")
  await page.getByLabel("Root account password").fill("correct horse battery")
  await page.getByRole("button", { name: "Create root account" }).click()

  await expect(page.getByRole("alert")).toContainText("invalid setup token")
  await expect(page).toHaveURL(/return_to=/)
})
