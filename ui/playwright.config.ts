import { defineConfig, devices } from "@playwright/test"

const origin = "http://127.0.0.1:4173"

export default defineConfig({
  testDir: "tests",
  use: { baseURL: origin },
  projects: [
    { name: "chromium", use: { ...devices["Desktop Chrome"] } },
    {
      name: "phone",
      use: { ...devices["Desktop Chrome"], viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true },
    },
    { name: "webkit", use: { ...devices["Desktop Safari"] } },
  ],
  webServer: {
    command: "vite preview --host 127.0.0.1 --port 4173 --strictPort",
    url: origin,
    // Reuse a preview server that is already up, so parallel runs share one dist and CI=true does not block them.
    reuseExistingServer: true,
  },
})
