import { expect, test, type Page } from "@playwright/test"
import { mockAPI } from "./helpers"

// Wednesday, Nov 18 2026, in UTC so the local days are fixed.
test.use({ timezoneId: "UTC" })
const NOW = new Date("2026-11-18T15:00:00Z")

const hour = (iso: string) => Date.parse(iso) / 1000
const row = (iso: string, harness: string, folder: string, node: string, output: number) => ({
  hour: hour(iso), node_id: node, harness, folder_id: folder, model: "", turns: 2, sessions: 1,
  input_tokens: 10, output_tokens: output, cache_read_tokens: output * 50, cache_write_tokens: 0, cost_micros: harness === "claude" ? 1_500_000 : 0, agent_ms: 600_000,
})
const usage = {
  hourly: [row("2026-11-16T10:00:00Z", "claude", "f-agentpack", "gateway", 961_000), row("2026-11-17T10:00:00Z", "codex", "f-codemode", "peer-1", 700_000)],
  sessions: [
    { id: "s1", node_id: "gateway", title: "Fix limits nudge", name: "", directory: "/work/agentpack", folder_id: "f-agentpack", harness: "claude", model: "claude-opus-5-5", turns: 2, input_tokens: 10, output_tokens: 961_000, cache_read_tokens: 0, cache_write_tokens: 0, cost_micros: 1_500_000, started_ms: Date.parse("2026-11-16T09:00:00Z"), last_event_ms: Date.parse("2026-11-16T11:00:00Z") },
    { id: "s2", node_id: "peer-1", title: "Release checks", name: "", directory: "/srv/codemode", folder_id: "f-codemode", harness: "codex", model: "gpt-6.1-sol", turns: 2, input_tokens: 10, output_tokens: 700_000, cache_read_tokens: 0, cache_write_tokens: 0, cost_micros: -1, started_ms: Date.parse("2026-11-17T09:00:00Z"), last_event_ms: Date.parse("2026-11-17T11:00:00Z") },
  ],
  samples: [],
}

async function open(page: Page, path: string) {
  await page.clock.setFixedTime(NOW)
  const requests = await mockAPI(page)
  await page.route("**/api/nodes", (route) => route.fulfill({ json: [{ id: "peer-1", name: "oracle-vm", online: true }, { id: "peer-2", name: "build-server", online: true }, { id: "peer-3", name: "asleep", online: false }] }))
  await page.route("**/api/usage?**", (route) => route.fulfill({ json: usage }))
  await page.goto(path)
  return requests
}

const hero = (page: Page) => page.locator('[data-slot="usage-hero"]')
const table = (page: Page) => page.getByRole("table", { name: "Sessions in this range" })

test("scrubbing moves a day into the header and a click pins it and filters the sessions", async ({ page }) => {
  await open(page, "/usage")
  await expect(hero(page)).toContainText("This Week")
  await expect(hero(page)).toContainText("1.7M")
  await expect(table(page).getByRole("row")).toHaveCount(3)

  await page.locator('[data-bucket="1"]').hover()
  await expect(hero(page)).toContainText("Tuesday, Nov 17")
  await expect(hero(page)).toContainText("700K")

  await page.locator('[data-bucket="1"]').click()
  await page.mouse.move(0, 0)
  await expect(hero(page)).toContainText("Tuesday, Nov 17")
  await expect(table(page).getByRole("row")).toHaveCount(2)
  await expect(table(page)).toContainText("Release checks")
  await page.getByRole("button", { name: "Show every day" }).click()
  await expect(table(page).getByRole("row")).toHaveCount(3)
})

