import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    include: ["test/**/*.test.ts"],
    pool: "forks",
    poolOptions: { forks: { singleFork: true } },
    testTimeout: 10_000,
    // Production defaults to openshell (SEC11); tests exercise the
    // in-process core. Openshell-path tests override per-test.
    env: {
    },
  },
});
