import { expect, test, type Page } from "@playwright/test"

const identity = { node: { id: "n1", name: "host-a", os: "linux", version: "0" }, auth: "password", tailscale_user: "" }
// Three machines: this one plus two peers.
const nodes = [
  { id: "n2", name: "oracle-vm", online: true },
  { id: "n3", name: "mac-studio", online: true },
]

const alpha = "github.com/acme/alpha"
const harnesses = ["claude", "codex", "cursor"].map((harness) => ({ harness, mode: "default", last_used: "2026-10-20T10:00:00Z" }))

const pin = (commit: string) => ({ module: alpha, commit, content_hash: "", cache_key: "", kind: "skill", name: "alpha", url: "", direct: true })

const record = (id: string, commit: string) => ({
  id,
  module: alpha,
  commit,
  kind: "skill",
  url: "https://github.com/acme/alpha",
  owner: "acme",
  repo: "alpha",
  cache_key: `${id}-key`,
  name: "alpha",
})

const mcp = (status: string) => ({
  name: "foo-tools",
  source: "",
  launcher: "npm",
  status,
  package: "@acme/foo-tools",
  version: "1.2.3",
  integrity: "sha512-abc",
  host: "",
  reason: "",
})

// Four folders per machine, each with three harness launches. Each folder pins alpha and runs foo-tools.
function projects(tag: string, pinCommit: string) {
  return ["alpha-app", "beta-app", "gamma-app", "delta-app"].map((name) => ({
    directory: `/srv/${tag}/${name}`,
    name,
    folder_id: "",
    available: true,
    unavailable_reason: "",
    last_used: "2026-10-20T10:00:00Z",
    launches: harnesses,
    packages: [pin(pinCommit)],
    mcp: [mcp("pinned")],
    error: "",
  }))
}

const rev = (id: string, label: string, createdAt: string, packageIds: string[]) => ({
  id,
  agentpack_id: "pk1",
  manifest: "",
  lock: "",
  created_at: createdAt,
  label,
  project: "oracle",
  package_ids: packageIds,
})

// Each machine answers the three library endpoints with its own data. `node` selects the machine.
const machines: Record<string, { packages: unknown[]; agentpacks: unknown[]; projects: unknown[] }> = {
  local: {
    packages: [record("alpha-local", "abcd1234ef56")],
    agentpacks: [],
    projects: projects("host-a", "abcd1234ef56"),
  },
  n2: {
    packages: [
      { id: "beta-old", module: "github.com/acme/beta", commit: "1111111aaaa", kind: "skill", url: "", owner: "", repo: "", cache_key: "", name: "beta" },
      { id: "beta-new", module: "github.com/acme/beta", commit: "2222222bbbb", kind: "skill", url: "", owner: "", repo: "", cache_key: "", name: "beta" },
      { id: "gamma", module: "github.com/acme/gamma", commit: "3333333cccc", kind: "skill", url: "", owner: "", repo: "", cache_key: "", name: "gamma" },
    ],
    agentpacks: [
      {
        agentpack: { id: "pk1", name: "team-pack", revisions: ["rev1", "rev2"], heads: ["rev2"] },
        revisions: [
          rev("rev2", "Add gamma", "2026-10-10T00:00:00Z", ["beta-new", "gamma"]),
          rev("rev1", "Initial", "2026-10-01T00:00:00Z", ["beta-old"]),
        ],
      },
    ],
    projects: projects("oracle-vm", "abcd1234ef56"),
  },
  n3: {
    packages: [record("alpha-n3", "9999aaaabbbb")],
    agentpacks: [],
    projects: projects("mac-studio", "abcd1234ef56"),
  },
}

function machineOf(url: string): string {
  return new URL(url).searchParams.get("node") ?? "local"
}

async function mockLibrary(page: Page) {
  await page.route("**/api/status", (route) => route.fulfill({ json: { setup_required: false, authenticated: true } }))
  await page.route("**/api/identity", (route) => route.fulfill({ json: identity }))
  await page.route("**/api/nodes", (route) => route.fulfill({ json: nodes }))
  // Aborting the stream keeps the queries on their plain fetch path.
  await page.route("**/api/events", (route) => route.abort())
  await page.route(/\/api\/library\/packages(\?.*)?$/, (route) => route.fulfill({ json: machines[machineOf(route.request().url())]?.packages }))
  await page.route(/\/api\/library\/agentpacks(\?.*)?$/, (route) => route.fulfill({ json: machines[machineOf(route.request().url())]?.agentpacks }))
  await page.route(/\/api\/projects(\?.*)?$/, (route) => route.fulfill({ json: machines[machineOf(route.request().url())]?.projects }))
}

test.beforeEach(async ({ page }) => {
  await mockLibrary(page)
})

test("Installed in lists one row per project per machine: 4 projects × 3 harnesses on 3 machines is 12 rows, not 36", async ({ page }) => {
  await page.goto(`/library/package/${alpha}`)
  await expect(page.getByText("12 projects", { exact: true })).toBeVisible()
  await expect(page.locator('[data-slot="data-grid"] tbody tr')).toHaveCount(12)
})

test("the version popup switches the shown commit and marks the other rows", async ({ page }) => {
  await page.goto(`/library/package/${alpha}`)
  const details = page.locator("dl").first()
  await expect(details).toContainText("abcd123")

  await page.getByRole("button", { name: "2 versions" }).click()
  await page.getByRole("menuitem", { name: "9999aaa" }).click()

  await expect(details).toContainText("9999aaa")
  await expect(page.getByText("Other version", { exact: true })).toHaveCount(12)
})

test("MCP inspector shows launcher, status, version and one row per project", async ({ page }) => {
  await page.goto("/library/mcp/foo-tools")
  const server = page.locator("dl").first()
  await expect(server).toContainText("npm")
  await expect(server).toContainText("Pinned")
  await expect(server).toContainText("1.2.3")
  await expect(page.getByText("12 projects", { exact: true })).toBeVisible()
  await expect(page.locator('[data-slot="data-grid"] tbody tr')).toHaveCount(12)
})

test("agentpack inspector diffs the newest revision and switches revisions from history", async ({ page }) => {
  await page.goto("/library/agentpack/pk1")
  await expect(page.getByRole("list", { name: "Changes from previous revision" }).getByRole("listitem")).toHaveText(["~beta", "+gamma"])

  await page.getByRole("button", { name: /Initial/ }).click()
  await expect(page).toHaveURL(/\/library\/agentpack\/pk1\?revision=rev1$/)
  await expect(page.getByText("First saved revision.", { exact: true })).toBeVisible()
})
