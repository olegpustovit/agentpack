import { defineConfig } from '@playwright/test';
export default defineConfig({
  testDir: 'tests',
  workers: 1,
  timeout: 60000,
  expect: {timeout: 15000},
  use: {
    baseURL: 'http://127.0.0.1:18473',
    headless: true,
    launchOptions: {executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH},
  },
  webServer: {command: 'node test-server.mjs', url: 'http://127.0.0.1:18473', reuseExistingServer: false, timeout: 120000},
  projects: [
    {name: 'desktop', use: {viewport: {width: 1440, height: 1000}}},
    {name: 'phone', use: {viewport: {width: 390, height: 844}, colorScheme: 'dark', isMobile: true, hasTouch: true}},
  ],
});
