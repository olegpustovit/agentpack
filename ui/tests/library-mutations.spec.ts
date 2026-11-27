import { expect, test, type Page, type Route } from "@playwright/test"
import { session } from "./helpers"

const identity = { node: { id: "n1", name: "host-a", os: "linux", version: "0" }, auth: "password", tailscale_user: "" }
const alpha = "github.com/acme/alpha"
const alphaPin = `${alpha}@abcd1234ef56`

// Mutable per test: whether the peer is online.
let peerOnline = true

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
const peer = { packages: [] as unknown[], agentpacks: [] as unknown[], projects: [{
  ...project("/srv/oracle/app", "folder-oracle"),
  packages: [{ ...pkg, module: "github.com/acme/baseline", name: "baseline", commit: "abc1000", cache_key: "baseline-key" }],
}] }
const machines: Record<string, { packages: unknown[]; agentpacks: unknown[]; projects: unknown[] }> = { local, n2: peer }

function machineOf(url: string): string {
  return new URL(url).searchParams.get("node") ?? "local"
}

async function mockGateway(page: Page) {
  calls.length = 0
  peerOnline = true
  await page.route("**/api/config", (route) => route.fulfill({ json: { ui_build_id: "test-build" } }))
  await page.route("**/api/status", (route) => route.fulfill({ json: { claimed: true, legacy: false, email: "owner@example.com", authenticated: true } }))
  await page.route("**/api/identity", (route) => route.fulfill({ json: identity }))
  await page.route("**/api/nodes", (route) => route.fulfill({ json: [{ id: "n2", name: "oracle-vm", online: peerOnline }] }))
  await page.route("**/api/topology", (route) => route.fulfill({ json: { self: { id: "n1", name: "host-a", os: "linux", relay: "" }, peers: [] } }))
  await page.route("**/api/events", (route) => route.abort())
  await page.route(/\/api\/sessions(\?.*)?$/, (route) => route.fulfill({ json: [] }))
  await page.route(/\/api\/library\/packages(\?.*)?$/, (route) => route.fulfill({ json: machines[machineOf(route.request().url())]?.packages }))
  await page.route(/\/api\/library\/agentpacks(\?.*)?$/, (route) => route.fulfill({ json: machines[machineOf(route.request().url())]?.agentpacks }))
  await page.route(/\/api\/projects(\?.*)?$/, (route) => route.fulfill({ json: machines[machineOf(route.request().url())]?.projects }))
  await page.route(/\/api\/packages(\?.*)?$/, (route) => {
    record(route)
    return route.fulfill({ json: { installed: true } })
  })
  await page.route(/\/restore-preview\?/, (route) => {
    record(route)
    return route.fulfill({ json: { add: ["github.com/acme/gamma"], update: [], remove: [], mode: "default", preview_digest: "restore-current" } })
  })
  await page.route(/\/api\/library\/agentpacks\/[^/]+\/revisions\/[^/]+\/restore(\?.*)?$/, (route) => {
    record(route)
    return route.fulfill({ json: { restored: true } })
  })
}

test.beforeEach(async ({ page }) => {
  await mockGateway(page)
})

test("a package inspector version installs only after confirmation on the selected project", async ({ page }) => {
  await page.goto("/library/package/github.com/acme/alpha")
  await page.getByRole("button", { name: "Install abcd123…" }).click()
  await page.getByRole("dialog", { name: "Choose a project" }).getByRole("link", { name: "two · host-a" }).click()
  const install = page.getByRole("button", { name: "Install alpha" })
  await expect(install).toBeVisible()
  expect(calls).toEqual([])
  await install.click()
  await expect.poll(() => calls[0]).toEqual({
    method: "POST", path: "/api/packages", node: null,
    body: { directory: "/srv/local/two", spec: alphaPin, restart: false },
  })
})

test("LIB-10: Add package reviews before writes, retains results, and retries only failed packages", async ({ page }) => {
  const pkgB = { ...pkg, id: "beta-local", module: "github.com/acme/beta", name: "beta", commit: "111122223333" }
  const pkgC = { ...pkg, id: "gamma-local", module: "github.com/acme/gamma", name: "gamma", commit: "444455556666" }
  await page.route(/\/api\/library\/packages(\?.*)?$/, (route) =>
    route.fulfill({ json: machineOf(route.request().url()) === "local" ? [pkg, pkgB, pkgC] : [] }),
  )
  let failBeta = true
  let firstProjectAvailable = true
  await page.route(/\/api\/projects(\?.*)?$/, (route) =>
    route.fulfill({
      json: machineOf(route.request().url()) === "local"
        ? local.projects.filter((project) => firstProjectAvailable || !project.directory.endsWith("/one"))
        : [],
    }),
  )
  await page.route(/\/api\/packages(\?.*)?$/, (route) => {
    const call = record(route)
    const body = call.body as { spec?: string } | null
    if (body?.spec?.includes("beta") && failBeta) {
      firstProjectAvailable = false
      return route.fulfill({ status: 500, json: { error: "Checksum mismatch on beta package download." } })
    }
    return route.fulfill({ json: { installed: true } })
  })

  await page.goto("/library?add=1")
  const dialog = page.getByRole("dialog")
  await dialog.getByRole("checkbox", { name: /^alpha/ }).click()
  await dialog.getByRole("checkbox", { name: /^beta/ }).click()
  await dialog.getByRole("checkbox", { name: /^gamma/ }).click()
  await dialog.getByRole("button", { name: "Review packages", exact: true }).click()
  const review = dialog.getByRole("list", { name: "Packages to install" })
  await expect(review.getByRole("listitem")).toHaveCount(3)
  await expect(review).toContainText(alphaPin)
  await expect(review).toContainText("host-a · /srv/local/one")
  await expect(dialog.getByRole("checkbox", { name: "Restart affected agents now" })).not.toBeChecked()
  expect(calls).toEqual([])
  await dialog.getByRole("button", { name: "Install 3 packages" }).click()

  await expect(dialog).toContainText("2 installed · 1 failed")
  await expect(dialog.getByText("Installed", { exact: true })).toHaveCount(2)
  await expect(dialog).toContainText("Couldn't install: Checksum mismatch on beta package download.")
  expect(calls).toHaveLength(3)

  failBeta = false
  calls.length = 0
  await dialog.getByRole("button", { name: "Retry failed (1)" }).click()
  await expect(dialog.getByText("Installed", { exact: true })).toHaveCount(3)
  expect(calls).toHaveLength(1)
  expect(calls[0]?.body).toMatchObject({ spec: expect.stringContaining("beta"), directory: "/srv/local/one", restart: false })
  await dialog.getByRole("button", { name: "Done", exact: true }).click()
  await expect(dialog).toBeHidden()
})

