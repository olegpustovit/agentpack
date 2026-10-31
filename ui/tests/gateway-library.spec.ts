import { expect, test, type Page } from "@playwright/test"
import { mockAPI, projectRow, session } from "./helpers"

const peer = { id: "peer-1", name: "Build server", online: true }

// The pending-installs list reads drafts on every Library view; mockAPI has no default for it.
async function mockLibraryBase(page: Page) {
  await page.route(/\/api\/library\/drafts(\?.*)?$/, (route) => route.fulfill({ json: [] }))
}

// The connection popover's Refresh invalidates every query, so offline state reaches the library.
// The status bar is hidden on phones, so the refresh is done at desktop width and the viewport restored.
async function refreshMachines(page: Page) {
  const size = page.viewportSize()
  await page.setViewportSize({ width: 1280, height: 800 })
  await page.getByRole("contentinfo").getByRole("button", { name: /^Connected/ }).click()
  await page.getByRole("button", { name: "Refresh", exact: true }).click()
  await page.keyboard.press("Escape")
  if (size) await page.setViewportSize(size)
}

test("Library keeps installed packages when an older peer has no Library API", async ({ page }) => {
  await mockAPI(page)
  await mockLibraryBase(page)
  await page.route("**/api/nodes", (route) => route.fulfill({ json: [peer] }))
  await page.route("**/api/projects**", (route) => {
    const node = new URL(route.request().url()).searchParams.get("node")
    const rows = node === peer.id ? [projectRow("/srv/app", [{ harness: "claude" }], { packages: [{ module: "github.com/acme/installed", commit: "abc123", kind: "plugin", name: "Installed tools" }] })] : []
    return route.fulfill({ json: rows })
  })
  await page.route("**/api/library/packages**", (route) => route.fulfill({ json: [{ id: "saved-one", module: "github.com/acme/saved", commit: "def456", name: "Saved tools" }] }))
  await page.route("**/api/library/agentpacks**", (route) => {
    if (new URL(route.request().url()).searchParams.get("node") === peer.id) return route.fulfill({ status: 404, body: "404 page not found" })
    return route.fulfill({ json: [] })
  })
  await page.goto("/library")
  await expect(page.getByRole("link", { name: /Installed tools/ })).toBeVisible()
  await expect(page.getByRole("link", { name: /Saved tools/ })).toBeVisible()
  await page.getByRole("link", { name: /Installed tools/ }).click()
  await expect(page.getByText("github.com/acme/installed")).toBeVisible()
  await expect(page.getByRole("cell", { name: "Build server", exact: true })).toBeVisible()
  await expect(page.getByText("404 page not found")).toHaveCount(0)
  // The list shows the same banner on desktop; the inspector's copy is the one that also shows on phones.
  await expect(page.locator('[data-slot="inspector"]').getByText("Build server needs an update to show its library.")).toBeVisible()
  await expect(page.getByText("404 page not found")).toHaveCount(0)
})

// Legacy "existing package installation remains available from More" moved to Library > Add package…;
// its intent (install a package by spec into a chosen project) is kept under the legacy name.
test("existing package installation remains available from More", async ({ page }) => {
  const requests = await mockAPI(page)
  await mockLibraryBase(page)
  await page.route("**/api/projects**", (route) => route.fulfill({ json: [projectRow("/work/my-project"), projectRow("/work/other")] }))
  await page.goto("/library")
  await page.getByRole("button", { name: "Add package…" }).click()
  await page.getByRole("combobox", { name: "Project" }).click()
  await page.getByRole("option", { name: /other$/ }).click()
  await page.getByLabel("Package", { exact: true }).fill("acme/skill")
  await page.getByRole("button", { name: "Add package", exact: true }).click()
  await expect(page.getByText("Added acme/skill")).toBeVisible()
  expect(requests.some((value) => value.path === "/api/packages" && value.method === "POST" && value.body && typeof value.body === "object" && "spec" in value.body && value.body.spec === "acme/skill" && "directory" in value.body && value.body.directory === "/work/other")).toBeTruthy()
})

