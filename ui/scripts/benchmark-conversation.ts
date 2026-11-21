import { chromium, webkit } from "@playwright/test"
import { identity, session } from "../tests/helpers"

const urls = process.argv.slice(2)
if (urls.length === 0) throw new Error("Pass one or more local Vite preview URLs.")
const turns = 120
const chat = (label: string) => Array.from({ length: turns }, (_, index) => [
  { seq: index * 3 + 1, ts: index * 3, kind: "prompt", payload: { text: { text: `${label} question ${index}` } } },
  { seq: index * 3 + 2, ts: index * 3 + 1, kind: "agent_message_chunk", payload: { text: { text: `${label} reply ${index}` } } },
  { seq: index * 3 + 3, ts: index * 3 + 2, kind: "turn_end", payload: {} },
]).flat().map((entry) => JSON.stringify(entry)).join("\n")
const snapshot = [chat("Alpha"), chat("Bravo")]
const sessions = ["Alpha", "Bravo"].map((name, index) => ({
  ...session, id: `bench-${index}`, name, transport: "acp",
  state: { ...session.state, running: false }, ended_at: session.started_at,
}))

for (const url of urls) {
  for (const engine of [chromium, webkit]) {
    const browser = await engine.launch()
    const samples: number[] = []
    let mounted = 0
    try {
      for (let run = 0; run < 5; run++) {
        const page = await browser.newPage({ viewport: { width: 1280, height: 800 }, serviceWorkers: "block" })
        if (engine === chromium) {
          const cdp = await page.context().newCDPSession(page)
          await cdp.send("Emulation.setCPUThrottlingRate", { rate: 4 })
        }
        await page.route("**/api/**", (route) => {
          const address = new URL(route.request().url())
          const conversation = address.pathname.match(/^\/api\/sessions\/bench-(\d)\/conversation$/)
          if (conversation) return route.fulfill({ body: snapshot[Number(conversation[1])], contentType: "application/x-ndjson" })
          switch (address.pathname) {
            case "/api/status": return route.fulfill({ json: { claimed: true, legacy: false, email: "owner@example.com", authenticated: true } })
            case "/api/config": return route.fulfill({ json: { ui_build_id: "benchmark", conversation_config_log: true } })
            case "/api/identity": return route.fulfill({ json: identity })
            case "/api/sessions": return route.fulfill({ json: sessions })
            case "/api/events": return route.abort()
            default: return route.fulfill({ json: [] })
          }
        })
        await page.goto(new URL("/sessions/local/bench-0", url).href)
        await page.getByText("Alpha reply 119", { exact: true }).waitFor({ timeout: 10_000 }).catch(async (error: unknown) => {
          throw new Error(`${engine.name()} did not open the fixture: ${(await page.locator("body").innerText()).slice(-600)}`, { cause: error })
        })
        await page.evaluate(() => {
          document.addEventListener("click", () => {
            performance.mark("bench-switch")
            const check = () => {
              const scroll = document.querySelector('[aria-label="Conversation"]')
              const reply = Array.from(scroll?.querySelectorAll("p") ?? []).find((node) => node.textContent === "Bravo reply 119")
              if (scroll && reply && reply.getBoundingClientRect().bottom <= scroll.getBoundingClientRect().bottom) {
                performance.measure("bench-visible", "bench-switch")
              } else requestAnimationFrame(check)
            }
            requestAnimationFrame(check)
          }, { capture: true, once: true })
        })
        const target = page.getByRole("link", { name: /Bravo/ })
        await target.evaluate((link) => (link as HTMLAnchorElement).click())
        await page.waitForFunction(() => performance.getEntriesByName("bench-visible").length > 0, undefined, { timeout: 10_000 })
        samples.push(await page.evaluate(() => performance.getEntriesByName("bench-visible")[0]!.duration))
        mounted = await page.locator('[aria-label="Conversation"]:visible p').count()
        await page.close()
      }
    } finally {
      await browser.close()
    }
    samples.sort((left, right) => left - right)
    console.log(JSON.stringify({ url, browser: engine.name(), medianMs: Math.round(samples[2]!), mountedParagraphs: mounted }))
  }
}
