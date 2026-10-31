import { expect, test, type Page, type Route } from "@playwright/test"

const identity = { node: { id: "n1", name: "host-a", os: "linux", version: "0" }, auth: "password", tailscale_user: "" }
const alpha = "github.com/acme/alpha"
const alphaPin = `${alpha}@abcd1234ef56`

// Mutable per test: a peer that goes offline and a draft list the gateway keeps.
let peerOnline = true
let drafts: unknown[] = []

type Call = { method: string; path: string; node: string | null; body: unknown }
const calls: Call[] = []

const record = (route: Route): Call => {
  const request = route.request()
  const url = new URL(request.url())
  const call = { method: request.method(), path: url.pathname, node: url.searchParams.get("node"), body: request.postData() ? request.postDataJSON() : null }
  calls.push(call)
  return call
}

const pkg = { id: "alpha-local", module: alpha, commit: "abcd1234ef56", kind: "skill", url: "", owner: "", repo: "", cache_key: "alpha-key", name: "alpha" }

const project = (directory: string, folderId: string) => ({
  directory,
  name: directory.split("/").pop(),
  folder_id: folderId,
  available: true,
  unavailable_reason: "",
  last_used: "2026-10-20T10:00:00Z",
  launches: [{ harness: "claude", mode: "default", last_used: "2026-10-20T10:00:00Z" }],
  packages: [],
  mcp: [],
  error: "",
})

// host-a is this gateway ("local"); oracle-vm is a peer that can go offline.
const local = { packages: [pkg], agentpacks: [] as unknown[], projects: [project("/srv/local/one", "folder-one"), project("/srv/local/two", "folder-two"), project("/srv/local/three", "folder-three")] }
const pack = {
  agentpack: { id: "pk1", name: "team-pack", revisions: ["rev1", "rev2"], heads: ["rev2"] },
  revisions: [
    { id: "rev2", agentpack_id: "pk1", manifest: "", lock: "", created_at: "2026-10-10T00:00:00Z", label: "Add gamma", project: "one", package_ids: [] },
    { id: "rev1", agentpack_id: "pk1", manifest: "", lock: "", created_at: "2026-10-01T00:00:00Z", label: "Initial", project: "one", package_ids: ["alpha-local"] },
  ],
}
local.agentpacks = [pack]
const peer = { packages: [] as unknown[], agentpacks: [] as unknown[], projects: [project("/srv/oracle/app", "folder-oracle")] }
const machines: Record<string, { packages: unknown[]; agentpacks: unknown[]; projects: unknown[] }> = { local, n2: peer }

function machineOf(url: string): string {
  return new URL(url).searchParams.get("node") ?? "local"
}