test("save, rename, preview and restore an agentpack from an attached session", async ({ page }) => {
  const requests = await mockAPI(page)
  await mockLibraryBase(page)
  const pack = { id: "pack-1", name: "Project setup", revisions: ["rev-1"], heads: ["rev-1"] }
  const revision = { id: "rev-1", agentpack_id: pack.id, manifest: 'name = "project"\n', lock: "", package_ids: [], project: "/work/my-project", created_at: "2026-10-20T10:00:00Z", label: "Initial", mode: "default" }
  let saved = false
  let saveBody: unknown
  let restoreBody: unknown
  let renamed: { name: string } | undefined
  await page.route("**/api/projects**", (route) => route.fulfill({ json: [projectRow("/work/my-project", [{ harness: "claude" }])] }))
  await page.route(/\/api\/library\/agentpacks(\?.*)?$/, (route) => {
    if (route.request().method() === "POST") {
      saveBody = route.request().postDataJSON()
      saved = true
      return route.fulfill({ json: { agentpack: pack, revision } })
    }
    return route.fulfill({ json: saved ? [{ agentpack: pack, revisions: [revision] }] : [] })
  })
  await page.route(/\/api\/library\/agentpacks\/pack-1$/, (route) => {
    const body: { name: string } = route.request().postDataJSON()
    renamed = body
    pack.name = body.name
    return route.fulfill({ json: pack })
  })
  await page.route(/restore-preview\?/, (route) => route.fulfill({ json: { add: ["github.com/acme/new"], update: [], remove: [], mode: "default" } }))
  await page.route(/\/revisions\/rev-1\/restore(\?.*)?$/, (route) => {
    restoreBody = route.request().postDataJSON()
    return route.fulfill({ json: { restored: true } })
  })

  await page.goto("/sessions")
  await page.getByRole("navigation", { name: "Sessions" }).locator("[data-source-row]").filter({ hasText: "Claude" }).click({ button: "right" })
  await page.getByRole("menuitem", { name: "Save agentpack…" }).click()
  await page.getByLabel("Name", { exact: true }).fill("Project setup")
  await page.getByLabel("Revision note").fill("Initial")
  await page.getByRole("button", { name: "Save", exact: true }).click()
  expect(saveBody).toMatchObject({ directory: "/work/my-project", name: "Project setup", label: "Initial", mode: "default" })

  await page.goto("/library")
  await page.getByRole("link", { name: /Project setup/ }).click()
  await page.getByRole("button", { name: "Rename…" }).click()
  await page.getByLabel("Name", { exact: true }).fill("Renamed setup")
  await page.getByRole("button", { name: "Rename", exact: true }).click()
  await expect(page.getByRole("heading", { name: "Renamed setup" })).toBeVisible()
  expect(renamed).toEqual({ name: "Renamed setup" })

  await page.getByRole("button", { name: "Restore…", exact: true }).click()
  await page.getByRole("radio").first().check()
  await expect(page.getByText("Add github.com/acme/new")).toBeVisible()
  await page.getByRole("checkbox", { name: /Restart affected agents after restore/ }).click()
  await page.getByRole("button", { name: "Restore", exact: true }).click()
  await expect(page.getByText("Restored Renamed setup in my-project")).toBeVisible()
  expect(restoreBody).toEqual({ directory: "/work/my-project", restart: false })
})

