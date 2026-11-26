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

test("on phones the settings index opens the System list and a pane opens full height with Back", async ({ page }) => {
  await mockGateway(page)
  await page.setViewportSize({ width: 390, height: 500 })
  await page.goto("/settings")
  await expect(page).toHaveURL(/\/system$/)
  await page.getByRole("navigation", { name: "System" }).getByRole("link", { name: "Appearance" }).click()
  await expect(page).toHaveURL(/\/settings\/appearance$/)
  await expect(page.getByRole("navigation", { name: "System" })).toHaveCount(0)
  for (const viewport of [{ width: 390, height: 500 }, { width: 320, height: 568 }]) {
    await page.setViewportSize(viewport)
    for (const name of ["System", "Light", "Dark"]) {
      const button = page.getByRole("group", { name: "Theme" }).getByRole("button", { name, exact: true })
      await button.focus()
      const bottom = (await button.boundingBox())!
      const tabs = (await page.locator('[data-slot="tab-bar"]').boundingBox())!
      expect(bottom.y + bottom.height).toBeLessThanOrEqual(tabs.y)
    }
  }
  await page.getByRole("link", { name: "Back to System" }).click()
  await expect(page).toHaveURL(/\/system$/)
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
  await expect(page.getByRole("link", { name: /Machine details/ })).toHaveAttribute("href", "/machines/local")
})

test("blocked browser delivery keeps event preferences editable", async ({ page }) => {
  await mockGateway(page)
  await page.addInitScript(() => {
    Object.defineProperty(Notification, "permission", { value: "denied", configurable: true })
  })
  await page.goto("/settings/notifications")
  await expect(page.getByText(/Notifications are blocked for this site/)).toHaveCount(1)
  await expect(page.getByText(/^Applies once delivery is on\./)).toBeVisible()
  const alert = page.getByRole("switch", { name: "A turn finishes" })
  await expect(alert).toBeChecked()
  await alert.click()
  await expect(alert).not.toBeChecked()
  await expect(page.getByRole("switch", { name: "Push to this device" })).toBeDisabled()
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
  await expect(page.getByText("Sharing change applies within five seconds. Local CLI sessions keep running.")).toBeVisible()
})

test("a catch-all proxy suggests nothing; the owner types the address and it is checked", async ({ page }) => {
  await mockGateway(page)
  await page.route("**/api/settings", (route) => answer(route, { running: true, platform: "linux", sharing_enabled: true, public: { enabled: false, catch_all: true } }))
  let completeProbe!: () => void
  const probe = new Promise<void>((resolve) => { completeProbe = resolve })
  await page.route("**/api/settings/public", async (route) => {
    calls.push({ request: "POST /api/settings/public", body: route.request().postDataJSON() })
    await probe
    return route.fulfill({ contentType: "application/x-ndjson", body: JSON.stringify({ check: "reachable", ok: false, detail: "couldn't reach mine.example" }) + "\n" + JSON.stringify({ public: { enabled: false, catch_all: true } }) + "\n" })
  })
  await page.goto("/settings/sharing")
  const toggle = page.getByRole("switch", { name: "Public access" })
  await expect(page.getByText("Detected")).toBeHidden()
  await expect(toggle).toBeDisabled()
  await page.getByRole("textbox", { name: "Public address" }).fill("https://mine.example/")
  await page.getByRole("button", { name: "Check address" }).click()
  await expect(page.getByText(/Checking mine.example/)).toBeVisible()
  await expect(toggle).not.toBeChecked()
  completeProbe()
  await expect.poll(() => calls).toEqual([{ request: "POST /api/settings/public", body: { enabled: true, check_only: true, origin: "https://mine.example" } }])
  await expect(page.getByRole("list", { name: "Public access checks" }).getByLabel("Failed")).toHaveCount(1)
  // The switch never claims On for an address that failed its checks.
  await expect(toggle).not.toBeChecked()
  await expect(page.getByRole("button", { name: "Enable access" })).toHaveCount(0)
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
    const outcome = route.request().postDataJSON().check_only ? { verified_origin: origin } : { public: { enabled: true, origin } }
    const lines = [...checks, { check: "mcp", ok: true, detail: "OAuth metadata answers" }, outcome]
    return route.fulfill({ contentType: "application/x-ndjson", body: lines.map((line) => JSON.stringify(line)).join("\n") + "\n" })
  })
  await page.goto("/settings/sharing")
  await expect(page.getByText("agentpack.example.com", { exact: true })).toBeVisible()

  await page.getByRole("button", { name: "Check address" }).click()

  const checks = page.getByRole("list", { name: "Public access checks" })
  await expect(checks.getByRole("listitem")).toHaveCount(4)
  await expect(checks.getByLabel("Passed")).toHaveCount(4)
  await expect(page.getByRole("switch", { name: "Public access" })).not.toBeChecked()
  await expect(page.getByText(/sign-in page and MCP endpoint at agentpack.example.com/)).toBeVisible()
  expect(calls).toEqual([{ request: "POST /api/settings/public", body: { enabled: true, check_only: true, origin } }])
  await page.getByRole("button", { name: "Enable access" }).click()
  await expect.poll(() => calls).toEqual([
    { request: "POST /api/settings/public", body: { enabled: true, check_only: true, origin } },
    { request: "POST /api/settings/public", body: { enabled: true, origin } },
  ])
  await expect(page.getByText(`On · ${origin}`)).toBeVisible()
  await expect(page.getByRole("switch", { name: "Public access" })).toBeChecked()
  await expect(page.getByText(`${origin}/mcp`)).toBeVisible()
})

