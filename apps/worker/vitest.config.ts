import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    include: ["test/**/*.test.ts"],
    // E2E lives under test/e2e/ and runs against real LLMs + the Docker
    // stack — it's slow and burns API credits. Excluded from the default
    // suite. Run via `pnpm test:e2e` (vitest.e2e.config.ts).
    exclude: ["test/e2e/**", "node_modules/**", "dist/**"],
    pool: "forks",
    poolOptions: { forks: { singleFork: true } },
    testTimeout: 10_000,
  },
});
