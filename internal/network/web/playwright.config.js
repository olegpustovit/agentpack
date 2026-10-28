import { defineConfig } from '@playwright/test';
const chromiumPath = process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH;
export default defineConfig({
  testDir: 'tests',
  workers: 1,
  timeout: 60000,
  expect: {timeout: 15000},
  use: {baseURL: 'http://127.0.0.1:18473', headless: true},
  webServer: {command: 'node test-server.mjs', url: 'http://127.0.0.1:18473', reuseExistingServer: false, timeout: 120000},
  projects: [
    {name: 'desktop-chromium', use: {browserName: 'chromium', viewport: {width: 1440, height: 1000}, launchOptions: chromiumPath ? {executablePath: chromiumPath} : {}}},
    {name: 'phone-chromium', grep: /390px switches/, use: {browserName: 'chromium', viewport: {width: 390, height: 844}, colorScheme: 'dark', isMobile: true, hasTouch: true, launchOptions: chromiumPath ? {executablePath: chromiumPath} : {}}},
    {name: 'desktop-webkit', use: {browserName: 'webkit', viewport: {width: 1440, height: 1000}}},
  ],
});
