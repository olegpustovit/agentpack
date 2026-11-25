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

test("composer offers snapshot model, reasoning, and agent slash commands while preserving Cmd/Ctrl+Enter and IME", async ({ page }) => {
  const snapshotQueries: URLSearchParams[] = []
  const starts: unknown[] = []
  await page.route("**/api/agent-snapshot**", (route) => {
    snapshotQueries.push(new URL(route.request().url()).searchParams)
    return route.fulfill({
      json: {
        config: {
          configOptions: [
            {
              type: "select",
              id: "model",
              name: "Model",
              category: "model",
              currentValue: "gpt-5.4",
              options: [
                { value: "gpt-5.4", name: "GPT-5.4" },
                { value: "gpt-5.3-codex", name: "GPT-5.3 Codex" },
                { value: "legacy", name: "Legacy", disabledReason: "Plan limit" },
              ],
            },
            {
              type: "select",
              id: "reasoning_effort",
              name: "Reasoning",
              category: "thought_level",
              currentValue: "medium",
              options: [
                { value: "low", name: "Low" },
                { value: "medium", name: "Medium" },
                { value: "high", name: "High" },
              ],
            },
          ],
        },
        commands: {
          availableCommands: [
            { name: "goal", description: "Active chat goal" },
            { name: "fork", description: "Active chat fork" },
            { name: "terminal", description: "Active chat terminal" },
            { name: "compact", description: "Active chat compact" },
            { name: "review", description: "Review changes", input: { hint: "scope" } },
            { name: "agentpack-bundle:ponytail", description: "Least new code" },
          ],
        },
      },
    })
  })
  await page.route(/\/api\/sessions(\?.*)?$/, (route) => {
    if (route.request().method() !== "POST") return route.fallback()
    starts.push(route.request().postDataJSON())
    return route.fulfill({ json: { ...session, id: "created-chat", transport: "acp" } })
  })
  await page.goto("/sessions?new=1")
  const sheet = page.getByRole("dialog", { name: "New session" })
  const model = sheet.getByRole("combobox", { name: "Model" })
  const reasoning = sheet.getByRole("combobox", { name: "Reasoning" })
  await expect(model).toContainText("Model: automatic")
  await expect(reasoning).toContainText("Reasoning: automatic")
  expect(snapshotQueries[0]?.get("model_fallback")).toBe("1")
  expect(snapshotQueries[0]?.get("owner")).toBe("local")
  expect(snapshotQueries[0]?.get("harness")).toBe("codex")
  expect(snapshotQueries[0]?.get("definition_root")).toBe("/work/previous")
  expect(snapshotQueries[0]?.get("mode")).toBe("default")

  await model.click()
  await expect(page.getByRole("option", { name: /Model: automatic/ })).toContainText("Last used: GPT-5.4")
  await expect(page.getByRole("option", { name: /Legacy/ })).toHaveAttribute("aria-disabled", "true")
  await page.getByRole("option", { name: "GPT-5.3 Codex", exact: true }).click()
  await expect(model).toContainText("GPT-5.3 Codex")
  await reasoning.click()
  await page.getByRole("option", { name: "High", exact: true }).click()
  await expect(reasoning).toContainText("High")

  const first = sheet.getByRole("textbox", { name: "First message" })
  await first.click()
  await first.pressSequentially("/")
  const hints = sheet.locator('[data-slot="composer-hints"]')
  await expect(hints).toBeVisible()
  await expect(hints).toContainText("/review")
  await expect(hints).toContainText("/agentpack-bundle:ponytail")
  for (const blocked of ["/goal", "/fork", "/terminal", "/compact"]) {
    await expect(hints).not.toContainText(blocked)
  }
  await first.press("Escape")
  await expect(hints).toHaveCount(0)
  await expect(sheet).toBeVisible()

  await first.fill("Check")
  await sheet.getByRole("button", { name: "Commands", exact: true }).click()
  await expect(hints).toBeVisible()
  await first.press("ArrowDown")
  await first.press("Tab")
  await expect(first).toHaveValue("Check /agentpack-bundle:ponytail ")
  await expect(sheet.locator("mark[data-command]")).toHaveText("/agentpack-bundle:ponytail")

  // Composing IME keys must not pick hints or submit the sheet.
  await first.pressSequentially("/re")
  await expect(hints).toBeVisible()
  await first.evaluate((element) => {
    element.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", ctrlKey: true, isComposing: true, bubbles: true, cancelable: true }))
  })
  expect(starts).toHaveLength(0)

  // Ctrl+Enter submits even while slash hints are open.
  await first.press("Control+Enter")
  await expect.poll(() => starts).toHaveLength(1)
  expect(starts[0]).toMatchObject({
    directory: "/work/previous",
    harness: "codex",
    mode: "default",
    transport: "acp",
    model: "gpt-5.3-codex",
    effort: "high",
    first_message: "Check /agentpack-bundle:ponytail /re",
  })
})