test("filters combine, show as tokens in the URL, and the range steps back under them", async ({ page }) => {
  await open(page, "/usage")
  await page.getByRole("button", { name: "Filters", exact: true }).click()
  const filters = page.getByRole("dialog", { name: "Usage filters" })
  const projects = filters.getByRole("group", { name: "Project" })
  await expect(projects.getByRole("button", { name: /agentpack/ })).toContainText("961K")
  await projects.getByRole("button", { name: /codemode/ }).click()
  await filters.getByRole("group", { name: "Machine" }).getByRole("button", { name: /oracle-vm/ }).click()
  await expect(page).toHaveURL(/project=codemode&machine=peer-1/)
  await page.keyboard.press("Escape")
  await expect(filters).toBeHidden()
  await expect(page.getByRole("button", { name: "Filters, 2 on" })).toBeVisible()
  await expect(hero(page)).toContainText("700K")
  await expect(table(page)).not.toContainText("Fix limits nudge")

  await page.getByRole("button", { name: "Remove machine filter" }).click()
  await expect(page).toHaveURL(/\/usage\?project=codemode$/)
  await expect(page.getByRole("heading", { name: "codemode", level: 1 })).toBeVisible()

  await page.getByRole("button", { name: "Previous period" }).click()
  await expect(page).toHaveURL(/back=1/)
  await expect(page).toHaveURL(/project=codemode/)
  await expect(page.getByText("No usage matches these filters")).toBeVisible()
  await page.getByRole("button", { name: "Clear filters" }).click()
  await expect(page).toHaveURL(/\/usage\?back=1$/)
})

test("⌘F searches projects inside the filters and Enter picks the first match", async ({ page }) => {
  await open(page, "/usage")
  await page.getByRole("button", { name: "Filters", exact: true }).click()
  const filters = page.getByRole("dialog", { name: "Usage filters" })
  await filters.getByRole("group", { name: "Agent" }).getByRole("button").first().focus()
  await page.keyboard.press("Control+f")
  const search = filters.getByRole("searchbox", { name: "Search projects" })
  await expect(search).toBeFocused()
  await search.fill("code")
  await search.press("Enter")
  await expect(page).toHaveURL(/\/usage\?project=codemode$/)
})

test("an old drill-down link opens the filtered page", async ({ page }) => {
  await open(page, "/usage/agent/codex?range=month&metric=cost")
  await expect(page).toHaveURL(/\/usage\?range=month&metric=cost&agent=codex$/)
  await expect(page.getByRole("button", { name: "Remove agent filter" })).toBeVisible()
  // Old project links carried inode IDs, which name nothing now.
  await page.goto("/usage/project/12345")
  await expect(page).toHaveURL(/\/usage$/)
})

test("one System sidebar: no sources, no peer project reads, and its search survives navigation", async ({ page }) => {
  const requests = await open(page, "/usage/limits?machine=peer-1")
  const sidebar = page.getByRole("navigation", { name: "System" })
  // Limits has a machine param of its own, which must not become a Usage filter.
  await sidebar.getByRole("link", { name: "Usage", exact: true }).click()
  await expect(page).toHaveURL(/\/usage$/)
  await expect(hero(page)).toContainText("1.7M")
  await expect(sidebar.getByRole("link", { name: /agentpack|codemode/ })).toHaveCount(0)
  expect(requests.filter((request) => request.path === "/api/projects")).toEqual([])

  const search = page.getByRole("searchbox", { name: "Search system" })
  await search.fill("screen reader")
  await search.press("Enter")
  await expect(page).toHaveURL(/\/settings\/appearance$/)
  await expect(search).toHaveValue("screen reader")
})

test("Limits asks each online peer to refresh once, and /limits lands there", async ({ page }) => {
  const requests = await open(page, "/limits")
  await expect(page).toHaveURL(/\/usage\/limits$/)
  const nudged = () => requests.filter((request) => request.method === "POST" && request.path === "/api/limits/refresh").map((request) => request.query.get("node") ?? "local").sort()
  await expect.poll(nudged).toEqual(["peer-1", "peer-2"])
  // Revisiting within the view asks nobody again.
  await page.getByRole("navigation", { name: "System" }).getByRole("link", { name: "Usage", exact: true }).click()
  await page.getByRole("navigation", { name: "System" }).getByRole("link", { name: /Limits/ }).click()
  await expect(page).toHaveURL(/\/usage\/limits/)
  expect(nudged()).toEqual(["peer-1", "peer-2"])
})
