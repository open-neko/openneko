import { defineConfig } from "@playwright/test";

const baseURL = process.env.OPENNEKO_PHONE_URL;
if (!baseURL) throw new Error("set OPENNEKO_PHONE_URL to a seeded server (scripts/phone-local.sh)");

export default defineConfig({
  testDir: "./test/phone",
  fullyParallel: false,
  workers: 1,
  retries: 0,
  timeout: 60_000,
  use: {
    baseURL,
    browserName: "chromium",
    deviceScaleFactor: 1,
    colorScheme: "light",
    locale: "en-US",
    timezoneId: "UTC",
    screenshot: "only-on-failure",
  },
});
