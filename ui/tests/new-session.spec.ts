import { expect, test } from "@playwright/test"
import { mockAPI, projectRow, session } from "./helpers"

test.beforeEach(async ({ page }) => {
  await mockAPI(page)
})

test("one field accepts home and absolute paths, and both back controls restore project search", async ({ page }) => {
  await page.goto("/sessions?new=1")
  const search = page.getByPlaceholder("Search, or type a path")
  const path = page.getByRole("textbox", { name: "Folder path" })
  await search.pressSequentially("~/")
  await expect(path).toHaveValue("~/")
  await path.press("Escape")
  await expect(search).toBeFocused()
  await search.press("/")
  await expect(path).toHaveValue("/")
  await page.getByRole("button", { name: "Back to projects" }).click()
  await expect(search).toBeFocused()
  await expect(page.getByRole("navigation", { name: "Projects" })).toBeVisible()
})

test.describe("projects on two machines", () => {
  test.beforeEach(async ({ page }) => {
    await page.route("**/api/nodes", (route) => route.fulfill({ json: [{ id: "birch", name: "Birch", online: true }] }))
    await page.route((url) => url.pathname === "/api/projects", (route) => {
      const peer = new URL(route.request().url()).searchParams.get("node") === "birch"
      return route.fulfill({ json: peer
        ? [projectRow("/srv/site", [{ harness: "claude" }]), projectRow("/srv/docs")]
        : [projectRow("/work/site", [{ harness: "claude" }]), projectRow("/work/tools")] })
    })
  })

  test("choosing a browsing machine preserves the active project filter", async ({ page }) => {
    await page.goto("/sessions?new=1&machine=birch")
    await page.getByRole("button", { name: "Browse folders on Birch…" }).click()
    const path = page.getByRole("textbox", { name: "Folder path" })
    await expect(path).toBeFocused()
    const machine = page.getByRole("combobox", { name: "Browse folders on" })
    await expect(machine).toContainText("Birch")
    await machine.click()
    await page.getByRole("option", { name: "Gateway machine", exact: true }).click()
    await expect(machine).toContainText("Gateway machine")
    await expect(page.getByText("Gateway machine › /home/viewer", { exact: true })).toBeVisible()
    await path.press("Escape")
    await expect(page.getByRole("button", { name: "Machine: Birch. Edit filters" })).toBeVisible()
    const projects = page.getByRole("navigation", { name: "Projects" })
    await expect(projects.getByRole("button", { name: /docs/ })).toBeVisible()
    await expect(projects.getByRole("button", { name: /tools/ })).toHaveCount(0)
    await expect(projects.getByRole("button", { name: "Browse folders on Gateway machine…" })).toBeVisible()
  })

  test("chips update faceted counts and tokens can be removed individually or cleared", async ({ page }) => {
    await page.goto("/sessions?new=1")
    const filters = page.getByRole("button", { name: /^Filters/ })
    await filters.click()
    const popover = page.getByRole("dialog", { name: "Project filters" })
    const chip = (name: string) => popover.getByRole("button", { name, exact: true })
    await expect(chip("Birch 2")).toBeVisible()
    await expect(chip("Codex 2")).toBeVisible()
    await chip("Birch 2").click()
    await expect(chip("Codex 1")).toBeVisible()
    await chip("Codex 1").click()
    await expect(chip("Birch 1")).toHaveAttribute("aria-pressed", "true")
    await filters.click()
    const tokens = page.getByRole("group", { name: "Active filters" })
    await expect(tokens).toContainText("1 of 3")
    await tokens.getByRole("button", { name: "Remove machine filter" }).click()
    await expect(tokens).toContainText("2 of 3")
    await expect(tokens.getByRole("button", { name: "Remove used with filter" })).toBeVisible()
    await tokens.getByRole("button", { name: "Clear", exact: true }).click()
    await expect(tokens).toHaveCount(0)
  })
})

test("launch names follow the task, terminal and in-place resume states", async ({ page }) => {
  await page.route("**/api/agent-sessions**", (route) => route.fulfill({
    json: { sessions: [{ id: "native-1", title: "Review logs", cwd: "/work/previous" }] },
  }))
  await page.goto("/sessions?new=1")
  const sheet = page.getByRole("dialog", { name: "New session" })
  const action = (name: string) => sheet.getByRole("button", { name, exact: true })
  const first = sheet.getByRole("textbox", { name: "First message" })
  await expect(action("Start chat")).toBeEnabled()
  await first.fill("Keep the draft")
  await expect(action("Start and send")).toBeEnabled()
  await action("Terminal").click()
  await expect(action("Open terminal")).toBeEnabled()
  await action("Chat").click()
  await expect(first).toHaveValue("Keep the draft")
  await first.clear()
  await sheet.getByRole("button", { name: /^Resume a past .* conversation/ }).click()
  await expect(sheet.getByText("Choose a conversation to resume")).toBeVisible()
  await expect(action("Resume")).toBeDisabled()
  await action("Review logs").click()
  await expect(sheet.getByText("Resuming Review logs")).toBeVisible()
  await expect(action("Resume")).toBeEnabled()
  await sheet.getByRole("textbox", { name: "Message to send after resuming" }).fill("Keep the draft")
  await expect(action("Resume and send")).toBeEnabled()
  await expect(action("Resume only")).toBeEnabled()
  await action("New conversation").click()
  await expect(first).toHaveValue("Keep the draft")
  await expect(action("Start and send")).toBeEnabled()
})

test("the shared-folder offer switches the workspace to a new worktree", async ({ page }) => {
  await page.route("**/api/projects", (route) => route.fulfill({
    json: [projectRow(session.directory, [], { git: { branch: "main", head: "abc", dirty: 0 } })],
  }))
  await page.route("**/api/worktrees", (route) => route.fulfill({ json: { git: true, worktrees: [] } }))
  await page.goto("/sessions?new=1")
  const workspace = page.getByRole("combobox", { name: "Workspace" })
  await expect(workspace).toContainText("New worktree")
  await workspace.click()
  await page.getByRole("option", { name: "This folder", exact: true }).click()
  await expect(page.getByText("1 other agent is working in this folder.")).toBeVisible()
  await page.getByRole("button", { name: "Use a new worktree" }).click()
  await expect(workspace).toContainText("New worktree")
  await expect(page.getByText(/^New branch from main/)).toBeVisible()
})

for (const broken of [false, true]) {
  test(`Loads opens automatically for ${broken ? "a broken lockfile" : "a login choice"}`, async ({ page }) => {
    const url = "https://mcp.linear.app/mcp"
    await page.route("**/api/projects", (route) => route.fulfill({ json: [projectRow("/work/previous", [], broken
      ? { error: "invalid lockfile" }
      : { mcp: [{ name: "linear", launcher: "remote", source: "manifest", url }] })] }))
    await page.route("**/api/mcp/credentials", (route) => route.fulfill({ json: { credentials: [
      { id: "work", match: url, kind: "oauth", label: "Work", status: "connected" },
      { id: "client", match: url, kind: "oauth", label: "Client", status: "connected" },
    ] } }))
    await page.goto("/sessions?new=1")
    if (broken) {
      await expect(page.getByText("Couldn't read this project's pack.lock: invalid lockfile")).toBeVisible()
    } else {
      await expect(page.getByRole("combobox", { name: "linear login for previous" })).toBeVisible()
    }
  })
}
