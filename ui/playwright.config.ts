import { availableParallelism } from "node:os"
import { defineConfig, devices } from "@playwright/test"

// Parallel checkouts each pick a port, since an existing preview server is reused. WebKit refuses
// some ports (4190, for one); 42xx works.
const port = process.env.E2E_PORT ?? "4173"
const origin = `http://127.0.0.1:${port}`
// Screenshots for design review, not assertions: `bun run capture`.
const captures = "review-captures.spec.ts"

export default defineConfig({
  testDir: "tests",
  fullyParallel: true,
  // Every worker runs its own browser; past half the cores they only contend (same wall time, flaky timing
  // tests), while a 4-core host keeps all four.
  workers: Math.max(4, Math.floor(availableParallelism() / 2)),
  // Budgets, not guesses: see "Test budgets" in AGENTS.md. A test that needs more is a test to rewrite.
  timeout: 8_000,
  expect: { timeout: 5_000 },
  globalTimeout: process.env.CI ? 150_000 : 90_000,
  // A cold CI runner can miss the limit on a browser's first page. Reported as flaky, so it stays visible.
  retries: process.env.CI ? 1 : 0,
  reportSlowTests: { max: 10, threshold: 2_000 },
  // The shell cache would serve one test's build to the next; caching is a deployment concern, not a behaviour under test.
  use: { baseURL: origin, reducedMotion: "reduce", serviceWorkers: "block" },
  projects: [
    // Every behaviour runs once. The other two engines cover only what can differ on them.
    { name: "chromium", testIgnore: process.env.CAPTURE ? [] : [captures], grepInvert: /@timing/, use: { ...devices["Desktop Chrome"] } },
    // Real-time budgets measure the app, so they run alone after the rest instead of competing with every worker.
    { name: "timing", workers: 1, grep: /@timing/, dependencies: ["chromium", "phone", "webkit"], use: { ...devices["Desktop Chrome"] } },
    {
      name: "phone",
      testMatch: ["shell.spec.ts"],
      use: { ...devices["Desktop Chrome"], viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true },
    },
    { name: "webkit", testMatch: ["terminal.spec.ts", "visual.spec.ts", "conversation.spec.ts", "sessions.spec.ts"], grep: /kit styling|drag|click with app mouse|wheel|long conversation starts|offline cache writes|a failed permission answer|close hides|fork opens|stopped chat resumes|chat stream stays responsive/, use: { ...devices["Desktop Safari"] } },
  ],
  webServer: {
    command: `vite preview --host 127.0.0.1 --port ${port} --strictPort`,
    url: origin,
    // Reuse a preview server that is already up, so parallel runs share one dist and CI=true does not block them.
    reuseExistingServer: true,
  },
})