test("replication rows show enrollment, enroll only online peers and sync one", async ({ page }) => {
  await mockGateway(page)
  await page.goto("/settings/replication")
  await expect(page.getByText("Enrolled · online")).toBeVisible()
  await expect(page.getByText(/Offline · enrollment unavailable/)).toBeVisible()
  await expect(page.getByRole("button", { name: "Enroll" })).toBeDisabled()

  await page.getByRole("button", { name: "Sync now" }).click()

  await expect.poll(() => calls).toEqual([{ request: "POST /api/library/replication/sync", body: { peer: "n2" } }])
})

test("replication lists what a peer receives and links removal to credential recovery", async ({ page }) => {
  await mockGateway(page)
  await page.route("**/api/nodes*", (route) => answer(route, nodes.map((node) => ({ ...node, online: true }))))
  await page.goto("/settings/replication")
  await page.getByRole("button", { name: "Enroll", exact: true }).click()
  const dialog = page.getByRole("alertdialog")
  await expect(dialog).toContainText("password hashes, chat history, limits, Library data and shared MCP logins")
  await dialog.getByRole("button", { name: "Cancel" }).click()
  await page.getByRole("button", { name: "oracle-vm actions" }).click()
  await page.getByRole("menuitem", { name: "Remove from replication…" }).click()
  await expect(dialog).toContainText("Previously replicated data remains")
  await expect(dialog.getByRole("link", { name: "Vault" })).toHaveAttribute("href", "/settings/connections")
  await dialog.getByRole("button", { name: "Remove", exact: true }).click()
  await expect(page.getByRole("link", { name: "Open Vault" })).toBeVisible()
})

test("appearance sets the theme and keeps it across a reload", async ({ page }) => {
  await mockGateway(page)
  await page.goto("/settings/appearance")
  await page.getByRole("button", { name: "Dark" }).click()
  await expect(page.locator("html")).toHaveAttribute("data-theme", "dark")
  await expect(page.getByText("Currently Dark")).toBeVisible()

  await page.reload()
  await expect(page.locator("html")).toHaveAttribute("data-theme", "dark")

  await page.getByRole("button", { name: "System" }).click()
  await expect(page.locator("html")).not.toHaveAttribute("data-theme")
  await page.emulateMedia({ colorScheme: "light" })
  await expect(page.getByText("Currently Light")).toBeVisible()
})

test("an old Accessibility link opens Appearance, where screen reader mode explains its browser scope", async ({ page }) => {
  await mockGateway(page)
  await page.goto("/settings/accessibility")
  await expect(page).toHaveURL(/\/settings\/appearance$/)
  await expect(page.getByRole("group", { name: "Theme" })).toBeVisible()
  const mode = page.getByRole("switch", { name: "Terminal screen reader mode" })
  await expect(mode).toHaveAccessibleDescription("Open terminals reconnect with this setting.")
  await mode.click()
  await page.reload()
  await expect(mode).toBeChecked()
})

