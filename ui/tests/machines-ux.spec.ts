import { expect, test, type Page } from "@playwright/test"

const identity = { node: { id: "n1", name: "atlas", os: "linux", version: "0.14.0" }, auth: "password", tailscale_user: "" }
const nodes = [
  { id: "n2", name: "birch", online: true, version: "0.14.0" },
  { id: "n3", name: "cedar", online: false, version: "0.14.0" },
]
const topology = {
  self: { id: "n1", name: "atlas", os: "linux", relay: "" },
  peers: [
    { id: "n2", name: "birch", os: "linux", state: "reachable", connection: "relay", relay: "fra", rtt_ms: 82, version: "0.14.0", sessions: 1 },
    { id: "n3", name: "cedar", os: "linux", state: "offline", connection: "", relay: "", last_seen: "2026-11-24T19:00:00Z", version: "0.14.0", sessions: 0 },
  ],
}
const machineInfo = {
  version: "0.14.0",
  os: "linux",
  arch: "arm64",
  cores: 8,
  memory: 32 * 1024 ** 3,
  disk_free: 120 * 1024 ** 3,
  disk_total: 200 * 1024 ** 3,
  agents: [
    { harness: "claude", installed: true, version: "2.1.140" },
    { harness: "codex", installed: true, version: "0.52.0" },
    { harness: "cursor", installed: false },
  ],
}
async function mockMachines(page: Page) {
  await page.route("**/api/config", (route) => route.fulfill({ json: { ui_build_id: "test-build" } }))
  await page.route("**/api/status", (route) => route.fulfill({ json: { claimed: true, legacy: false, email: "owner@example.com", authenticated: true } }))
  await page.route("**/api/identity*", (route) => route.fulfill({ json: identity }))
  await page.route("**/api/nodes*", (route) => route.fulfill({ json: nodes }))
  await page.route("**/api/topology", (route) => route.fulfill({ json: topology }))
  await page.route("**/api/version/latest", (route) => route.fulfill({ json: { version: "0.15.0", url: "" } }))
  await page.route(/\/api\/machine(\?.*)?$/, (route) => route.fulfill({ json: machineInfo }))
  await page.route(/\/api\/sessions(\?.*)?$/, (route) => route.fulfill({ json: [] }))
  await page.route(/\/api\/limits(\?.*)?$/, (route) => route.fulfill({ json: { limits: [] } }))
  await page.route(/\/api\/library\/replication\/peers/, (route) => route.fulfill({ json: { peers: [{ node: "n2", mesh: true }], contacts: {} } }))
  await page.route(/\/api\/node\/update(\?.*)?$/, (route) => {
    if (route.request().method() === "POST") return route.fulfill({ json: { started: true, version: "0.15.0" } })
    return route.fulfill({ json: { state: "idle", error: "" } })
  })
  await page.route(/\/api\/agents(\?.*)?$/, (route) => route.fulfill({
    json: { agents: [
      { harness: "claude", installed: true, signed_in: true, label: "owner@example.com", sign_out: true, methods: [] },
      { harness: "codex", installed: true, signed_in: false, methods: [{ id: "oauth", label: "Browser" }] },
    ] },
  }))
  await page.route(/\/api\/worktrees(\?.*)?$/, (route) => route.fulfill({ json: { git: true, worktrees: [] } }))
  await page.route("**/api/events", (route) => route.abort())
}

test("phone detail keeps agent names and the worktree disabled reason visible", async ({ page }) => {
  await mockMachines(page)
  await page.route(/\/api\/worktrees(\?.*)?$/, (route) => route.fulfill({
    json: {
      git: true,
      worktrees: [
        { path: "/h/wt/busy", main: "/srv/team-a/web", branch: "ap/busy", head: "abc", dirty: 2, untracked: 1, unpushed: 0, merged: false, orphaned: false, sessions: ["s1"], running: 1, size: 5 * 1024 ** 2 },
      ],
    },
  }))
  await page.setViewportSize({ width: 390, height: 844 })
  await page.goto("/machines/n2")

  const agents = page.getByRole("table", { name: "Agents" })
  const claudeCell = agents.getByRole("cell", { name: "Claude", exact: true })
  await expect(claudeCell).toBeVisible()
  expect((await claudeCell.boundingBox())!.width).toBeGreaterThan(0)
  await expect(agents).toContainText("Not installed")

  const removeBtn = page.getByRole("button", { name: "Remove…" })
  await expect(removeBtn).toBeDisabled()
  await expect(page.getByText("Stop 1 running session first")).toBeVisible()
})

