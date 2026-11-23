import path from "node:path";
import { defineConfig } from "@playwright/test";

const port = 3322;
const apiPort = 3324;
const root = path.resolve(__dirname, "../..");
const binary = path.join(__dirname, "node_modules/.cache/openneko-design/openneko");

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
  webServer: [
    {
      command: `go build -o '${binary}' ./cmd/openneko && exec '${binary}' web --addr 127.0.0.1:${apiPort} --dir '${path.join(__dirname, "src/spa")}' --jobs=false`,
      cwd: path.join(root, "apps/openneko"),
      env: {
        OPENNEKO_BUILTIN_SKILLS_ROOT: path.join(root, "packages/llm/assets/builtin-skills"),
        OPENNEKO_SANDBOX_SKILLS_ROOT: path.join(root, "recipes/sandbox/skills"),
        OPENNEKO_PACKS_DIR: path.join(root, "packs"),
      },
      url: `http://127.0.0.1:${apiPort}/api/v1/version`,
      reuseExistingServer: false,
      timeout: 300_000,
    },
    {
      command: `pnpm dev --host 127.0.0.1 --port ${port}`,
      env: {
        OPENNEKO_SPA_API: `http://127.0.0.1:${apiPort}`,
        OPENNEKO_MAGENTO_VISUAL_TEST: "true",
        NEXT_PUBLIC_VISUAL_TEST: "true",
        OPENNEKO_VISUAL_TEST: "true",
      },
      url: `http://127.0.0.1:${port}/admin/settings/packs`,
      reuseExistingServer: false,
      timeout: 120_000,
    },
  ],
});