test("account shows the signed-in identity and the attribution sentence", async ({ page }) => {
  await mockGateway(page)
  await page.goto("/settings/account")
  const account = page.getByRole("region", { name: "Account" })
  await expect(account.getByText("ada@example.com", { exact: true })).toHaveCount(2)
  await expect(account.getByText("Sessions you start are attributed to this account.")).toBeVisible()
  await expect(account.getByText("Signing out leaves running agents running.")).toBeVisible()
})

test("stop all agents asks first and sends nothing until confirmed", async ({ page }) => {
  await mockGateway(page)
  await page.goto("/settings/general")

  await page.getByRole("button", { name: "Stop agents…" }).click()
  const dialog = page.getByRole("alertdialog")
  await expect(dialog).toContainText("Stop all agents on host-a?")
  expect(calls).toEqual([])

  await dialog.getByRole("button", { name: "Stop all agents" }).click()
  await expect.poll(() => calls).toEqual([{ request: "POST /api/node/stop-agents", body: null }])
  await expect(page.getByText("Stopped local agents; 1 ACP session owned by another device remains running.")).toBeVisible()
})

test("stop gateway in General names the machine and how to reconnect", async ({ page }) => {
  await mockGateway(page)
  await page.goto("/settings/general")

  await page.getByRole("button", { name: "Stop gateway…" }).click()
  const dialog = page.getByRole("alertdialog")
  await expect(dialog).toContainText("Stop the gateway on host-a?")
  await expect(dialog).toContainText("ap gateway start on host-a")
  await expect(dialog).toContainText(/\d+ running sessions? keep running/)
  await expect(dialog.getByRole("button").first()).toHaveText("Cancel")
  await expect(dialog.getByRole("button", { name: "Cancel" })).toBeFocused()
  expect(calls).toEqual([])

  await dialog.getByRole("button", { name: "Stop gateway" }).click()
  await expect.poll(() => calls).toEqual([{ request: "POST /api/node/stop", body: null }])
  await expect(page.getByRole("button", { name: "Stop gateway…" })).toBeDisabled()
})

test("account holds identity only; machine controls live in General", async ({ page }) => {
  await mockGateway(page)
  await page.goto("/settings/account")
  await expect(page.getByRole("button", { name: "Sign out everywhere…" })).toBeVisible()
  await expect(page.getByRole("button", { name: "Stop gateway…" })).toHaveCount(0)
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
  await page.route("**/api/status", (route) => route.fulfill({ json: { claimed: false, legacy: true, authenticated: false, email: "", node_name: "atlas" } }))
  await page.route("**/api/claim", (route) => {
    expect(route.request().postDataJSON()).toEqual({ email: "ada@example.com", current_password: "correct horse battery" })
    return route.fulfill({ status: 401, body: "invalid current password" })
  })
  await page.goto("/login")
  await expect(page.getByRole("heading", { name: "Claim atlas" })).toBeVisible()
  await expect(page.getByLabel("Current gateway password", { exact: true })).toHaveAccessibleDescription(/12 to 72 bytes/)
  await page.getByLabel("Email for the owner account", { exact: true }).fill("ada@example.com")
  await page.getByLabel("Current gateway password", { exact: true }).fill("correct horse battery")
  await page.getByRole("button", { name: "Claim gateway", exact: true }).click()
  await expect(page.getByRole("alert")).toContainText("invalid current password")
  await expect(page).toHaveURL(/\/login$/)
})

