import { expect, test, type Page } from "@playwright/test"
import { fleetNodes, identity, mockAPI, session } from "./helpers"

const viewButton = (page: Page) => page.getByRole("button", { name: /^View options/ })
const options = (page: Page) => page.getByRole("dialog", { name: "View options" })
const rows = (page: Page) => page.getByRole("navigation", { name: "Sessions" }).locator("[data-source-row]")
const activeFilters = (page: Page) => page.getByRole("group", { name: "Active filters" })

test("View options focuses search from its button and the find shortcut", async ({ page }) => {
  await mockAPI(page)
  await page.goto("/sessions")
  const search = options(page).getByRole("searchbox", { name: "Filter sessions" })
  await viewButton(page).click()
  await expect(search).toBeFocused()
  await page.keyboard.press("Escape")
  await expect(viewButton(page)).toBeFocused()
  await page.keyboard.press("Meta+f")
  await expect(search).toBeFocused()
  await viewButton(page).click()
  await expect(options(page)).toBeHidden()
  await page.keyboard.press("Control+f")
  await expect(search).toBeFocused()
})

async function mockFacets(page: Page) {
  await mockAPI(page)
  const peer = fleetNodes[0]!
  const working = { ...session, node_id: peer.id, transport: "acp", conversation_state: "working" }
  const stopped = { ...working, state: { ...working.state, running: false } }
  await page.route("**/api/nodes", (route) => route.fulfill({ json: [peer] }))
  await page.route(/\/api\/sessions(?:\?.*)?$/, (route) => route.fulfill({ json: [
    { ...working, id: "peer-claude" },
    { ...stopped, id: "peer-codex", harness: "codex" },
    { ...stopped, id: "peer-cursor", harness: "cursor" },
    { ...stopped, id: "local-claude", node_id: identity.node.id },
  ] }))
}

test("facets combine across dimensions", async ({ page }) => {
  await mockFacets(page)
  const peer = fleetNodes[0]!
  await page.goto("/sessions")
  await expect(rows(page)).toHaveCount(4)
  await viewButton(page).click()
  const dialog = options(page)
  await dialog.getByRole("combobox", { name: "Machine", exact: true }).click()
  await page.getByRole("option", { name: peer.name }).click()
  await dialog.getByRole("button", { name: "Stopped 2", exact: true }).click()
  const agents = dialog.getByRole("group", { name: "Agent" })
  await agents.getByRole("button", { name: "Codex 1" }).click()
  await agents.getByRole("button", { name: /^Claude\s*0$/ }).click()
  await dialog.getByRole("button", { name: /^Working\s*1$/ }).click()
  await expect(activeFilters(page)).toContainText("2 of 4")
  await expect(page).toHaveURL((url) => url.searchParams.get("host") === peer.id &&
    url.searchParams.get("agent") === "codex,claude" && url.searchParams.get("status") === "stopped,working")
})

test("filter tokens survive reload and clear one or every dimension", async ({ page }) => {
  await mockFacets(page)
  await page.goto("/sessions?host=peer-1&agent=codex,claude&status=stopped,working")
  await expect(rows(page)).toHaveCount(2)
  await page.reload({ waitUntil: "commit" })
  await expect(rows(page)).toHaveCount(2)
  await expect(viewButton(page)).toHaveAccessibleName("View options, 3 filters on")
  await activeFilters(page).getByRole("button", { name: "Remove machine filter" }).click()
  await expect(rows(page)).toHaveCount(3)
  await expect(activeFilters(page).getByRole("button", { name: "Remove agent filter" })).toBeVisible()
  await activeFilters(page).getByRole("button", { name: "Clear", exact: true }).click()
  await expect(rows(page)).toHaveCount(4)
  await expect(page).toHaveURL("/sessions")
})

test("Reset to default restores grouping and clears the open search field", async ({ page }) => {
  await mockAPI(page)
  await page.goto("/sessions")
  await viewButton(page).click()
  const dialog = options(page)
  const reset = dialog.getByRole("button", { name: "Reset to default" })
  const search = dialog.getByRole("searchbox", { name: "Filter sessions" })
  await expect(reset).toBeDisabled()
  await dialog.getByRole("button", { name: "Machine", exact: true }).click()
  await search.fill("123")
  await expect(search).toHaveValue("123")
  await expect(activeFilters(page).getByRole("button", { name: "Remove contains filter" })).toBeVisible()
  await reset.click()
  await expect(dialog.getByRole("button", { name: "Project", exact: true })).toHaveAttribute("aria-pressed", "true")
  await expect(search).toHaveValue("")
  await expect(reset).toBeDisabled()
  await expect(page).toHaveURL("/sessions")
})

test("on touch View options stays in the toolbar and the empty state clears filters", async ({ browser, baseURL }) => {
  const page = await browser.newPage({ baseURL, viewport: { width: 390, height: 844 }, hasTouch: true })
  try {
    await mockAPI(page)
    await page.goto("/sessions?q=no-such-session")
    const toolbar = page.locator('[data-slot="toolbar"]')
    await toolbar.getByRole("button", { name: /^View options/ }).tap()
    await expect(toolbar.getByRole("link", { name: "New session", exact: true })).toBeVisible()
    await expect(options(page)).toBeVisible()
    await expect(options(page).getByRole("searchbox")).not.toBeFocused()
    await page.keyboard.press("Escape")
    await expect(page.getByText("No matching sessions", { exact: true })).toBeVisible()
    await page.getByRole("button", { name: "Clear filters", exact: true }).tap()
    await expect(rows(page)).toHaveCount(2)
    await expect(viewButton(page)).toBeInViewport()
  } finally {
    await page.close()
  }
})

test("Work folds review rows and marking them reviewed updates the group", async ({ page }) => {
  await mockAPI(page)
  await page.addInitScript(() => localStorage.setItem("agentpack.sessions.seen", '{"":0}'))
  const review = Array.from({ length: 6 }, (_, index) => ({
    ...session, id: `review-${index}`, name: `Review ${index + 1}`, transport: "acp", conversation_state: "idle",
  }))
  await page.route(/\/api\/sessions(?:\?.*)?$/, (route) => route.fulfill({ json: review }))
  await page.goto("/sessions")
  const group = page.getByRole("region", { name: "Ready for review" })
  await expect(group.getByRole("link")).toHaveCount(5)
  await expect(page.getByRole("region", { name: /Needs you|Working|Waiting/ })).toHaveCount(0)
  await group.getByRole("button", { name: "Show 1 more" }).click()
  await expect(group.getByRole("link")).toHaveCount(6)
  await group.getByRole("link", { name: "Review 1", exact: true }).hover()
  await group.getByRole("button", { name: "Mark “Review 1” reviewed" }).click()
  await expect(group.getByRole("link")).toHaveCount(5)
  await group.getByRole("button", { name: "Mark all reviewed" }).click()
  await expect(group).toHaveCount(0)
  await expect(page.getByText("Nothing needs attention", { exact: true })).toBeVisible()
})

test("the Work composer opens New session with its draft on Meta+Enter", async ({ page }) => {
  await mockAPI(page)
  await page.goto("/sessions")
  const task = page.getByRole("textbox", { name: "Task", exact: true })
  await task.fill("Check the build logs")
  await task.press("Meta+Enter")
  await expect(page.getByRole("dialog", { name: "New session" }).getByRole("textbox", { name: "First message" })).toHaveValue("Check the build logs")
})