test("model and reasoning options stay scoped to machine, project, agent, and mode, and hide in terminal", async ({ page }) => {
  const starts: unknown[] = []
  await page.route("**/api/nodes", (route) => route.fulfill({ json: [{ id: "birch", name: "Birch", online: true }] }))
  await page.route((url) => url.pathname === "/api/projects", (route) => {
    const peer = new URL(route.request().url()).searchParams.get("node") === "birch"
    return route.fulfill({
      json: peer
        ? [projectRow("/srv/alpha", [{ harness: "codex" }], { last_used: "2026-10-26T10:30:00Z" })]
        : [
            projectRow("/work/alpha", [{ harness: "codex" }], {
              last_used: "2026-10-27T10:30:00Z",
              modes: [
                { name: "default", off_packages: [], partial_packages: [], off_mcp: [] },
                { name: "review", off_packages: [], partial_packages: [], off_mcp: [] },
              ],
            }),
            projectRow("/work/beta", [{ harness: "codex" }], { last_used: "2026-10-25T10:30:00Z" }),
          ],
    })
  })
  await page.route("**/api/agent-snapshot**", (route) => {
    const q = new URL(route.request().url()).searchParams
    const key = `${q.get("owner")}|${q.get("definition_root")}|${q.get("harness")}|${q.get("mode")}`
    if (q.get("harness") === "claude") {
      return route.fulfill({
        json: {
          config: {
            configOptions: [
              { type: "select", id: "model", category: "model", currentValue: "sonnet", options: [{ value: "sonnet", name: "Sonnet" }, { value: "opus", name: "Opus" }] },
            ],
          },
        },
      })
    }
    const label = key.includes("review") ? "GPT-5.4 Review" : key.includes("beta") ? "GPT-5.4 Beta" : key.includes("birch") ? "GPT-5.4 Birch" : "GPT-5.4 Local"
    return route.fulfill({
      json: {
        config: {
          configOptions: [
            { type: "select", id: "model", category: "model", currentValue: "default-model", options: [{ value: "default-model", name: label }, { value: "alt-model", name: "Alt Model" }] },
            { type: "select", id: "effort", category: "thought_level", currentValue: "medium", options: [{ value: "medium", name: "Medium" }, { value: "high", name: "High" }] },
          ],
        },
      },
    })
  })
  await page.route(/\/api\/sessions(\?.*)?$/, (route) => {
    if (route.request().method() !== "POST") return route.fallback()
    starts.push(route.request().postDataJSON())
    return route.fulfill({ json: { ...session, id: "created-term", transport: "terminal" } })
  })
  await page.goto("/sessions?new=1")
  const sheet = page.getByRole("dialog", { name: "New session" })
  const model = sheet.getByRole("combobox", { name: "Model" })
  const reasoning = sheet.getByRole("combobox", { name: "Reasoning" })
  async function expectAutomaticModel(last: string) {
    await expect(model).toContainText("Model: automatic")
    await model.click()
    await expect(page.getByRole("option", { name: /Model: automatic/ })).toContainText(`Last used: ${last}`)
    await page.keyboard.press("Escape")
  }
  await expectAutomaticModel("GPT-5.4 Local")
  await model.click()
  await page.getByRole("option", { name: "Alt Model", exact: true }).click()
  await reasoning.click()
  await page.getByRole("option", { name: "High", exact: true }).click()
  await expect(model).toContainText("Alt Model")
  await expect(reasoning).toContainText("High")

  // Switching mode uses that mode's own option state.
  await sheet.getByRole("combobox", { name: "Mode", exact: true }).click()
  await page.getByRole("option", { name: "review", exact: true }).click()
  await expectAutomaticModel("GPT-5.4 Review")
  await expect(reasoning).toContainText("Reasoning: automatic")

  // Switching harness never leaks Codex picks into Claude.
  await sheet.getByRole("combobox", { name: "Agent", exact: true }).click()
  await page.getByRole("option", { name: "Claude", exact: true }).click()
  await expectAutomaticModel("Sonnet")
  await expect(reasoning).toHaveCount(0)

  // Switching back to Codex default restores its scoped pick, then switching project and machine stays isolated.
  await sheet.getByRole("combobox", { name: "Agent", exact: true }).click()
  await page.getByRole("option", { name: /Codex/ }).click()
  await sheet.getByRole("combobox", { name: "Mode", exact: true }).click()
  await page.getByRole("option", { name: "Default mode", exact: true }).click()
  await expect(model).toContainText("Alt Model")
  await expect(reasoning).toContainText("High")

  await sheet.getByRole("combobox", { name: "Machine" }).click()
  await page.getByRole("option", { name: "Birch", exact: true }).click()
  await expectAutomaticModel("GPT-5.4 Birch")
  await expect(reasoning).toContainText("Reasoning: automatic")

  await sheet.getByRole("navigation", { name: "Projects" }).getByRole("button", { name: /beta/ }).click()
  await expectAutomaticModel("GPT-5.4 Beta")
  await expect(reasoning).toContainText("Reasoning: automatic")

  // Terminal transport hides chat options and omits model/effort from POST /api/sessions.
  await sheet.getByRole("navigation", { name: "Projects" }).getByRole("button", { name: /alpha/ }).click()
  await expect(model).toContainText("Alt Model")
  await sheet.getByRole("button", { name: "Terminal", exact: true }).click()
  await expect(model).toHaveCount(0)
  await expect(reasoning).toHaveCount(0)
  await sheet.getByRole("button", { name: "Chat", exact: true }).click()
  await expect(model).toContainText("Alt Model")
  await expect(reasoning).toContainText("High")
  await sheet.getByRole("button", { name: "Terminal", exact: true }).click()
  await sheet.getByRole("button", { name: "Open terminal", exact: true }).click()
  await expect.poll(() => starts).toHaveLength(1)
  expect(starts[0]).toMatchObject({ transport: "terminal" })
  expect(starts[0]).not.toHaveProperty("model")
  expect(starts[0]).not.toHaveProperty("effort")
})

