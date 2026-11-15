import { expect, test } from "@playwright/test"
import { mockAPI } from "./helpers"

test("diagnostics lists issues, opens a sample and resolves with a fix version", async ({ page }) => {
  await mockAPI(page)
  let issue = {
    fingerprint: "a".repeat(64), kind: "worker", title: "Worker exited unexpectedly", status: "open",
    regressed: true, count: 7, first_seen: "2026-11-14T10:00:00Z", last_seen: "2026-11-14T12:00:00Z",
    counts: [{ node_id: "machine-with-a-long-stable-node-identity-1234567890", count: 7, last_version: "v0.14.1", last_seen: "2026-11-14T12:00:00Z" }],
    sample: { kind: "worker", name: "unexpected_exit", attrs: { version: "v0.14.1", exit_code: 9 }, stack: "worker.run\n /source.go:42" },
    recent_traces: ["b".repeat(32)], resolved_version: "", note: "",
  }
  const mutations: unknown[] = []
  await page.route("**/api/logs/issues**", async (route) => {
    const request = route.request()
    const url = new URL(request.url())
    if (request.method() === "POST") {
      const body = request.postDataJSON()
      mutations.push(body)
      issue = { ...issue, status: "resolved", regressed: false, resolved_version: body.version, note: body.note }
      return route.fulfill({ json: issue })
    }
    if (url.pathname.endsWith(issue.fingerprint)) return route.fulfill({ json: issue })
    const status = url.searchParams.get("status")
    return route.fulfill({ json: !status || status === issue.status ? [issue] : [] })
  })
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto("/settings/diagnostics")
  const row = page.getByRole("button", { name: /Worker exited unexpectedly/ })
  await expect(row).toContainText("Regressed · 7 occurrences · 1 machine")
  if (process.env.CAPTURE) await page.screenshot({ path: test.info().outputPath("list-desktop.png") })
  await row.click()
  await expect(page.getByRole("heading", { name: "Sample event" })).toBeVisible()
  await expect(page.getByText('"exit_code": 9')).toBeVisible()
  await page.getByRole("button", { name: "Resolve…", exact: true }).click()
  await page.getByRole("button", { name: "Resolve issue", exact: true }).click()
  await expect(page.getByRole("alert")).toContainText("Enter the version containing the fix.")
  expect(mutations).toEqual([])
  await page.getByLabel("Fix version", { exact: true }).fill("v0.14.2")
  await page.getByLabel("Note (optional)").fill("Handle an empty session during restart")
  if (process.env.CAPTURE) await page.screenshot({ path: test.info().outputPath("desktop.png") })
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  if (process.env.CAPTURE) {
    await page.setViewportSize({ width: 390, height: 844 })
    await page.emulateMedia({ colorScheme: "dark" })
    await expect.poll(() => page.getByLabel("Fix version", { exact: true }).evaluate((input) => getComputedStyle(input).color)).toBe(await page.locator("body").evaluate((body) => getComputedStyle(body).color))
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    await page.screenshot({ path: test.info().outputPath("mobile.png") })
  }
  await page.getByRole("button", { name: "Resolve issue", exact: true }).click()
  await expect(page.getByText("Issue resolved.", { exact: true })).toBeVisible()
  await expect(page.getByRole("button", { name: "Reopen", exact: true })).toBeVisible()
  expect(mutations).toEqual([{ version: "v0.14.2", note: "Handle an empty session during restart" }])
  await page.getByRole("button", { name: "Back to issues" }).click()
  await expect(page.getByText("No open issues", { exact: true })).toBeVisible()
  await page.getByRole("button", { name: "Resolved", exact: true }).click()
  await expect(row).toContainText("Resolved · 7 occurrences")
})