test("offline package rows save drafts and require a fresh review before apply", async ({ page }) => {
  await mockAPI(page)
  await mockLibraryBase(page)
  let online = true
  let ready = false
  let drafts: Record<string, unknown>[] = []
  let discarded = false
  const packageRequests: { body: unknown; node: string | null }[] = []
  const item = { id: "package-1", module: "github.com/acme/tools", commit: "abcd1234", content_hash: "sha256-tree-v1:abc", kind: "plugin", name: "Tools", cache_key: "cache-key" }
  await page.route("**/api/nodes", (route) => route.fulfill({ json: [{ ...peer, online }] }))
  await page.route("**/api/projects**", (route) => {
    const rows = new URL(route.request().url()).searchParams.get("node") === peer.id ? [projectRow("/srv/project", [{ harness: "claude" }], { folder_id: "folder-fingerprint" })] : []
    return route.fulfill({ json: rows })
  })
  await page.route("**/api/library/packages**", (route) => route.fulfill({ json: [item] }))
  await page.route("**/api/library/agentpacks**", (route) => route.fulfill({ json: [] }))
  await page.route(/\/api\/library\/drafts(\?.*)?$/, async (route) => {
    if (route.request().method() === "POST") {
      const input: Record<string, unknown> = route.request().postDataJSON()
      drafts = [{ ...input, id: "draft-1", created_at: "2026-10-28T12:00:00Z" }]
      return route.fulfill({ json: drafts[0] })
    }
    return route.fulfill({ json: drafts })
  })
  await page.route("**/api/library/drafts/draft-1/review", (route) =>
    route.fulfill({
      json: { draft: drafts[0], ready, pins_match: true, folder_match: true, reason: ready ? "Ready to install the exact pin." : "Build server is offline.", diff: { add: [], update: [item.module], remove: [], mode: "default" } },
    }),
  )
  await page.route("**/api/library/drafts/draft-1", (route) => {
    discarded = true
    drafts = []
    return route.fulfill({ json: { discarded: true } })
  })
  await page.route(/\/api\/packages(\?.*)?$/, (route) => {
    const url = new URL(route.request().url())
    if (route.request().method() === "GET") return route.fulfill({ json: [] })
    packageRequests.push({ body: route.request().postDataJSON(), node: url.searchParams.get("node") })
    return route.fulfill({ json: { installed: true } })
  })

  await page.goto("/library")
  await expect(page.getByRole("link", { name: /Tools/ })).toBeVisible()
  online = false
  await refreshMachines(page)
  await page.getByRole("link", { name: /Tools/ }).click()
  await page.getByRole("button", { name: "Install…" }).click()
  await page.locator("label").filter({ hasText: "Offline · install as draft" }).click()
  await page.getByRole("button", { name: "Save 1 draft" }).click()
  await page.getByRole("button", { name: "Done", exact: true }).click()
  // Phones show one pane at a time, and Pending installs lives in the list pane, so reopen the list.
  await page.goto("/library")
  await expect(page.getByRole("region", { name: "Pending installs" })).toBeVisible()
  expect(drafts[0]).toMatchObject({ item: `${item.module}@${item.commit}`, node: "peer-1", directory: "/srv/project", folder_identity: "folder-fingerprint", expected_pins: { [item.module]: { commit: item.commit, cache_key: "cache-key", content_hash: item.content_hash } } })

  await page.getByRole("button", { name: "Review…" }).click()
  await expect(page.getByText("Build server is offline.", { exact: true })).toBeVisible()
  await page.keyboard.press("Escape")

  online = true
  ready = true
  await refreshMachines(page)
  await page.getByRole("button", { name: "Review…" }).click()
  await expect(page.getByText("Ready to install the exact pin.")).toBeVisible()
  await page.getByRole("checkbox", { name: /Restart affected agents after install/ }).click()
  await page.getByRole("button", { name: "Install", exact: true }).click()
  await expect.poll(() => packageRequests.length).toBe(1)
  expect(packageRequests[0]).toMatchObject({ node: "peer-1", body: { directory: "/srv/project", spec: `${item.module}@${item.commit}`, restart: false } })
  await expect.poll(() => discarded).toBe(true)
})