test("missing snapshot shows an honest cue without inventing model options", async ({ page }) => {
  const starts: unknown[] = []
  await page.route("**/api/agent-snapshot**", (route) => route.fulfill({ json: null }))
  await page.route(/\/api\/sessions(\?.*)?$/, (route) => {
    if (route.request().method() !== "POST") return route.fallback()
    starts.push(route.request().postDataJSON())
    return route.fulfill({ json: { ...session, id: "created-empty", transport: "acp" } })
  })
  await page.goto("/sessions?new=1")
  const sheet = page.getByRole("dialog", { name: "New session" })
  const cue = sheet.getByText("Model options aren't saved for Codex on Gateway machine yet. You can choose a model in chat before sending.")
  await expect(cue).toBeVisible()
  await expect(sheet.getByRole("combobox", { name: "Model" })).toHaveCount(0)
  await expect(sheet.getByRole("combobox", { name: "Reasoning" })).toHaveCount(0)
  await expect(sheet.getByRole("button", { name: "Commands", exact: true })).toHaveCount(0)
  await sheet.getByRole("button", { name: "Terminal", exact: true }).click()
  await expect(cue).toHaveCount(0)
  await sheet.getByRole("button", { name: "Chat", exact: true }).click()
  await expect(cue).toBeVisible()
  await sheet.getByRole("button", { name: "Start chat", exact: true }).click()
  await expect.poll(() => starts).toHaveLength(1)
  expect(starts[0]).not.toHaveProperty("model")
  expect(starts[0]).not.toHaveProperty("effort")
})

