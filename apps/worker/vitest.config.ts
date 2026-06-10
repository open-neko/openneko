import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    include: ["test/**/*.test.ts"],
    // E2E lives under test/e2e/ and runs against real LLMs + the Docker
    // stack — it's slow and burns API credits. Excluded from the default
    // suite. Run via `pnpm test:e2e` (vitest.e2e.config.ts).
    exclude: ["test/e2e/**", "node_modules/**", "dist/**"],
    // See apps/web/vitest.config.ts — one fork per file isolates @neko/db
    // pool state and avoids ordering-dependent test failures.
    pool: "forks",
    poolOptions: { forks: { singleFork: false } },
    testTimeout: 10_000,
    // Production defaults to openshell (SEC11); tests exercise the
    // in-process core. Openshell-path tests override per-test.
    env: {
      OPENNEKO_AGENT_RUNTIME: "inprocess",
      OPENNEKO_PLUGIN_RUNTIME: "microsandbox",
    },
  },
});