test("replication requires opt-in and reports receipts per peer", async ({ page }) => {
  await mockAPI(page)
  await mockLibraryBase(page)
  let enrolled = false
  let synced = false
  const item = { id: "package-1", module: "github.com/acme/tools", commit: "abcd1234", kind: "plugin", name: "Tools" }
  await page.route("**/api/nodes", (route) => route.fulfill({ json: [peer] }))
  await page.route("**/api/settings", (route) => route.fulfill({ json: { running: true, platform: "linux", sharing_enabled: true, allowed_peers: [] } }))
  await page.route("**/api/library/replication/peers", (route) => {
    if (route.request().method() === "POST") enrolled = true
    return route.fulfill({ json: { peers: enrolled ? [{ node: peer.id, receipt_count: synced ? 1 : 0, digests: synced ? [item.id] : [] }] : [], contacts: {} } })
  })
  await page.route("**/api/library/replication/sync", (route) => {
    synced = true
    return route.fulfill({ json: { synced: true } })
  })
  await page.route("**/api/library/packages**", (route) => route.fulfill({ json: [item] }))
  await page.route("**/api/library/agentpacks**", (route) => route.fulfill({ json: [] }))
  await page.goto("/settings/replication")
  await expect(page.getByText("Both machines must enroll each other before registry objects replicate.")).toBeVisible()
  await expect(page.getByText("Not enrolled")).toBeVisible()
  await page.getByRole("button", { name: "Enroll", exact: true }).click()
  await expect(page.getByText("Enrolled. The other machine must enroll this gateway too.")).toBeVisible()
  await page.getByRole("button", { name: "Sync now" }).click()
  await expect(page.getByText("Sync complete with Build server.")).toBeVisible()
  await expect(page.getByText("1 verified object receipt · online")).toBeVisible()
})

test("offline restore saves the exact agentpack revision as a draft", async ({ page }) => {
  await mockAPI(page)
  await mockLibraryBase(page)
  let online = true
  let draftInput: unknown
  const pack = { id: "pack-1", name: "Project setup", revisions: ["rev-1"], heads: ["rev-1"] }
  const revision = { id: "rev-1", agentpack_id: pack.id, manifest: 'name = "project"\n', lock: "", package_ids: [], project: "/srv/project", created_at: "2026-10-20T10:00:00Z", label: "Stable", mode: "default" }
  let drafts: unknown[] = []
  await page.route("**/api/nodes", (route) => route.fulfill({ json: [{ ...peer, online }] }))
  await page.route("**/api/projects**", (route) => {
    const rows = new URL(route.request().url()).searchParams.get("node") === peer.id ? [projectRow("/srv/project", [{ harness: "claude" }], { folder_id: "folder-fingerprint" })] : []
    return route.fulfill({ json: rows })
  })
  await page.route("**/api/library/packages**", (route) => route.fulfill({ json: [] }))
  await page.route(/\/api\/library\/agentpacks(\?.*)?$/, (route) => route.fulfill({ json: [{ agentpack: pack, revisions: [revision] }] }))
  await page.route(/restore-preview\?/, (route) => route.fulfill({ json: { add: [], update: [], remove: [], mode: "default" } }))
  await page.route(/\/api\/library\/drafts(\?.*)?$/, async (route) => {
    if (route.request().method() === "POST") {
      draftInput = route.request().postDataJSON()
      drafts = [{ id: "draft-restore" }]
      return route.fulfill({ json: drafts[0] })
    }
    return route.fulfill({ json: drafts })
  })
  await page.route("**/api/library/replication/peers", (route) => route.fulfill({ json: { peers: [], contacts: {} } }))

  await page.goto("/library")
  await expect(page.getByRole("link", { name: /Project setup/ })).toBeVisible()
  online = false
  await refreshMachines(page)
  await page.getByRole("link", { name: /Project setup/ }).click()
  await page.getByRole("button", { name: "Restore…", exact: true }).click()
  await page.locator("label").filter({ hasText: "Machine offline · save as draft" }).click()
  await page.getByRole("button", { name: "Save draft", exact: true }).click()
  await expect.poll(() => draftInput).toBeDefined()
  expect(draftInput).toMatchObject({ item: pack.id, pack_id: pack.id, revision: revision.id, node: "peer-1", directory: "/srv/project", folder_identity: "folder-fingerprint", expected_pins: {} })
})

