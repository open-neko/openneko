import { createHash } from "node:crypto";
import { copyFileSync, readFileSync, readdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { defineConfig, type Plugin } from "vite";
import pkg from "./package.json";
import { THEME_COLOR } from "./src/lib/theme-color";

const root = path.dirname(fileURLToPath(import.meta.url));
const outDir = path.join(root, "dist");

function appIcons(): Plugin {
  const icon = readFileSync(path.join(root, "src/app/icon.png"));
  return {
    name: "openneko-app-icons",
    transformIndexHtml(html) {
      return html.replace("__OPENNEKO_THEME_COLOR__", THEME_COLOR);
    },
    configureServer(server) {
      server.middlewares.use((req, res, next) => {
        if (req.url === "/icon.png" || req.url === "/apple-icon.png") {
          res.setHeader("Content-Type", "image/png");
          res.end(icon);
          return;
        }
        next();
      });
    },
    writeBundle() {
      copyFileSync(path.join(root, "src/app/icon.png"), path.join(outDir, "icon.png"));
      copyFileSync(path.join(root, "src/app/icon.png"), path.join(outDir, "apple-icon.png"));
      writeFileSync(path.join(outDir, "version.json"), JSON.stringify({ version: pkg.version }) + "\n");

      const assetsDir = path.join(outDir, "assets");
      const assetFiles = readdirSync(assetsDir)
        .filter((file) => /\.(?:js|css|woff2?)$/i.test(file))
        .sort()
        .map((file) => `/assets/${file}`);
      const shellAssets = [
        "/",
        "/index.html",
        "/manifest.webmanifest",
        "/icon.png",
        "/apple-icon.png",
        "/cat.png",
        ...assetFiles,
      ];
      const hash = createHash("sha256")
        .update(pkg.version)
        .update(readFileSync(path.join(outDir, "index.html")))
        .update(assetFiles.join("\n"))
        .digest("hex")
        .slice(0, 12);
      const swTemplate = path.join(root, "public", "sw.js");
      const swPath = path.join(outDir, "sw.js");
      const swSource = readFileSync(swTemplate, "utf8")
        .replace("__OPENNEKO_SHELL_VERSION__", `${pkg.version}-${hash}`)
        .replace(/const SHELL_ASSETS = \[[\s\S]*?\];/, `const SHELL_ASSETS = ${JSON.stringify(shellAssets, null, 2)};`);
      writeFileSync(swPath, swSource);
    },
  };
}

const apiTarget = process.env.OPENNEKO_SPA_API ?? "http://127.0.0.1:3000";
const apiProxy = { "/api": apiTarget };

export default defineConfig(({ mode }) => ({
  root: path.join(root, "src/spa"),
  publicDir: path.join(root, "public"),
  appType: "spa",
  plugins: [appIcons()],
  esbuild: { jsx: "automatic" },
  define: {
    "process.env.NODE_ENV": JSON.stringify(mode === "production" ? "production" : "development"),
    "process.env.NEXT_PUBLIC_APP_VERSION": JSON.stringify(pkg.version),
    "process.env.NEXT_PUBLIC_VISUAL_TEST": JSON.stringify(process.env.NEXT_PUBLIC_VISUAL_TEST ?? ""),
    "process.env.OPENNEKO_VISUAL_TEST": JSON.stringify(process.env.OPENNEKO_VISUAL_TEST ?? ""),
    "process.env.OPENNEKO_MAGENTO_VISUAL_TEST": JSON.stringify(process.env.OPENNEKO_MAGENTO_VISUAL_TEST ?? ""),
  },
  resolve: {
    alias: [
      { find: /^next\/link$/, replacement: path.join(root, "src/spa/next/link.tsx") },
      { find: /^next\/image$/, replacement: path.join(root, "src/spa/next/image.tsx") },
      { find: /^next\/navigation$/, replacement: path.join(root, "src/spa/next/navigation.ts") },
      { find: /^@\//, replacement: `${path.join(root, "src")}/` },
    ],
  },
  build: {
    outDir,
    emptyOutDir: true,
    rollupOptions: {
      onwarn(warning, warn) {
        if (warning.code === "MODULE_LEVEL_DIRECTIVE" || warning.code === "SOURCEMAP_ERROR") return;
        warn(warning);
      },
    },
  },
  server: { proxy: apiProxy },
  preview: { proxy: apiProxy },
}));
