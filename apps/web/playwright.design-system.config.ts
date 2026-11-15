import { defineConfig } from "@playwright/test";

const port = 3322;

export default defineConfig({
  testDir: "./test/visual",
  testMatch: ["magento-design-system.spec.ts", "ui-polish.spec.ts", "library-browser.spec.ts"],
  fullyParallel: false,
  workers: 1,
  retries: 0,
  timeout: 60_000,
  use: {
    baseURL: `http://127.0.0.1:${port}`,
    browserName: "chromium",
    deviceScaleFactor: 1,
    colorScheme: "light",
    locale: "en-US",
    timezoneId: "UTC",
    screenshot: "only-on-failure",
  },
  webServer: {
    command:
      `OPENNEKO_MAGENTO_VISUAL_TEST=true ` +
      `NEXT_PUBLIC_VISUAL_TEST=true OPENNEKO_VISUAL_TEST=true ` +
      `pnpm dev --host 127.0.0.1 --port ${port}`,
    url: `http://127.0.0.1:${port}/admin/settings/packs`,
    reuseExistingServer: false,
    timeout: 120_000,
  },
});