async function mockGateway(page: Page) {
  calls.length = 0
  drafts = []
  peerOnline = true
  await page.route("**/api/status", (route) => route.fulfill({ json: { setup_required: false, authenticated: true } }))
  await page.route("**/api/identity", (route) => route.fulfill({ json: identity }))
  await page.route("**/api/nodes", (route) => route.fulfill({ json: [{ id: "n2", name: "oracle-vm", online: peerOnline }] }))
  await page.route("**/api/topology", (route) => route.fulfill({ json: { self: { id: "n1", name: "host-a", os: "linux", relay: "" }, peers: [] } }))
  await page.route("**/api/events", (route) => route.abort())
  await page.route(/\/api\/sessions(\?.*)?$/, (route) => route.fulfill({ json: [] }))
  await page.route(/\/api\/library\/packages(\?.*)?$/, (route) => route.fulfill({ json: machines[machineOf(route.request().url())]?.packages }))
  await page.route(/\/api\/library\/agentpacks(\?.*)?$/, (route) => route.fulfill({ json: machines[machineOf(route.request().url())]?.agentpacks }))
  await page.route(/\/api\/projects(\?.*)?$/, (route) => route.fulfill({ json: machines[machineOf(route.request().url())]?.projects }))
  await page.route(/\/api\/library\/drafts(\?.*)?$/, async (route) => {
    if (route.request().method() === "POST") {
      const body = record(route).body
      const created = { ...(typeof body === "object" && body !== null ? body : {}), id: "draft-1", created_at: "2026-10-30T12:00:00Z" }
      drafts = [...drafts, created]
      return route.fulfill({ json: created })
    }
    return route.fulfill({ json: drafts })
  })
  await page.route(/\/api\/library\/drafts\/[^/]+\/review$/, (route) => {
    record(route)
    return route.fulfill({ json: { draft: drafts[0], ready: true, pins_match: true, folder_match: true, reason: "Ready to install the exact pin.", diff: { add: [alpha], update: [], remove: [], mode: "default" } } })
  })
  await page.route(/\/api\/library\/drafts\/[^/]+$/, (route) => {
    record(route)
    drafts = []
    return route.fulfill({ json: { discarded: true } })
  })
  await page.route(/\/api\/packages(\?.*)?$/, (route) => {
    record(route)
    return route.fulfill({ json: { installed: true } })
  })
  await page.route(/\/restore-preview\?/, (route) => {
    record(route)
    return route.fulfill({ json: { add: ["github.com/acme/gamma"], update: [], remove: [], mode: "default" } })
  })
  await page.route(/\/api\/library\/agentpacks\/[^/]+\/revisions\/[^/]+\/restore(\?.*)?$/, (route) => {
    record(route)
    return route.fulfill({ json: { restored: true } })
  })
}

test.beforeEach(async ({ page }) => {
  await mockGateway(page)
})

test("two installs run at a time per machine, and nothing is sent before Install is confirmed", async ({ page }) => {
  const held: Array<() => void> = []
  let inFlight = 0
  let maxInFlight = 0
  await page.route(/\/api\/packages(\?.*)?$/, async (route) => {
    const call = record(route)
    inFlight++
    maxInFlight = Math.max(maxInFlight, inFlight)
    await new Promise<void>((release) => held.push(release))
    inFlight--
    expect(call.body).toMatchObject({ spec: alphaPin, restart: true })
    await route.fulfill({ json: { installed: true } })
  })

  await page.goto("/library/package/github.com/acme/alpha")
  await page.getByRole("button", { name: "Install…" }).click()
  await expect(page.getByRole("dialog")).toContainText("Choose at least one project")
  for (const name of ["one", "two", "three"]) await page.getByRole("checkbox", { name: new RegExp(`^${name}`) }).click()
  await expect(page.getByRole("button", { name: "Install in 3 projects" })).toBeEnabled()
  expect(calls).toEqual([])

  await page.getByRole("button", { name: "Install in 3 projects" }).click()
  await expect.poll(() => held.length).toBe(2)
  held[0]?.()
  await expect.poll(() => held.length).toBe(3)
  held[1]?.()
  held[2]?.()

  await expect(page.getByRole("button", { name: "Done" })).toBeVisible()
  expect(maxInFlight).toBe(2)
  expect(calls.map((call) => call.path)).toEqual(["/api/packages", "/api/packages", "/api/packages"])
  await expect(page.getByRole("dialog").getByText("Installed", { exact: true })).toHaveCount(3)
})

test("a row that fails keeps its error visible after the install finishes", async ({ page }) => {
  await page.route(/\/api\/packages(\?.*)?$/, (route) => {
    const call = record(route)
    const body = call.body
    if (typeof body === "object" && body !== null && "directory" in body && String(body.directory).endsWith("two")) {
      return route.fulfill({ status: 500, json: { error: "Folder is not available on this machine." } })
    }
    return route.fulfill({ json: { installed: true } })
  })

  await page.goto("/library/package/github.com/acme/alpha")
  await page.getByRole("button", { name: "Install…" }).click()
  await page.getByRole("checkbox", { name: /^one/ }).click()
  await page.getByRole("checkbox", { name: /^two/ }).click()
  await page.getByRole("button", { name: "Install in 2 projects" }).click()

  await expect(page.getByRole("button", { name: "Done" })).toBeVisible()
  await expect(page.getByRole("dialog")).toContainText("Couldn't install: Folder is not available on this machine.")
  await expect(page.getByRole("dialog").getByText("Installed", { exact: true })).toHaveCount(1)
})