test("MAC-04 sign-in explains mesh sharing, surfaces polling failure, and sign-out states peers keep copies", async ({ page }) => {
  await mockMachines(page)
  await page.route(/\/api\/agents\/codex\/login(\?.*)?$/, (route) => route.fulfill({ json: { id: "lg1", state: "waiting", url: "https://auth.example.com/device", code: "ABCD-1234" } }))
  await page.route(/\/api\/agents\/codex\/login\/lg1(\?.*)?$/, (route) => route.request().method() === "DELETE" ? route.fulfill({ status: 204 }) : route.fulfill({ status: 500, body: "boom" }))
  await page.goto("/machines/n2")

  await page.getByRole("button", { name: "Sign in…" }).click()
  const dialog = page.getByRole("dialog")
  await expect(dialog).toContainText("shared with every enrolled machine")
  await dialog.getByRole("button", { name: "Sign in with Browser" }).click()
  await expect(dialog.getByText("Couldn't check the sign-in on birch.")).toBeVisible()
  await expect(dialog.getByRole("button", { name: "Retry" })).toBeVisible()
  await page.keyboard.press("Escape")

  await page.getByRole("button", { name: "Sign out…" }).click()
  const confirm = page.getByRole("alertdialog")
  await expect(confirm).toContainText("Enrolled peers keep their own copy")
})

test("MAC-05 update job keeps its progress across navigation and verifies the requested version", async ({ page }) => {
  await page.clock.install()
  await mockMachines(page)
  let currentVersion = "0.14.0"
  let updateState = "idle"
  await page.route("**/api/nodes*", (route) => route.fulfill({
    json: nodes.map((node) => node.id === "n2" ? { ...node, version: currentVersion } : node),
  }))
  await page.route(/\/api\/machine(\?.*)?$/, (route) => route.fulfill({ json: { ...machineInfo, version: currentVersion } }))
  await page.route(/\/api\/node\/update(\?.*)?$/, (route) => {
    if (route.request().method() === "POST") {
      updateState = "running"
      return route.fulfill({ json: { started: true, version: "0.15.0" } })
    }
    return route.fulfill({ json: { state: updateState, error: "", phase: updateState === "running" ? "downloading" : undefined } })
  })

  await page.goto("/machines/n2")
  await page.getByRole("button", { name: "Update to 0.15.0" }).click()
  await expect(page.getByTestId("content").getByText(/Updating birch to 0\.15\.0/)).toBeVisible()

  // Navigate to Topology in the same tab while the update is running.
  await page.getByRole("navigation", { name: "System" }).getByRole("link", { name: "Topology" }).click()
  const row = page.getByRole("table", { name: "Machines" }).getByRole("row").filter({ hasText: "birch" })
  await page.clock.fastForward("00:03")
  await expect(row).toContainText("Downloading…")

  // Version bump marks Verified.
  currentVersion = "0.15.0"
  updateState = "idle"
  await page.clock.fastForward("00:03")
  await expect(row).toContainText("0.15.0")
  await row.getByRole("link", { name: "birch" }).click()
  await expect(page.getByText("Verified birch is running 0.15.0.")).toBeVisible()
})

test("MAC-05 update timeout shows Retry and View logs", async ({ page }) => {
  await page.clock.install()
  await mockMachines(page)
  await page.goto("/machines/n2")
  await page.getByRole("button", { name: "Update to 0.15.0" }).click()
  await expect(page.getByTestId("content").getByText(/Updating birch to 0\.15\.0/)).toBeVisible()
  await page.clock.fastForward("05:00")
  await expect(page.getByTestId("content").getByText(/Timed out/)).toBeVisible()
  await expect(page.getByRole("button", { name: "Retry" })).toBeVisible()
  await expect(page.getByRole("link", { name: "View logs" })).toBeVisible()
})

test("MAC-06 distinguishes an undiscovered machine from an unknown one", async ({ page }) => {
  await mockMachines(page)
  let releaseNodes = () => {}
  const nodesGate = new Promise<void>((resolve) => { releaseNodes = resolve })
  await page.route("**/api/nodes*", async (route) => {
    await nodesGate
    return route.fulfill({ json: nodes })
  })

  await page.goto("/machines/ghost-box")
  await expect(page.getByText("Looking for ghost-box…")).toBeVisible()
  releaseNodes()
  await expect(page.getByText("Not on this mesh")).toBeVisible()
  await expect(page.getByTestId("content").getByRole("link", { name: "Machines", exact: true })).toBeVisible()
})
