import path from "node:path";
import type { NextConfig } from "next";
import pkg from "./package.json";

const nextConfig: NextConfig = {
  env: {
    NEXT_PUBLIC_APP_VERSION: pkg.version,
  },
  transpilePackages: [
    "@neko/db",
    "@neko/llm",
    "@neko/secret-crypt",
    "@open-neko/plugin-install",
  ],
  // Emit a self-contained build at .next/standalone for container deploys
  // (Cloud Run). Tracing root is the monorepo root so workspace deps
  // (@neko/db, @neko/llm) get included.
  output: "standalone",
  outputFileTracingRoot: path.join(__dirname, "../.."),
  experimental: {
    // Turbopack's FS cache (default-on since Next 16.1) was serving stale
    // globals.css after edits in dev. Disabling for dev only.
    turbopackFileSystemCacheForDev: false,
  },
};

export default nextConfig;
