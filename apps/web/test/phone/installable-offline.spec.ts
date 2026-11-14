import { spawn } from "node:child_process";
import { cpSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";

async function waitForController(page: Page): Promise<void> {
  await page.evaluate(async () => {
    if (!("serviceWorker" in navigator)) {
      throw new Error("serviceWorker is not supported");
    }
    await navigator.serviceWorker.ready;
    if (navigator.serviceWorker.controller) return;
    await new Promise<void>((resolve) => {
      navigator.serviceWorker.addEventListener("controllerchange", () => resolve(), { once: true });
    });
  });
}

async function freePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const srv = createServer();
    srv.listen(0, "127.0.0.1", () => {
      const addr = srv.address();
      if (!addr || typeof addr === "string") {
        srv.close();
        reject(new Error("failed to allocate port"));
        return;
      }
      const { port } = addr;
      srv.close(() => resolve(port));
    });
    srv.on("error", reject);
  });
}

test.describe("4.3 installable app and offline shell", () => {
  test.use({ viewport: { width: 400, height: 860 } });

  test("serves manifest and worker with right headers, meets Chromium install criteria, never caches API answers, and loads offline", async ({
    page,
    request,
  }) => {
    const manifestRes = await request.get("/manifest.webmanifest");
    expect(manifestRes.status()).toBe(200);
    expect(manifestRes.headers()["content-type"]).toBe("application/manifest+json; charset=utf-8");
    expect(manifestRes.headers()["cache-control"]).toBe("no-cache");
    const manifestJson = (await manifestRes.json()) as {
      name: string;
      start_url: string;
      display: string;
      icons: Array<{ src: string; sizes: string; type: string }>;
    };
    expect(manifestJson.name).toBe("OpenNeko");
    expect(manifestJson.start_url).toBe("/chat");
    expect(manifestJson.display).toBe("standalone");

    for (const icon of ["/icon-192.png", "/icon-512.png"]) {
      const iconRes = await request.get(icon);
      expect(iconRes.status()).toBe(200);
      expect(iconRes.headers()["content-type"]).toContain("image/png");
    }

    const swRes = await request.get("/sw.js");
    expect(swRes.status()).toBe(200);
    expect(swRes.headers()["content-type"]).toBe("application/javascript; charset=utf-8");
    expect(swRes.headers()["cache-control"]).toBe("no-cache");

    const pageErrors: string[] = [];
    page.on("pageerror", (err) => pageErrors.push(err.message));

    await page.goto("/chat", { waitUntil: "networkidle" });
    await waitForController(page);

    const cdp = await page.context().newCDPSession(page);
    const appManifest = await cdp.send("Page.getAppManifest");
    expect(appManifest.errors).toEqual([]);
    expect(appManifest.url).toContain("/manifest.webmanifest");

    const installability = await cdp.send("Page.getInstallabilityErrors");
    expect(installability.installabilityErrors).toEqual([]);

    await page.evaluate(async () => {
      await fetch("/api/v1/version");
      await fetch("/api/v1/auth/session");
      await fetch("/api/v1/bots");
    });

    const cachedPaths = await page.evaluate(async () => {
      const keys = await caches.keys();
      const urls: string[] = [];
      for (const key of keys) {
        const cache = await caches.open(key);
        const requests = await cache.keys();
        urls.push(...requests.map((req) => new URL(req.url).pathname));
      }
      return urls;
    });
    expect(cachedPaths).toContain("/index.html");
    expect(cachedPaths).toContain("/manifest.webmanifest");
    expect(cachedPaths.filter((p) => p === "/api" || p.startsWith("/api/"))).toEqual([]);

    await page.context().setOffline(true);
    try {
      const offlineChat = await page.reload({ waitUntil: "domcontentloaded" });
      expect(offlineChat?.status()).toBe(200);
      await expect(page.getByTestId("offline-banner")).toContainText(
        "You are offline. OpenNeko will reconnect when your connection returns.",
      );
      await expect(page.getByRole("heading", { name: "Bots", exact: true })).toBeVisible();
      expect(await page.evaluate(() => window.scrollX)).toBe(0);

      const offlineWorkflows = await page.goto("/workflows", { waitUntil: "domcontentloaded" });
      expect(offlineWorkflows?.status()).toBe(200);
      await expect(page.getByTestId("offline-banner")).toBeVisible();
      await expect(page.getByRole("navigation", { name: "Primary navigation" })).toBeVisible();
      expect(pageErrors).toEqual([]);
    } finally {
      await page.context().setOffline(false);
    }

    await expect(page.getByTestId("offline-banner")).toHaveCount(0);
  });

  test("replaces the old shell on the next load when a new release is deployed", async ({ browser }) => {
    const webRoot = path.resolve(__dirname, "../..");
    const repoRoot = path.resolve(webRoot, "../..");
    const work = mkdtempSync(path.join(tmpdir(), "openneko-pwa-release-"));
    const distDir = path.join(work, "dist");
    cpSync(path.join(webRoot, "dist"), distDir, { recursive: true });

    const port = await freePort();
    const binPath = path.join(work, "openneko");
    const { execFileSync } = await import("node:child_process");
    execFileSync("go", ["build", "-o", binPath, "./cmd/openneko"], {
      cwd: path.join(repoRoot, "apps/openneko"),
      stdio: "ignore",
    });

    const srv = spawn(binPath, ["web", "--addr", `127.0.0.1:${port}`, "--dir", distDir, "--jobs=false"], {
      env: process.env,
      stdio: "ignore",
    });

    const origin = `http://127.0.0.1:${port}`;
    try {
      for (let i = 0; i < 30; i++) {
        try {
          const r = await fetch(`${origin}/api/v1/version`);
          if (r.ok) break;
        } catch {
          // Wait for server startup.
        }
        await new Promise((r) => setTimeout(r, 200));
      }

      const context = await browser.newContext({ viewport: { width: 400, height: 860 } });
      const page = await context.newPage();

      await page.goto(`${origin}/chat`, { waitUntil: "networkidle" });
      await waitForController(page);

      const initialCaches = await page.evaluate(() => caches.keys());
      expect(initialCaches.length).toBe(1);
      expect(initialCaches[0]).toMatch(/^openneko-shell-5\.0\.0-/);

      const indexPath = path.join(distDir, "index.html");
      const swPath = path.join(distDir, "sw.js");
      writeFileSync(
        indexPath,
        readFileSync(indexPath, "utf8").replace("</head>", '  <meta name="openneko-release" content="5.0.1-next" /></head>'),
      );
      writeFileSync(path.join(distDir, "version.json"), JSON.stringify({ version: "5.0.1" }));
      writeFileSync(
        swPath,
        readFileSync(swPath, "utf8").replace(/const SHELL_VERSION = "[^"]+";/, 'const SHELL_VERSION = "5.0.1-next";'),
      );

      await page.reload({ waitUntil: "networkidle" });
      await expect(page.locator('meta[name="openneko-release"]')).toHaveAttribute("content", "5.0.1-next");

      await expect
        .poll(() => page.evaluate(() => caches.keys()), { timeout: 10_000 })
        .toEqual(["openneko-shell-5.0.1-next"]);

      await context.setOffline(true);
      await page.reload({ waitUntil: "domcontentloaded" });
      await expect(page.locator('meta[name="openneko-release"]')).toHaveAttribute("content", "5.0.1-next");
      await expect(page.getByTestId("offline-banner")).toBeVisible();

      await context.close();
    } finally {
      srv.kill("SIGTERM");
      rmSync(work, { recursive: true, force: true });
    }
  });
});
