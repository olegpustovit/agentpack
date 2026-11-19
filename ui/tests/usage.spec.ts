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

test("the sidebar drills into a project and the range steps back", async ({ page }) => {
  await open(page, "/usage")
  const sidebar = page.getByRole("navigation", { name: "Usage" })
  await expect(sidebar.getByRole("link", { name: /agentpack/ })).toContainText("961K")
  await sidebar.getByRole("link", { name: /codemode/ }).click()
  await expect(page).toHaveURL(/\/usage\/project\/f-codemode/)
  await expect(hero(page)).toContainText("700K")
  await expect(table(page)).not.toContainText("Fix limits nudge")

  await page.getByRole("button", { name: "Previous period" }).click()
  await expect(page).toHaveURL(/back=1/)
  await expect(hero(page)).toContainText("Last Week")
})

test("Limits asks each online peer to refresh once, and /limits lands there", async ({ page }) => {
  const requests = await open(page, "/limits")
  await expect(page).toHaveURL(/\/usage\/limits$/)
  const nudged = () => requests.filter((request) => request.method === "POST" && request.path === "/api/limits/refresh").map((request) => request.query.get("node") ?? "local").sort()
  await expect.poll(nudged).toEqual(["peer-1", "peer-2"])
  // Revisiting within the view asks nobody again.
  await page.getByRole("navigation", { name: "Usage" }).getByRole("link", { name: "Overview" }).click()
  await page.getByRole("navigation", { name: "Usage" }).getByRole("link", { name: /Limits/ }).click()
  await expect(page).toHaveURL(/\/usage\/limits/)
  expect(nudged()).toEqual(["peer-1", "peer-2"])
})