test("connections group logins by server and a second login needs its own name", async ({ page }) => {
  await mockGateway(page)
  const linear = "https://mcp.linear.app/mcp"
  await page.route("**/api/mcp/credentials", (route) => answer(route, { callback_url: "", credentials: [
    { id: "c1", match: linear, kind: "oauth", label: "Work", status: "connected" },
    { id: "c2", match: linear, kind: "oauth", label: "Client", status: "connected" },
  ] }))
  await page.goto("/settings/connections")
  const logins = page.getByRole("listitem")
  await expect(logins).toHaveCount(2)
  await expect(logins.first()).toContainText("Default")
  await expect(logins.nth(1)).not.toContainText("Default")
  await page.getByRole("button", { name: "Add login…" }).click()
  const dialog = page.getByRole("dialog", { name: "Add login" })
  await expect(dialog.getByLabel("Server URL")).toHaveValue(linear)
  await expect(dialog.getByLabel("Server URL")).toHaveAttribute("readonly", "")
  await expect(dialog.getByLabel("Client ID")).toBeHidden()
  await expect(dialog.getByLabel("Alias")).toBeFocused()
  await dialog.getByLabel("Alias").fill("Work")
  await dialog.getByRole("button", { name: "Sign in…" }).click()
  await expect(page.getByText("A login named Work exists. Choose another name.")).toBeVisible()
  expect(calls).toEqual([])
})

test("phone Vault shows visible actions and the OAuth return state on its row", async ({ page }) => {
  await mockGateway(page)
  await page.setViewportSize({ width: 320, height: 568 })
  await page.route("**/api/mcp/credentials", (route) => answer(route, { callback_url: "", credentials: [
    { id: "c1", match: "https://mcp.linear.app/mcp", kind: "oauth", label: "Work", status: "needs_login" },
    { id: "c2", match: "https://mcp.linear.app/mcp", kind: "oauth", label: "Client", status: "connected" },
  ] }))
  await page.goto("/settings/connections?callback=expired&connection=c1")
  const work = page.getByRole("listitem").filter({ hasText: "Work" })
  await expect(work.getByRole("button", { name: "Work actions" })).toHaveCSS("opacity", "1")
  await expect(work.getByText(/link expired or was already used/)).toBeVisible()
  await expect(work.getByRole("button", { name: "Try again" })).toBeVisible()
  await expect(page.getByRole("listitem").filter({ hasText: "Client" }).getByRole("button", { name: "Try again" })).toHaveCount(0)
})

test("Vault lists agent sign-ins, opens the sign-in sheet and tolerates an older gateway", async ({ page }) => {
  await mockGateway(page)
  let shared = 200
  await page.route("**/api/mcp/credentials", (route) => answer(route, { callback_url: "", credentials: [] }))
  await page.route("**/api/agents", (route) => answer(route, { agents: [
    { harness: "cursor", installed: true, signed_in: false, methods: [{ id: "cursor", label: "Cursor account" }] },
  ] }))
  await page.route("**/api/agents/shared", (route) => shared === 200
    ? answer(route, { agents: [{ harness: "claude", state: "shared", publisher_node: "n1" }, { harness: "cursor", state: "none" }] })
    : route.fulfill({ status: shared, body: "404 page not found" }))
  await page.goto("/settings/connections")
  const pane = page.getByRole("region", { name: "Vault" })
  await expect(pane.getByText("Shared from host-a")).toBeVisible()
  await expect(pane.getByRole("button", { name: "Claude actions" })).toBeVisible()
  await pane.getByRole("button", { name: "Sign in to Cursor CLI…" }).click()
  await expect(page.getByRole("dialog").getByRole("button", { name: "Sign in with Cursor account" })).toBeVisible()
  await page.keyboard.press("Escape")

  // Phones put the action under the status line.
  await page.setViewportSize({ width: 390, height: 700 })
  const status = await pane.getByText("Not shared yet").first().boundingBox()
  const action = await pane.getByRole("button", { name: "Sign in to Cursor CLI…" }).boundingBox()
  expect(action!.y).toBeGreaterThanOrEqual(status!.y + status!.height)

  shared = 404
  await page.reload()
  await expect(pane.getByText("Update Agentpack on this machine to see agent sign-ins.")).toBeVisible()
})

