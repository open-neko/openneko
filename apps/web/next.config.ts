import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  transpilePackages: ["@neko/db", "@neko/llm"],
};

export default nextConfig;
