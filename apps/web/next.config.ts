import path from "node:path";
import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  transpilePackages: ["@neko/db", "@neko/llm"],
  // Emit a self-contained build at .next/standalone for container deploys
  // (Cloud Run). Tracing root is the monorepo root so workspace deps
  // (@neko/db, @neko/llm) get included.
  output: "standalone",
  outputFileTracingRoot: path.join(__dirname, "../.."),
};

export default nextConfig;
