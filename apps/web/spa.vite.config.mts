import { copyFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { defineConfig, type Plugin } from "vite";
import pkg from "./package.json" with { type: "json" };
import { THEME_COLOR } from "./src/lib/theme-color";
import { serverPagePattern } from "./src/spa/server-pages";

const root = path.dirname(fileURLToPath(import.meta.url));
const src = path.join(root, "src");
const outDir = path.join(root, "dist");
const apiTarget = process.env.OPENNEKO_SPA_API ?? "http://127.0.0.1:3000";
const nodeProxy = { "/api": apiTarget, "/_next": apiTarget, [serverPagePattern().source]: apiTarget };

function appIcons(): Plugin {
  return {
    name: "openneko-app-icons",
    apply: "build",
    writeBundle() {
      for (const file of ["favicon.ico", "icon.png"]) copyFileSync(path.join(src, "app", file), path.join(outDir, file));
    },
  };
}

export default defineConfig(({ mode }) => ({
  root: path.join(src, "spa"),
  publicDir: path.join(root, "public"),
  envPrefix: "NEXT_PUBLIC_",
  define: {
    "process.env.NODE_ENV": JSON.stringify(mode === "production" ? "production" : "development"),
    "process.env.NEXT_PUBLIC_APP_VERSION": JSON.stringify(pkg.version),
    "process.env.NEXT_PUBLIC_VISUAL_TEST": JSON.stringify(process.env.NEXT_PUBLIC_VISUAL_TEST ?? ""),
    "process.env.OPENNEKO_VISUAL_TEST": JSON.stringify(""),
    "process.env.OPENNEKO_MAGENTO_VISUAL_TEST": JSON.stringify(""),
  },
  esbuild: { jsx: "automatic" },
  resolve: {
    alias: [
      { find: /^next\/link$/, replacement: path.join(src, "spa/next/link.tsx") },
      { find: /^next\/image$/, replacement: path.join(src, "spa/next/image.tsx") },
      { find: /^next\/navigation$/, replacement: path.join(src, "spa/next/navigation.ts") },
      { find: /^@\//, replacement: `${src}/` },
    ],
  },
  plugins: [
    appIcons(),
    {
      name: "openneko-theme-color",
      transformIndexHtml: (html: string) => html.replace("%OPENNEKO_THEME_COLOR%", THEME_COLOR),
    },
  ],
  build: {
    outDir,
    emptyOutDir: true,
    sourcemap: false,
    rollupOptions: {
      onwarn(warning, warn) {
        if (warning.code === "MODULE_LEVEL_DIRECTIVE" || warning.code === "SOURCEMAP_ERROR") return;
        warn(warning);
      },
    },
  },
  server: { proxy: nodeProxy },
  preview: { proxy: nodeProxy },
}));
