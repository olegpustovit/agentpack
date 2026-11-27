import { expect, test, type Page } from "@playwright/test"
import { mockAPI, projectRow } from "./helpers"

const peer = { id: "peer-1", name: "Build server", online: true }

// The pending-installs list reads drafts on every Library view; mockAPI has no default for it.
async function mockLibraryBase(page: Page) {
  await page.route(/\/api\/library\/drafts(\?.*)?$/, (route) => route.fulfill({ json: [] }))
}

// With /api/events aborted, nodesQuery polls every 30 s. Advance the fake clock so online/offline changes reach the library.
async function refreshMachines(page: Page) {
  const refreshed = page.waitForResponse((response) => new URL(response.url()).pathname === "/api/nodes")
  await page.clock.runFor(31_000)
  await refreshed
}

test("Library keeps installed packages when an older peer has no Library API", async ({ page }) => {
  await mockAPI(page)
  await mockLibraryBase(page)
  await page.route("**/api/nodes*", (route) => route.fulfill({ json: [peer] }))
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
  await expect(page.locator('[data-slot="inspector"]').getByText("Build server needs an update to show the library.")).toBeVisible()
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
  await page.getByRole("option", { name: /other.*\/work\/other/ }).click()
  await page.getByLabel("Package", { exact: true }).fill("acme/skill")
  const dialog = page.getByRole("dialog")
  await dialog.getByRole("button", { name: "Review packages", exact: true }).click()
  await dialog.getByRole("button", { name: "Install 1 package", exact: true }).click()
  await expect(dialog.getByText("Installed", { exact: true })).toBeVisible()
  await dialog.getByRole("button", { name: "Done", exact: true }).click()
  expect(requests.some((value) => value.path === "/api/packages" && value.method === "POST" && value.body && typeof value.body === "object" && "spec" in value.body && value.body.spec === "acme/skill" && "directory" in value.body && value.body.directory === "/work/other")).toBeTruthy()
})

test("save, rename, preview and restore an agentpack in the Library", async ({ page }) => {
  await mockAPI(page)
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
  await page.route(/restore-preview\?/, (route) => route.fulfill({ json: { add: ["github.com/acme/new"], update: [], remove: [], mode: "default", preview_digest: "restore-current" } }))
  await page.route(/\/revisions\/rev-1\/restore(\?.*)?$/, (route) => {
    restoreBody = route.request().postDataJSON()
    return route.fulfill({ json: { restored: true } })
  })

  await page.goto("/library")
  await page.getByRole("button", { name: "Save agentpack…" }).click()
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
  await expect(page.getByRole("checkbox", { name: /Restart affected agents after restore/ })).not.toBeChecked()
  await page.getByRole("button", { name: "Restore", exact: true }).click()
  await expect(page.getByText("Restored Renamed setup in my-project")).toBeVisible()
  expect(restoreBody).toEqual({ directory: "/work/my-project", restart: false, preview_digest: "restore-current" })
})

