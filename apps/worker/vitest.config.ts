import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    include: ["test/**/*.test.ts"],
    exclude: ["node_modules/**", "dist/**"],
    // See apps/web/vitest.config.ts — one fork per file isolates @neko/db
    // pool state and avoids ordering-dependent test failures.
    pool: "forks",
    poolOptions: { forks: { singleFork: false } },
    testTimeout: 10_000,
  },
});