test("an install into an offline machine is saved as a draft with its exact pins", async ({ page }) => {
  // The status bar, which holds the connection popover, is hidden under 760 px, so run at desktop width.
  await page.setViewportSize({ width: 1280, height: 800 })
  await page.goto("/library/package/github.com/acme/alpha")
  await expect(page.getByRole("button", { name: "Install…" })).toBeVisible()

  // The peer's projects were loaded while it was online. Going offline and refreshing keeps them, marked offline.
  peerOnline = false
  await page.getByRole("button", { name: /Connected/ }).click()
  await page.getByRole("button", { name: "Refresh" }).click()
  await expect(page.getByRole("button", { name: /1 offline/ })).toBeVisible()
  await page.keyboard.press("Escape")

  await page.getByRole("button", { name: "Install…" }).click()
  await expect(page.getByRole("dialog")).toContainText("Offline · install as draft")
  await page.getByRole("checkbox", { name: /^app/ }).click()
  await page.getByRole("button", { name: "Save 1 draft" }).click()

  await expect(page.getByRole("dialog")).toContainText("Draft saved")
  expect(calls).toEqual([
    {
      method: "POST",
      path: "/api/library/drafts",
      node: null,
      body: {
        item: alphaPin,
        node: "n2",
        directory: "/srv/oracle/app",
        reason: "Machine offline",
        folder_identity: "folder-oracle",
        expected_pins: { [alpha]: { commit: "abcd1234ef56", cache_key: "alpha-key" } },
      },
    },
  ])
})

test("restore takes one project at a time and shows the live diff for it", async ({ page }) => {
  await page.goto("/library/agentpack/pk1")
  await page.getByRole("button", { name: /Initial/ }).click()
  await page.getByRole("button", { name: "Restore…" }).click()

  const dialog = page.getByRole("dialog")
  await dialog.getByRole("radio", { name: /one/ }).check()
  await expect(dialog.getByText("Add github.com/acme/gamma")).toBeVisible()
  await dialog.getByRole("radio", { name: /app/ }).check()
  await expect(dialog.getByRole("radio", { checked: true })).toHaveCount(1)
  await expect(dialog.getByRole("radio", { name: /app/ })).toBeChecked()
  await expect(dialog.getByRole("radio", { name: /one/ })).not.toBeChecked()

  await dialog.getByRole("button", { name: "Restore", exact: true }).click()
  await expect(page.getByText("Restored team-pack")).toBeVisible()
  const restore = calls.find((call) => call.path.endsWith("/restore"))
  expect(restore).toMatchObject({ method: "POST", node: "n2", body: { directory: "/srv/oracle/app", restart: true } })
})

test("Add package… from ?add=1 posts the spec to the chosen project", async ({ page }) => {
  await page.goto("/library?add=1")

  const dialog = page.getByRole("dialog")
  await expect(dialog.getByRole("combobox", { name: "Project" })).toContainText("one")
  await dialog.getByLabel("Package").fill("github.com/acme/new@abcd1234ef56")
  await dialog.getByRole("combobox", { name: "Project" }).click()
  await page.getByRole("option", { name: /oracle-vm · app/ }).click()
  await dialog.getByRole("button", { name: "Add package", exact: true }).click()

  await expect.poll(() => calls.length).toBe(1)
  expect(calls[0]).toEqual({
    method: "POST",
    path: "/api/packages",
    node: "n2",
    body: { directory: "/srv/oracle/app", spec: "github.com/acme/new@abcd1234ef56", restart: true },
  })
  await expect(page).toHaveURL(/\/library$/)
})