test("composer loads and caches @ tracked file hints on demand, scopes by machine/project, and tolerates old peers", async ({ page }) => {
  const fileRequests: { node: string | null; directory: string | null }[] = []
  const starts: unknown[] = []
  await page.route("**/api/nodes", (route) => route.fulfill({ json: [
    { id: "birch", name: "Birch", online: true },
    { id: "legacy", name: "Legacy", online: true },
  ] }))
  await page.route((url) => url.pathname === "/api/projects", (route) => {
    const node = new URL(route.request().url()).searchParams.get("node")
    return route.fulfill({
      json: node === "birch"
        ? [projectRow("/srv/birch-app", [{ harness: "codex" }], { last_used: "2026-10-26T10:30:00Z" })]
        : node === "legacy"
          ? [projectRow("/srv/legacy-app", [{ harness: "codex" }], { last_used: "2026-10-25T10:30:00Z" })]
          : [projectRow("/work/local-app", [{ harness: "codex" }], { last_used: "2026-10-27T10:30:00Z" })],
    })
  })
  await page.route((url) => url.pathname === "/api/projects/files", (route) => {
    const q = new URL(route.request().url()).searchParams
    const node = q.get("node")
    const directory = q.get("directory")
    fileRequests.push({ node, directory })
    if (node === "legacy") return route.fulfill({ status: 404, body: "404 page not found\n" })
    if (node === "birch") return route.fulfill({ json: { files: ["cmd/birch/main.go", "docs/deploy.md"], truncated: false } })
    return route.fulfill({ json: { files: ["README.md", "src/app.ts", "src/api.ts"], truncated: false } })
  })
  await page.route(/\/api\/sessions(\?.*)?$/, (route) => {
    if (route.request().method() !== "POST") return route.fallback()
    starts.push(route.request().postDataJSON())
    return route.fulfill({ json: { ...session, id: "created-at", transport: "acp" } })
  })

  await page.goto("/sessions?new=1")
  const sheet = page.getByRole("dialog", { name: "New session" })
  const first = sheet.getByRole("textbox", { name: "First message" })
  const hints = sheet.locator('[data-slot="composer-hints"]')

  // Opening the sheet and typing text without a leading/whitespace @ never queries project files.
  await first.click()
  await first.fill("Contact dev@example.com about ")
  expect(fileRequests).toHaveLength(0)

  // Typing @ queries once for the selected folder; further filtering reuses the cached list.
  await first.pressSequentially("@sr")
  await expect(hints).toBeVisible()
  await expect(hints).toContainText("Project files")
  await expect(hints).toContainText("@src/app.ts")
  await expect(hints).toContainText("@src/api.ts")
  await expect(hints).not.toContainText("README.md")
  expect(fileRequests).toEqual([{ node: null, directory: "/work/local-app" }])

  await first.press("ArrowDown")
  await first.press("Tab")
  await expect(first).toHaveValue("Contact dev@example.com about @src/api.ts ")
  await expect(hints).toHaveCount(0)

  // Reopening @ reuses the cache; Escape dismisses only the hints menu.
  await first.pressSequentially("@re")
  await expect(hints).toContainText("@README.md")
  expect(fileRequests).toHaveLength(1)
  await first.press("Escape")
  await expect(hints).toHaveCount(0)
  await expect(sheet).toBeVisible()

  // Switching to a peer project queries that peer with ?node= and scopes hints to it.
  await sheet.getByRole("navigation", { name: "Projects" }).getByRole("button", { name: /birch-app/ }).click()
  await first.fill("Check @")
  await expect(hints).toContainText("@cmd/birch/main.go")
  await expect(hints).not.toContainText("@src/api.ts")
  expect(fileRequests).toEqual([
    { node: null, directory: "/work/local-app" },
    { node: "birch", directory: "/srv/birch-app" },
  ])

  // Old peer returning 404 stays functional, caches the fallback, and still launches.
  await sheet.getByRole("navigation", { name: "Projects" }).getByRole("button", { name: /legacy-app/ }).click()
  await first.fill("Inspect @foo")
  await expect.poll(() => fileRequests).toHaveLength(3)
  expect(fileRequests[2]).toEqual({ node: "legacy", directory: "/srv/legacy-app" })
  await expect(hints).toHaveCount(0)
  await first.pressSequentially("bar")
  expect(fileRequests).toHaveLength(3)
  await first.press("Control+Enter")
  await expect.poll(() => starts).toHaveLength(1)
  expect(starts[0]).toMatchObject({ directory: "/srv/legacy-app", first_message: "Inspect @foobar" })
})
