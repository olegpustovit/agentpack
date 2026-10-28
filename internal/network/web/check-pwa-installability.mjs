import { chromium } from "playwright";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";

const url = process.argv[2];
if (!url) throw new Error("usage: node check-pwa-installability.mjs <gateway-url>");
const executablePath = process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH;
if (!executablePath) throw new Error("set PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH to the Chrome or Chromium executable");

const profile = await mkdtemp(join(tmpdir(), "agentpack-pwa-check-"));
let context;
try {
  context = await chromium.launchPersistentContext(profile, { executablePath, headless: true });
  const page = await context.newPage();
  const cdp = await context.newCDPSession(page);
  await cdp.send("Page.enable");
  await page.goto(url, { waitUntil: "networkidle" });
  const { installabilityErrors = [] } = await cdp.send("Page.getInstallabilityErrors");
  if (installabilityErrors.length) throw new Error(JSON.stringify(installabilityErrors, null, 2));
  console.log(`PWA installability checks passed for ${url}`);
} finally {
  await context?.close();
  await rm(profile, { recursive: true, force: true });
}