test("sidebar search filters sections, opens the first match on Enter and clears on Escape", async ({ page }) => {
  await mockGateway(page)
  await page.goto("/settings")
  const sidebar = page.getByRole("navigation", { name: "System" })
  const search = page.getByLabel("Search system")
  await search.fill("mcp")
  await expect(sidebar.getByRole("link")).toHaveCount(3)
  await expect(sidebar.getByRole("link", { name: "Appearance" })).toBeHidden()
  await search.press("Enter")
  await expect(page).toHaveURL(/\/settings\/sharing$/)
  await search.press("Escape")
  await expect(search).toHaveValue("")
  await expect(sidebar.getByRole("link", { name: "Appearance" })).toBeVisible()
})

test("sidebar shows badges for issues, expired logins and peers that are behind", async ({ page }) => {
  await mockGateway(page)
  await page.route("**/api/logs/issues*", (route) => answer(route, [{ regressed: true }, { regressed: false }]))
  await page.route("**/api/mcp/credentials", (route) => answer(route, { credentials: [
    { id: "c1", match: "https://mcp.example/mcp", kind: "oauth", label: "Work", status: "needs_login" },
  ] }))
  await page.route("**/api/library/replication/peers", (route) => answer(route, {
    ...replication, contacts: { n2: { needs_update: true } },
  }))
  await page.goto("/settings")
  const sidebar = page.getByRole("navigation", { name: "System" })
  for (const name of ["Diagnostics 2", "Vault 1 to sign in", "Replication 1 not syncing"]) {
    await expect(sidebar.getByRole("link", { name, exact: true })).toBeVisible()
  }
})

test("turning agentpack tools off writes only that default and disables replacement", async ({ page }) => {
  await mockGateway(page)
  let defaults = { agentpack_tools: true, replace_subagents: true }
  await page.route("**/api/agents/defaults", (route) => {
    if (route.request().method() === "PUT") defaults = { ...defaults, ...route.request().postDataJSON() }
    return answer(route, defaults)
  })
  await page.goto("/settings/agents")
  const tools = page.getByRole("switch", { name: "agentpack tools" })
  const replacement = page.getByRole("switch", { name: "Replace built-in subagents" })
  await expect(replacement).toBeChecked()
  await tools.click()
  await expect(tools).not.toBeChecked()
  await expect(replacement).toBeDisabled()
  await expect(replacement).not.toBeChecked()
  expect(calls).toEqual([{ request: "PUT /api/agents/defaults", body: { agentpack_tools: false } }])
})

test("an owner can turn on automatic tool approvals for helper chats", async ({ page }) => {
  await mockGateway(page)
  let defaults = { agentpack_tools: true, replace_subagents: true, bypass_permissions: false, auto_approve_helpers: false }
  await page.route("**/api/agents/defaults", (route) => {
    if (route.request().method() === "PUT") defaults = { ...defaults, ...route.request().postDataJSON() }
    return answer(route, defaults)
  })
  await page.goto("/settings/agents")
  const helpers = page.getByRole("switch", { name: "Auto-approve helper chats" })
  await helpers.click()
  await expect(helpers).toBeChecked()
  await expect(page.getByRole("switch", { name: "Bypass permissions by default" })).not.toBeChecked()
  expect(calls).toEqual([{ request: "PUT /api/agents/defaults", body: { auto_approve_helpers: true } }])
})

test("non-owners can read agent defaults with switches disabled", async ({ page }) => {
  await mockGateway(page)
  await page.route("**/api/settings", (route) => answer(route, { can_manage: false }))
  await page.route("**/api/agents/defaults", (route) => answer(route, { agentpack_tools: true, replace_subagents: true, bypass_permissions: true, auto_approve_helpers: true }))
  await page.goto("/settings/agents")
  await expect(page.getByText("Only the gateway owner can change agent defaults.")).toBeVisible()
  for (const name of ["agentpack tools", "Replace built-in subagents", "Bypass permissions by default", "Auto-approve helper chats"]) {
    await expect(page.getByRole("switch", { name })).toBeDisabled()
  }
})

test("an older gateway shows Needs update for agent defaults", async ({ page }) => {
  await mockGateway(page)
  await page.route("**/api/agents/defaults", (route) => answer(route, { error: "not found" }, 404))
  await page.goto("/settings/agents")
  await expect(page.getByText("Needs update", { exact: true })).toBeVisible()
  await expect(page.getByRole("switch")).toHaveCount(0)
})
