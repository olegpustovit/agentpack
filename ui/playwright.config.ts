import { defineConfig, devices } from "@playwright/test"

const origin = "http://127.0.0.1:4173"

export default defineConfig({
  testDir: "tests",
  use: { baseURL: origin },
  projects: [
    { name: "chromium", use: { ...devices["Desktop Chrome"] } },
    { name: "webkit", use: { ...devices["Desktop Safari"] } },
  ],
  webServer: {
    command: "vite preview --host 127.0.0.1 --port 4173 --strictPort",
    url: origin,
    reuseExistingServer: !process.env.CI,
  },
})