test("the project picker leaves offline machines out", async ({ page }) => {
  peerOnline = false
  await page.goto("/library/package/github.com/acme/alpha")
  await page.getByRole("button", { name: "Install…" }).click()
  await expect(page.getByRole("dialog", { name: "Choose a project" }).getByRole("link")).toHaveText([
    "one · host-a", "three · host-a", "two · host-a",
  ])
})

test("restore requires a server-reviewed digest and a new confirmation after conflict", async ({ page }) => {
  await page.route(/\/api\/sessions(\?.*)?$/, (route) =>
    route.fulfill({ json: [
      { ...session, id: "affected", name: "Build review", directory: "/srv/oracle/app", node_id: "n2" },
      { ...session, id: "elsewhere", name: "Other session", directory: "/srv/local/one" },
    ] }),
  )
  let digest: string | undefined
  let changed = false
  await page.route(/\/restore-preview\?/, (route) =>
    route.fulfill({ json: { add: ["github.com/acme/gamma"], update: changed ? [alpha] : [], remove: [], preview_digest: digest } }),
  )
  await page.route(/\/revisions\/rev1\/restore(\?.*)?$/, (route) => {
    record(route)
    if (changed) return route.fulfill({ json: { restored: true } })
    changed = true
    digest = "restore-next"
    return route.fulfill({ status: 409, json: { error: "Changed since preview" } })
  })
  await page.goto("/library/agentpack/pk1")
  await page.getByRole("button", { name: /Initial/ }).click()
  await page.getByRole("button", { name: "Restore…" }).click()

  const dialog = page.getByRole("dialog")
  await dialog.getByRole("radio", { name: /one/ }).check()
  await expect(dialog.getByText("Add github.com/acme/gamma")).toBeVisible()
  await expect(dialog).toContainText("Needs update")
  await expect(dialog.getByRole("button", { name: "Restore", exact: true })).toBeDisabled()
  digest = "restore-current"
  await dialog.getByRole("radio", { name: /app/ }).check()
  await expect(dialog.getByRole("radio", { checked: true })).toHaveCount(1)
  await expect(dialog.getByRole("radio", { name: /app/ })).toBeChecked()
  await expect(dialog.getByRole("radio", { name: /one/ })).not.toBeChecked()
  const affected = dialog.getByRole("region", { name: "Affected sessions" })
  await expect(affected.getByRole("listitem")).toHaveText(["Build review"])
  await expect(dialog.getByRole("checkbox", { name: /Restart affected agents/ })).not.toBeChecked()

  await dialog.getByRole("button", { name: "Restore", exact: true }).click()
  await expect(dialog).toContainText("Changed since you reviewed")
  await expect(dialog).toContainText(`Update ${alpha}`)
  const restores = () => calls.filter((call) => call.path.endsWith("/restore"))
  expect(restores()).toHaveLength(1)
  await dialog.getByRole("button", { name: "Confirm restore", exact: true }).click()
  await expect(page.getByText("Restored team-pack")).toBeVisible()
  expect(restores()).toMatchObject(
    ["restore-current", "restore-next"].map((preview_digest) => ({
      method: "POST", node: "n2", body: { directory: "/srv/oracle/app", restart: false, preview_digest },
    })),
  )
})

test("Add package… from ?add=1 posts the spec to the chosen project", async ({ page }) => {
  await page.goto("/library?add=1")

  const dialog = page.getByRole("dialog")
  await expect(dialog.getByRole("combobox", { name: "Project" })).toContainText("one")
  await dialog.getByLabel("Package", { exact: true }).fill("github.com/acme/new@abcd1234ef56")
  await dialog.getByRole("combobox", { name: "Project" }).click()
  await page.getByRole("option", { name: /oracle-vm · app/ }).click()
  await dialog.getByRole("button", { name: "Review packages", exact: true }).click()
  await expect(dialog.getByRole("checkbox", { name: "Restart affected agents now" })).not.toBeChecked()
  await dialog.getByRole("button", { name: "Install 1 package", exact: true }).click()

  await expect.poll(() => calls.length).toBe(1)
  expect(calls[0]).toEqual({
    method: "POST",
    path: "/api/packages",
    node: "n2",
    body: { directory: "/srv/oracle/app", spec: "github.com/acme/new@abcd1234ef56", restart: false },
  })
  await dialog.getByRole("button", { name: "Done", exact: true }).click()
  await expect(page).toHaveURL(/\/library$/)
})
