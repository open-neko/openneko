import { execFileSync, spawn, type ChildProcess } from "node:child_process";
import { cpSync, existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
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

const webRoot = path.resolve(__dirname, "../..");
const repoRoot = path.resolve(webRoot, "../..");

function openneko(work: string): string {
  const given = process.env.OPENNEKO_PHONE_BIN;
  if (given && existsSync(given)) return given;
  const bin = path.join(work, "openneko");
  execFileSync("go", ["build", "-o", bin, "./cmd/openneko"], { cwd: path.join(repoRoot, "apps/openneko"), stdio: "ignore" });
  return bin;
}

async function startWeb(bin: string, distDir: string, port: number): Promise<ChildProcess> {
  const srv = spawn(bin, ["web", "--addr", `127.0.0.1:${port}`, "--dir", distDir, "--jobs=false"], {
    env: process.env,
    stdio: "ignore",
  });
  for (let i = 0; i < 50; i++) {
    try {
      if ((await fetch(`http://127.0.0.1:${port}/api/v1/version`)).ok) return srv;
    } catch {
      // Wait for server startup.
    }
    await new Promise((r) => setTimeout(r, 200));
  }
  srv.kill("SIGKILL");
  throw new Error(`openneko web did not start on ${port}`);
}

// Offline emulation does not reach the service worker's own fetches, so each
// offline load runs with the server stopped.
async function stopWeb(srv: ChildProcess): Promise<void> {
  if (srv.exitCode !== null || srv.signalCode !== null) return;
  const exited = new Promise((r) => srv.once("exit", r));
  srv.kill("SIGTERM");
  await exited;
}

test.describe("4.3 installable app and offline shell", () => {
  test.use({ viewport: { width: 400, height: 860 } });

  test("serves manifest and worker with right headers, meets Chromium install criteria, never caches API answers, and loads offline", async ({
    browser,
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

    const work = mkdtempSync(path.join(tmpdir(), "openneko-pwa-offline-"));
    const port = await freePort();
    const origin = `http://127.0.0.1:${port}`;
    const bin = openneko(work);
    let srv = await startWeb(bin, path.join(webRoot, "dist"), port);
    const context = await browser.newContext({ baseURL: origin, viewport: { width: 400, height: 860 } });
    const page = await context.newPage();
    try {
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

      await stopWeb(srv);
      await page.context().setOffline(true);
      try {
        const offlineChat = await page.reload({ waitUntil: "domcontentloaded" });
        expect(offlineChat?.status()).toBe(200);
        expect(offlineChat?.fromServiceWorker()).toBe(true);
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
        srv = await startWeb(bin, path.join(webRoot, "dist"), port);
        await page.context().setOffline(false);
      }

      await expect(page.getByTestId("offline-banner")).toHaveCount(0);
    } finally {
      await context.close();
      await stopWeb(srv);
      rmSync(work, { recursive: true, force: true });
    }
  });

  test("replaces the old shell on the next load when a new release is deployed", async ({ browser }) => {
    const work = mkdtempSync(path.join(tmpdir(), "openneko-pwa-release-"));
    const distDir = path.join(work, "dist");
    cpSync(path.join(webRoot, "dist"), distDir, { recursive: true });

    const port = await freePort();
    const srv = await startWeb(openneko(work), distDir, port);

    const origin = `http://127.0.0.1:${port}`;
    try {

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

      await stopWeb(srv);
      await context.setOffline(true);
      const offline = await page.reload({ waitUntil: "domcontentloaded" });
      expect(offline?.fromServiceWorker()).toBe(true);
      await expect(page.locator('meta[name="openneko-release"]')).toHaveAttribute("content", "5.0.1-next");
      await expect(page.getByTestId("offline-banner")).toBeVisible();

      await context.close();
    } finally {
      await stopWeb(srv);
      rmSync(work, { recursive: true, force: true });
    }
  });
});