test("an existing offline package draft requires a fresh review before apply", async ({ page }) => {
  await page.clock.install()
  await mockAPI(page)
  await mockLibraryBase(page)
  let online = false
  let ready = false
  let digest: string | undefined
  let discarded = false
  const packageRequests: { body: unknown; node: string | null }[] = []
  const item = { id: "package-1", module: "github.com/acme/tools", commit: "abcd1234", content_hash: "sha256-tree-v1:abc", kind: "plugin", name: "Tools", cache_key: "cache-key" }
  let drafts = [{
    id: "draft-1", created_at: "2026-10-28T12:00:00Z", item: `${item.module}@${item.commit}`,
    node: peer.id, directory: "/srv/project", reason: "Machine offline", folder_identity: "folder-fingerprint", expected_pins: {},
  }]
  await page.route("**/api/nodes*", (route) => route.fulfill({ json: [{ ...peer, online }] }))
  await page.route("**/api/projects**", (route) => {
    const rows = new URL(route.request().url()).searchParams.get("node") === peer.id ? [projectRow("/srv/project", [{ harness: "claude" }], { folder_id: "folder-fingerprint" })] : []
    return route.fulfill({ json: rows })
  })
  await page.route("**/api/library/packages**", (route) => route.fulfill({ json: [item] }))
  await page.route("**/api/library/agentpacks**", (route) => route.fulfill({ json: [] }))
  await page.route("**/api/library/drafts", (route) => route.fulfill({ json: drafts }))
  await page.route("**/api/library/drafts/draft-1/review", (route) =>
    route.fulfill({
      json: { draft: drafts[0], ready, pins_match: false, folder_match: true, reason: ready ? "Destination pins changed." : "Build server is offline.", diff: { add: [], update: [item.module], remove: [], mode: "default", preview_digest: digest } },
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
    if (packageRequests.length === 1) {
      digest = "install-next"
      return route.fulfill({ status: 409, json: { error: "Changed since preview" } })
    }
    return route.fulfill({ json: { installed: true } })
  })

  await page.goto("/library")
  const pending = page.getByRole("region", { name: "Pending changes" })
  await expect(pending).toBeVisible()
  await expect(pending).toContainText("Reconnecting never applies a draft.")
  await expect(pending).toContainText("Offline")

  await pending.getByRole("button", { name: "Review again" }).click()
  await expect(page.getByText("Build server is offline.", { exact: true })).toBeVisible()
  await page.keyboard.press("Escape")

  online = true
  ready = true
  await refreshMachines(page)
  await pending.getByRole("button", { name: "Review again" }).click()
  await expect(page.getByText("Destination pins changed since the draft was saved.")).toBeVisible()
  const dialog = page.getByRole("dialog")
  await expect(dialog).toContainText("Needs update")
  await expect(dialog.getByRole("button", { name: "Install", exact: true })).toBeDisabled()
  expect(packageRequests).toHaveLength(0)
  digest = "install-current"
  await dialog.getByRole("button", { name: "Review again" }).click()
  await expect(page.getByRole("checkbox", { name: /Restart affected agents after install/ })).not.toBeChecked()
  await page.getByRole("button", { name: "Install", exact: true }).click()
  await expect.poll(() => packageRequests.length).toBe(1)
  await expect(dialog).toContainText("Changed since you reviewed")
  expect(discarded).toBe(false)
  await dialog.getByRole("button", { name: "Confirm install", exact: true }).click()
  await expect.poll(() => discarded).toBe(true)
  expect(packageRequests).toEqual(
    ["install-current", "install-next"].map((preview_digest) => ({
      node: "peer-1", body: { directory: "/srv/project", spec: `${item.module}@${item.commit}`, restart: false, preview_digest },
    })),
  )
})

test("LIB-06 and LIB-11: pending changes show pack name, and a changed draft recheck requires confirming again before writing", async ({ page }) => {
  await mockAPI(page)
  const pack = { id: "pack-1", name: "Project setup", revisions: ["rev-1"], heads: ["rev-1"] }
  const revision = { id: "rev-1", agentpack_id: pack.id, manifest: "", lock: "", package_ids: [], project: "/srv/project", created_at: "2026-10-20T10:00:00Z", label: "Stable", mode: "default" }
  const draft = {
    id: "draft-1",
    item: pack.id,
    pack_id: pack.id,
    revision: revision.id,
    node: peer.id,
    directory: "/srv/project",
    reason: "Machine offline",
    folder_identity: "folder-1",
    expected_pins: {},
    created_at: "2026-10-28T12:00:00Z",
  }
  let reviewCount = 0
  let restoreCalls = 0
  await page.route("**/api/nodes*", (route) => route.fulfill({ json: [{ ...peer, online: true }] }))
  await page.route("**/api/projects**", (route) =>
    route.fulfill({ json: new URL(route.request().url()).searchParams.get("node") === peer.id ? [projectRow("/srv/project", [{ harness: "claude" }])] : [] }),
  )
  await page.route("**/api/library/packages**", (route) => route.fulfill({ json: [] }))
  await page.route(/\/api\/library\/agentpacks(\?.*)?$/, (route) => route.fulfill({ json: [{ agentpack: pack, revisions: [revision] }] }))
  await page.route(/\/api\/library\/drafts(\?.*)?$/, (route) => route.fulfill({ json: [draft] }))
  await page.route("**/api/library/drafts/draft-1/review", (route) => {
    reviewCount += 1
    const diff =
      reviewCount === 1
        ? { add: ["github.com/acme/one"], update: [], remove: [], mode: "default", preview_digest: "draft-first" }
        : { add: ["github.com/acme/one"], update: ["github.com/acme/two"], remove: [], mode: "default", preview_digest: "draft-next" }
    return route.fulfill({ json: { draft, ready: true, pins_match: true, folder_match: true, reason: "Ready to restore.", diff } })
  })
  await page.route(/\/revisions\/rev-1\/restore(\?.*)?$/, (route) => {
    restoreCalls += 1
    expect(route.request().postDataJSON()).toMatchObject({ preview_digest: "draft-next", restart: false })
    return route.fulfill({ json: { restored: true } })
  })
  await page.route("**/api/library/drafts/draft-1", (route) => route.fulfill({ json: { discarded: true } }))

  await page.goto("/library")
  const pending = page.getByRole("region", { name: "Pending changes" })
  await expect(pending).toContainText("Pending changes (1)")
  await expect(pending).toContainText("Restore Project setup · Stable")
  await expect(pending).not.toContainText("pack-1")
  await expect(pending).toContainText("Build server · /srv/project")
  await expect(pending).toContainText("Reconnecting never applies a draft.")
  await pending.getByRole("button", { name: "Discard" }).click()
  const discard = page.getByRole("alertdialog")
  await expect(discard).toContainText("Discard the pending change for Restore Project setup")
  await discard.getByRole("button", { name: "Cancel" }).click()

  await pending.getByRole("button", { name: "Review again" }).click()
  const dialog = page.getByRole("dialog")
  await expect(dialog).toContainText("Add github.com/acme/one")

  // First confirm rechecks and finds a changed diff: must NOT write, must show revised diff and require a second confirm.
  await dialog.getByRole("button", { name: "Restore", exact: true }).click()
  await expect(dialog).toContainText("Changed since you reviewed")
  await expect(dialog).toContainText("Update github.com/acme/two")
  expect(restoreCalls).toBe(0)

  // Second confirm matches the revised diff and applies.
  await dialog.getByRole("button", { name: "Confirm restore", exact: true }).click()
  await expect.poll(() => restoreCalls).toBe(1)
})

test("offline restore saves the exact agentpack revision as a draft", async ({ page }) => {
  await page.clock.install()
  await mockAPI(page)
  await mockLibraryBase(page)
  let online = true
  let draftInput: unknown
  const pack = { id: "pack-1", name: "Project setup", revisions: ["rev-1"], heads: ["rev-1"] }
  const revision = { id: "rev-1", agentpack_id: pack.id, manifest: 'name = "project"\n', lock: "", package_ids: [], project: "/srv/project", created_at: "2026-10-20T10:00:00Z", label: "Stable", mode: "default" }
  let drafts: unknown[] = []
  await page.route("**/api/nodes*", (route) => route.fulfill({ json: [{ ...peer, online }] }))
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

