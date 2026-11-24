import { expect, test, type Page } from "@playwright/test"
import { mockAPI } from "./helpers"

const settings = {
  version: "18.20.2",
  schema: [
    { key: "git.enabled", tab: "interaction", group: "Git", label: "Enable Git Integration", description: "Show git branch, status, and PR information.", type: "boolean", default: true },
    { key: "steeringMode", tab: "interaction", group: "Input", label: "Steering Mode", description: "How to process queued messages while agent is working", type: "enum", values: ["all", "one-at-a-time"], default: "one-at-a-time" },
    { key: "maxHistory", tab: "interaction", group: "Input", label: "Maximum history", description: "Turns retained for recall", type: "number", default: 10 },
    { key: "defaultThinkingLevel", tab: "model", group: "Thinking", label: "Thinking Level", description: "Reasoning depth for thinking-capable models", type: "enum", values: ["low", "medium", "high", "xhigh"], default: "high" },
    { key: "cycleOrder", tab: "model", group: "Prompt", label: "Cycle Order", description: "", type: "array", default: [] },
  ],
  values: { "git.enabled": true, steeringMode: "one-at-a-time", defaultThinkingLevel: "high", cycleOrder: ["smol", "default", "slow"], modelRoles: { default: "anthropic/claude-opus-5-5:high", smol: "anthropic/claude-haiku-5-5:low" } },
  network: { defaultThinkingLevel: "xhigh", "modelRoles.plan": "openai-codex/gpt-5.5:xhigh" },
}

const model = (provider: string, id: string, name: string, thinking: string[] = ["low", "medium", "high", "xhigh", "max"]) =>
  ({ provider, kind: "chat", id, selector: `${provider}/${id}`, name, contextWindow: 1_000_000, maxTokens: 128_000, thinking, input: ["text", "image"], cost: { input: 5, output: 25 } })
const anthropic = [model("anthropic", "claude-opus-5-5", "Claude Opus 5.5"), model("anthropic", "claude-sonnet-5-5", "Claude Sonnet 5.5"), model("anthropic", "claude-haiku-5-5", "Claude Haiku 5.5")]
const codex = [model("openai-codex", "gpt-5.5", "GPT-5.5", ["minimal", "low", "medium", "high", "xhigh"])]

// A gateway with one peer. `peerModels` answers the peer's model list; undefined means a peer without the route.
async function mockOmp(page: Page, peerModels?: unknown[]) {
  const writes: { request: string; body: unknown }[] = []
  await mockAPI(page)
  await page.route("**/api/nodes*", (route) => route.fulfill({ json: [{ id: "n2", name: "laptop", online: true }] }))
  const network: Record<string, unknown> = { ...settings.network }
  await page.route("**/api/omp/settings", (route) => route.fulfill({ json: { ...settings, network } }))
  await page.route(/\/api\/omp\/settings\/[^/?]+$/, (route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname
    writes.push({ request: `${request.method()} ${path}`, body: request.postDataJSON() })
    network[decodeURIComponent(path.split("/").pop()!)] = request.postDataJSON().value
    return route.fulfill({ json: {} })
  })
  await page.route("**/api/omp/models*", (route) => {
    const peer = new URL(route.request().url()).searchParams.get("node") === "n2"
    if (!peer) return route.fulfill({ json: { models: [...anthropic, ...codex] } })
    return peerModels ? route.fulfill({ json: { models: peerModels } }) : route.fulfill({ status: 404, json: { error: "not found" } })
  })
  return writes
}

test.beforeEach(async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
})

test("a role and a switch save network-wide", async ({ page }) => {
  const writes = await mockOmp(page, anthropic)
  await page.goto("/settings/omp")
  const architect = page.getByRole("row", { name: /Architect/ })
  await expect(architect).toContainText("Not signed in on laptop · Sign in…")
  await expect(architect.getByRole("link", { name: "Sign in…" })).toHaveAttribute("href", "/machines/n2")
  await expect(page.getByRole("row", { name: /Default/ })).toContainText("All machines")

  await page.getByRole("button", { name: "Default model" }).click()
  const sheet = page.getByRole("dialog", { name: "Default model" })
  await sheet.getByRole("button", { name: "Claude Sonnet 5.5" }).click()
  await sheet.getByRole("group", { name: "Thinking" }).getByRole("button", { name: "medium" }).click()
  await sheet.getByRole("button", { name: "Use for Default" }).click()
  await expect(sheet).toBeHidden()
  await expect(page.getByRole("row", { name: /Default/ })).toContainText("Claude Sonnet 5.5")

  await page.getByRole("group", { name: "Oh My Pi settings" }).getByRole("button", { name: "Interaction" }).click()
  const setting = page.getByRole("switch", { name: "Enable Git Integration" })
  await expect(setting.locator("../../..")).toContainText("Inherited On")
  await setting.click()
  expect(writes).toHaveLength(1)
  const save = setting.locator("..").getByRole("button", { name: "Save", exact: true })
  await save.click()
  await expect(setting.locator("../../..")).toContainText("Mesh override Off")
  await expect(setting.locator("../../..")).toContainText("Effective Off")
  await expect(setting.locator("../../..")).toContainText("Saved")

  await expect.poll(() => writes).toEqual([
    { request: "PUT /api/omp/settings/modelRoles.default", body: { value: "anthropic/claude-sonnet-5-5:medium" } },
    { request: "PUT /api/omp/settings/git.enabled", body: { value: false } },
  ])
})

test("a peer without the models route reads as Needs update", async ({ page }) => {
  await mockOmp(page)
  await page.goto("/settings/omp")
  const row = page.getByRole("row", { name: /Default/ })
  await expect(row).toContainText("Needs update on laptop")
  await page.setViewportSize({ width: 320, height: 568 })
  const [role, model, thinking] = await row.getByRole("cell").all()
  const name = (await role!.boundingBox())!
  const choice = (await model!.boundingBox())!
  const level = (await thinking!.boundingBox())!
  expect(name.y + name.height).toBeLessThan(choice.y)
  expect(choice.y + choice.height).toBeLessThan(level.y)
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(320)
})

test("invalid numbers remain visible with an error after blur and send no changes", async ({ page }) => {
  const writes = await mockOmp(page)
  await page.goto("/settings/omp")
  await page.getByRole("group", { name: "Oh My Pi settings" }).getByRole("button", { name: "Interaction" }).click()
  const number = page.getByLabel("Maximum history")
  await number.fill("many")
  await number.press("Tab")
  await expect(number).toHaveValue("many")
  await expect(number).toHaveAccessibleDescription("Enter a valid number.")
  await expect(number.locator("..").getByRole("button", { name: "Save", exact: true })).toBeDisabled()
  expect(writes).toHaveLength(0)
})
