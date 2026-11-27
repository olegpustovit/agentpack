import { expect, test } from "@playwright/test"
import { mockAPI, projectRow, session } from "./helpers"

const closedAt = new Date().toISOString()
const chat = {
  ...session, name: "Release review", transport: "acp", ended_at: closedAt,
  state: { ...session.state, running: false },
}

test("closing from the session menu can be undone in Activity", async ({ page }) => {
  await mockAPI(page)
  const changes: boolean[] = []
  let closed = false
  await page.route((url) => url.pathname === "/api/sessions", (route) =>
    route.fulfill({ json: [{ ...chat, closed_at: closed ? closedAt : undefined }] }),
  )
  await page.route("**/api/sessions/local-one", (route) => {
    if (route.request().method() !== "PATCH") return route.fallback()
    closed = route.request().postDataJSON().closed
    changes.push(closed)
    return route.fulfill({ json: {} })
  })
  await page.goto("/sessions")
  const row = page.getByRole("navigation", { name: "Sessions" }).getByRole("link", { name: /Release review/ })
  await row.click({ button: "right" })
  await page.getByRole("menuitem", { name: /^Close(?: |$)/ }).click()
  await expect(row).toHaveCount(0)
  await page.getByRole("button", { name: /Closed .*Show activity/ }).click()
  const undo = page.getByRole("listitem").filter({ hasText: /Closed.*Release review/ }).getByRole("button", { name: "Undo" })
  await undo.click()
  await expect(row).toBeVisible()
  expect(changes).toEqual([true, false])
})

test("the project header confirms only the agents that will stop", async ({ page }) => {
  const requests = await mockAPI(page)
  await page.route("**/api/sessions", (route) => route.fulfill({ json: [
    chat, { ...session, id: "busy", name: "Busy build", harness: "codex" },
  ] }))
  await page.goto("/sessions")
  await page.locator("[data-head-key]").getByRole("button", { name: "More actions" }).click()
  await page.getByRole("menuitem", { name: "Close my-project" }).click()
  const dialog = page.getByRole("alertdialog")
  await expect(dialog.getByRole("list", { name: "Agents that will stop" }).getByRole("listitem")).toHaveText([
    "Stops Codex on Gateway machine · Busy build",
  ])
  expect(requests.filter((request) => request.method !== "GET")).toEqual([])
  await dialog.getByRole("button", { name: "Stop and Close" }).click()
  await expect.poll(() => requests.filter((request) => request.path.startsWith("/api/sessions/") && request.method !== "GET").map((request) => request.path).sort())
    .toEqual(["/api/sessions/busy", "/api/sessions/busy/stop", "/api/sessions/local-one"])
})

test("a closed chat is readable when opened by URL", async ({ page }) => {
  await mockAPI(page)
  await page.route("**/api/sessions", (route) => route.fulfill({ json: [{ ...chat, closed_at: closedAt }] }))
  await page.goto("/sessions/local/local-one")
  await expect(page.getByText("Closed · in History", { exact: true })).toBeVisible()
  await expect(page.getByRole("button", { name: "Reopen", exact: true })).toBeVisible()
})

test("Work opens History, where search and a project token narrow the list before Reopen", async ({ page }) => {
  const requests = await mockAPI(page)
  const other = { ...chat, id: "other", name: "Other review", directory: "/work/another" }
  await page.route((url) => url.pathname === "/api/sessions", (route) => {
    const sessions = [{ ...chat, closed_at: closedAt }, other]
    return route.fulfill({ json: new URL(route.request().url()).searchParams.has("history") ? { sessions, next: "" } : sessions })
  })
  await page.goto("/sessions")
  await expect(page.getByRole("main").getByRole("link", { name: /Release review/ })).toBeVisible()
  await page.getByRole("button", { name: "Show all" }).click()
  await expect(page).toHaveURL(/view=history/)
  const search = page.getByRole("searchbox", { name: "Search history" })
  const history = page.getByRole("navigation", { name: "History" })
  await search.fill("release")
  await expect(history.getByRole("link")).toHaveCount(1)
  await search.fill("my-pro")
  await page.getByRole("button", { name: "Project my-project" }).click()
  await expect(page).toHaveURL(/project=my-project/)
  await expect(history.getByRole("link")).toHaveCount(1)
  await history.getByRole("link", { name: /Release review/ }).hover()
  await history.getByRole("button", { name: "Reopen Release review" }).click()
  await expect.poll(() => requests.find((request) => request.method === "PATCH")?.body).toEqual({ closed: false })
})

test("Install package opens the selected project's focused search", async ({ page }) => {
  await mockAPI(page)
  await page.route("**/api/projects", (route) => route.fulfill({ json: [projectRow(session.directory)] }))
  await page.goto("/sessions")
  await page.locator('[data-row-key="local:local-one"]').click({ button: "right" })
  await page.getByRole("menuitem", { name: "Install package…" }).click()
  await expect(page).toHaveURL(/\/sessions\/project\/local\/[^?]+\?install=/)
  await expect(page.getByRole("searchbox", { name: "Search skills, plugins and MCP servers" })).toBeFocused()
})
