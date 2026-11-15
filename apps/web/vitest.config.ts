import { defineConfig } from "vitest/config";
import { fileURLToPath } from "node:url";

const here = (rel: string) => fileURLToPath(new URL(rel, import.meta.url));

export default defineConfig({
  test: {
    include: ["test/**/*.test.ts"],
    setupFiles: [here("./test/_helpers/setup-config-isolation.ts")],
    pool: "forks",
    poolOptions: { forks: { singleFork: false } },
    testTimeout: 10_000,
    alias: {
      "@/": `${here("./src/")}`,
      "next/link": here("./src/spa/next/link.tsx"),
      "next/image": here("./src/spa/next/image.tsx"),
      "next/navigation": here("./src/spa/next/navigation.ts"),
    },
  },
});
