import { mkdirSync, readFileSync } from "node:fs"
import { dirname, join } from "node:path"
import { fileURLToPath } from "node:url"
import { expect, test, type Locator, type Page } from "@playwright/test"
import { conversationImage, mockAPI, session } from "./helpers"

const conversationFixture = readFileSync(
  join(dirname(fileURLToPath(import.meta.url)), "../../testdata/acp/full.jsonl"),
  "utf8",
)

// Writes review screenshots only; no pixel assertions. Runs once, on desktop Chromium.
test.skip(({ browserName, isMobile }) => browserName !== "chromium" || isMobile, "captures run on the desktop chromium project only")

const dir = "/tmp/t71-screens"
const views: { name: string; path: string; ready: (page: Page) => Locator }[] = [
  { name: "sessions", path: "/sessions", ready: (page) => page.getByRole("heading", { level: 1, name: "Sessions" }) },
  { name: "conversation", path: "/sessions/local/local-one", ready: (page) => page.locator('[data-slot="agent-message"]').first() },
  { name: "library", path: "/library", ready: (page) => page.getByRole("navigation", { name: "Library" }) },
  { name: "machines", path: "/machines/peer-1", ready: (page) => page.getByRole("heading", { level: 1, name: "Build server" }) },
  { name: "topology", path: "/machines/topology", ready: (page) => page.getByRole("heading", { level: 1, name: "Topology" }) },
  { name: "usage", path: "/usage", ready: (page) => page.locator('[data-slot="usage-hero"]') },
  { name: "limits", path: "/usage/limits", ready: (page) => page.getByRole("list", { name: "Limits" }).getByRole("listitem").first() },
  { name: "general", path: "/settings/general", ready: (page) => page.getByRole("link", { name: "Sharing" }) },
  { name: "vault", path: "/settings/connections", ready: (page) => page.getByRole("button", { name: "Sign in to Cursor CLI…" }) },
]
const widths = [1440, 1024, 390]
const themes = ["light", "dark"] as const

for (const view of views) {
  for (const width of widths) {
    for (const theme of themes) {
      test(`capture ${view.name} ${width} ${theme}`, async ({ page }) => {
        await mockAPI(page, { fleet: view.name !== "conversation" })
        // The shared mock answers unknown endpoints with {}; the library list maps over drafts.
        await page.route("**/api/library/drafts", (route) => route.fulfill({ json: [] }))
        if (view.name === "usage") {
          const hour = Math.floor(Date.now() / 3_600_000) * 3600
          await page.route("**/api/usage?**", (route) => route.fulfill({ json: { hourly: [{
            hour, node_id: "gateway", harness: "claude", folder_id: "alpha", model: "",
            turns: 3, sessions: 1, input_tokens: 12_000, output_tokens: 4_000,
            cache_read_tokens: 0, cache_write_tokens: 0, cost_micros: 0, agent_ms: 0,
          }], sessions: [], samples: [] } }))
        }
        if (view.name === "vault") {
          await page.route("**/api/agents/shared", (route) => route.fulfill({ json: { agents: [{ harness: "cursor", state: "expired" }] } }))
        }
        if (view.name === "conversation") {
          await page.route("**/api/sessions", (route) => {
            if (route.request().method() !== "GET") return route.fallback()
            return route.fulfill({ json: [{ ...session, transport: "acp" }] })
          })
          await page.route("**/api/sessions/*/conversation**", (route) =>
            route.fulfill({ body: conversationFixture, contentType: "application/x-ndjson" }),
          )
          await page.route("**/api/sessions/*/conversation/config-options", (route) => route.fulfill({ json: [
            { type: "select", id: "mode", name: "Permissions", currentValue: "manual", options: [{ value: "manual", name: "Manual" }, { value: "acceptEdits", name: "Accept edits" }, { value: "plan", name: "Plan" }, { value: "bypass", name: "Bypass" }] },
            { type: "select", id: "model", name: "Model", currentValue: "opus", options: [{ value: "opus", name: "Opus 5.5" }, { value: "sonnet", name: "Sonnet 5.5" }, { value: "haiku", name: "Haiku 5.5" }] },
            { type: "select", id: "effort", name: "Effort", currentValue: "high", options: [{ value: "low", name: "Low" }, { value: "medium", name: "Medium" }, { value: "high", name: "High" }] },
          ] }))
          await page.route("**/api/sessions/*/blobs/**", (route) =>
            route.fulfill({ body: conversationImage, contentType: "image/png" }),
          )
        }
        await page.setViewportSize({ width, height: 900 })
        await page.emulateMedia({ colorScheme: theme })
        await page.goto(view.path)
        await expect(view.ready(page)).toBeVisible()
        if (view.name !== "conversation") await expect(page.getByRole("status", { name: "Loading" })).toHaveCount(0)
        mkdirSync(dir, { recursive: true })
        await page.screenshot({ path: `${dir}/${view.name}-${width}-${theme}.png` })
      })
    }
  }
}

