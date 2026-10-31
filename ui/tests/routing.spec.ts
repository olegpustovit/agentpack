import { expect, test, type Locator, type Page } from "@playwright/test"
import { attachTerminal, mockAPI } from "./helpers"

function trackPageErrors(page: Page) {
  const errors: string[] = []
  page.on("pageerror", (error) => errors.push(error.message))
  return errors
}

// The shared mock answers unknown endpoints with {}; the library list maps over drafts, so give it an array.
async function openFleet(page: Page) {
  await mockAPI(page, { fleet: true })
  await page.route("**/api/library/drafts", (route) => route.fulfill({ json: [] }))
}

// Desktop shows the Views bar in the top toolbar; under 760 px the Tabs bar takes over. The Settings icon sits in the toolbar at every width.
function viewLink(page: Page, label: "Sessions" | "Library" | "Machines" | "Limits" | "Settings") {
  if (label === "Settings") return page.getByRole("link", { name: "Settings", exact: true }).first()
  return page
    .getByRole("navigation", { name: "Views" })
    .or(page.getByRole("navigation", { name: "Tabs" }))
    .getByRole("link", { name: label, exact: true })
}

// No error boundary, no not-found page and no uncaught exception.
async function expectNoCrash(page: Page, errors: string[]) {
  await expect(page.getByRole("alert")).toHaveCount(0)
  await expect(page.getByText("Not found", { exact: true })).toHaveCount(0)
  expect(errors).toEqual([])
}

const sessionsNav = (page: Page) => page.getByRole("navigation", { name: "Sessions" })
const libraryNav = (page: Page) => page.getByRole("navigation", { name: "Library" })
const limitsGrid = (page: Page) => page.getByRole("table", { name: "Provider limits" })
const topologyHeading = (page: Page) => page.getByRole("heading", { level: 1, name: "Topology" })
const machineList = (page: Page) => page.getByRole("link", { name: "Topology", exact: true })
const settingsNav = (page: Page) => page.getByRole("navigation", { name: "Settings sections" })

// `settles` is the URL the route ends on when it redirects (/machines, /settings).
const deepLinks: { path: string; settles?: string; rendered: (page: Page) => Locator }[] = [
  { path: "/sessions", rendered: sessionsNav },
  { path: "/sessions/local/local-alpha-claude", rendered: (page) => page.getByRole("button", { name: "Larger" }) },
  { path: "/library", rendered: libraryNav },
  { path: "/library/package/github.com/acme/tools", rendered: (page) => page.getByText("Package not found") },
  { path: "/library/agentpack/pack-1", rendered: (page) => page.getByText("Agentpack not found") },
  { path: "/library/mcp/foo-tools", rendered: (page) => page.getByText("MCP server not found") },
  { path: "/machines", rendered: machineList },
  { path: "/machines/peer-1", rendered: (page) => page.getByRole("heading", { level: 1, name: "Build server" }) },
  { path: "/machines/topology", rendered: topologyHeading },
  { path: "/limits", rendered: limitsGrid },
  { path: "/settings", settles: "/settings/general", rendered: settingsNav },
  { path: "/settings/sharing", rendered: (page) => page.getByRole("switch", { name: "Tailscale sharing" }) },
]

for (const { path, settles = path, rendered } of deepLinks) {
  test(`deep link survives reload: ${path}`, async ({ page }) => {
    const errors = trackPageErrors(page)
    await openFleet(page)
    await attachTerminal(page)
    await page.goto(path)
    await expect(page).toHaveURL(settles)
    // Let the list requests finish first: a reload that cancels them makes WebKit report uncaught fetch errors.
    await page.waitForLoadState("networkidle")
    await expect(rendered(page)).toBeVisible()
    await page.reload()
    await expect(page).toHaveURL(settles)
    await expect(rendered(page)).toBeVisible()
    await expectNoCrash(page, errors)
  })
}

test("on phones Back from a machine returns to the machine list", async ({ page }) => {
  const errors = trackPageErrors(page)
  await page.setViewportSize({ width: 390, height: 844 })
  await openFleet(page)
  await page.goto("/machines/local")
  await page.getByRole("link", { name: "Back", exact: true }).click()
  await expect(page).toHaveURL(/\/machines$/)
  await expect(machineList(page)).toBeVisible()
  await expectNoCrash(page, errors)
})

test("back and forward step through the views, with each view's URL", async ({ page }) => {
  const errors = trackPageErrors(page)
  await openFleet(page)
  await page.goto("/sessions")
  await expect(sessionsNav(page)).toBeVisible()

  // Limits and Machines come before Settings; Machines opens its list, so the tab bar stays on screen on phones.
  const trail = [
    { label: "Sessions", url: "/sessions", rendered: sessionsNav },
    { label: "Library", url: "/library", rendered: libraryNav },
    { label: "Limits", url: "/limits", rendered: limitsGrid },
    { label: "Machines", url: "/machines", rendered: machineList },
    { label: "Settings", url: "/settings/general", rendered: settingsNav },
  ] as const

  for (const view of trail.slice(1)) {
    await viewLink(page, view.label).click()
    await expect(page).toHaveURL(view.url)
    await expect(view.rendered(page)).toBeVisible()
  }
  for (const view of trail.slice(0, -1).reverse()) {
    await page.goBack()
    await expect(page).toHaveURL(view.url)
    await expect(view.rendered(page)).toBeVisible()
  }
  for (const view of trail.slice(1)) {
    await page.goForward()
    await expect(page).toHaveURL(view.url)
    await expect(view.rendered(page)).toBeVisible()
  }
  await expectNoCrash(page, errors)
})

test("malformed sessions search falls back to grouping by machine", async ({ page }) => {
  const errors = trackPageErrors(page)
  await openFleet(page)
  await page.goto("/sessions?group=bogus&new=maybe")
  await expect(sessionsNav(page)).toBeVisible()
  await expect(page.getByRole("button", { name: "Machine", exact: true })).toHaveAttribute("aria-pressed", "true")
  await expectNoCrash(page, errors)
})

test("malformed library search falls back to the All filter", async ({ page }) => {
  const errors = trackPageErrors(page)
  await openFleet(page)
  await page.goto("/library?type=bogus&sort=zzz&q=%ZZ")
  await expect(libraryNav(page)).toBeVisible()
  // The button's name includes its count ("All 0"), so match the prefix.
  await expect(page.getByRole("button", { name: /^All\b/ })).toHaveAttribute("aria-pressed", "true")
  await expectNoCrash(page, errors)
})

test("unknown limits search key is ignored", async ({ page }) => {
  const errors = trackPageErrors(page)
  await openFleet(page)
  await page.goto("/limits?unexpected=1")
  await expect(limitsGrid(page)).toBeVisible()
  await expectNoCrash(page, errors)
})

test("malformed session id in the deep link renders the terminal view", async ({ page }) => {
  const errors = trackPageErrors(page)
  await openFleet(page)
  await attachTerminal(page)
  // %25 reaches the router as a literal "%ZZ" id; a raw %ZZ path is a 404 from the static server before the app loads.
  await page.goto("/sessions/local/%25ZZ")
  await expect(page.getByRole("button", { name: "Larger" })).toBeVisible()
  await expectNoCrash(page, errors)
})
