import { defineConfig, devices } from "@playwright/test"

const origin = "http://127.0.0.1:4173"
// Screenshots for design review, not assertions: `bun run capture`.
const captures = "review-captures.spec.ts"

export default defineConfig({
  testDir: "tests",
  fullyParallel: true,
  workers: "100%",
  // Budgets, not guesses: see "Test budgets" in AGENTS.md. A test that needs more is a test to rewrite.
  timeout: 8_000,
  expect: { timeout: 5_000 },
  globalTimeout: 90_000,
  reportSlowTests: { max: 10, threshold: 2_000 },
  use: { baseURL: origin },
  projects: [
    // Every behaviour runs once. The other two engines cover only what can differ on them.
    { name: "chromium", testIgnore: process.env.CAPTURE ? [] : [captures], use: { ...devices["Desktop Chrome"] } },
    {
      name: "phone",
      testMatch: ["shell.spec.ts"],
      use: { ...devices["Desktop Chrome"], viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true },
    },
    { name: "webkit", testMatch: ["terminal.spec.ts", "visual.spec.ts"], grep: /kit styling|drag|click with app mouse|wheel/, use: { ...devices["Desktop Safari"] } },
  ],
  webServer: {
    command: "vite preview --host 127.0.0.1 --port 4173 --strictPort",
    url: origin,
    // Reuse a preview server that is already up, so parallel runs share one dist and CI=true does not block them.
    reuseExistingServer: true,
  },
})
