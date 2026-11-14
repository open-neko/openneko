import { existsSync, readFileSync } from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";
import { THEME_COLOR } from "@/lib/theme-color";
import {
  isBrowserOnline,
  reportNetworkOffline,
  reportNetworkOnline,
  subscribeOnlineStatus,
} from "@/spa/pwa";

const webRoot = path.resolve(__dirname, "..");

function pngDimensions(filePath: string): { width: number; height: number } {
  const buf = readFileSync(filePath);
  expect(buf.subarray(0, 8).toString("hex")).toBe("89504e470d0a1a0a");
  return {
    width: buf.readUInt32BE(16),
    height: buf.readUInt32BE(20),
  };
}

describe("installable static app manifest and service worker", () => {
  it("links the manifest and apple-touch-icon in the static app index.html", () => {
    const html = readFileSync(path.join(webRoot, "src/spa/index.html"), "utf8");
    expect(html).toContain('<link rel="manifest" href="/manifest.webmanifest" />');
    expect(html).toContain('<link rel="apple-touch-icon" href="/icon-192.png" />');
  });

  it("provides a valid web app manifest and matching 192x192 and 512x512 PNG icons", () => {
    const manifestPath = path.join(webRoot, "public/manifest.webmanifest");
    const manifest = JSON.parse(readFileSync(manifestPath, "utf8")) as {
      name: string;
      short_name: string;
      start_url: string;
      scope: string;
      display: string;
      background_color: string;
      theme_color: string;
      icons: Array<{ src: string; sizes: string; type: string; purpose: string }>;
    };

    expect(manifest.name).toBe("OpenNeko");
    expect(manifest.short_name).toBe("OpenNeko");
    expect(manifest.start_url).toBe("/chat");
    expect(manifest.scope).toBe("/");
    expect(manifest.display).toBe("standalone");
    expect(manifest.background_color).toBe(THEME_COLOR);
    expect(manifest.theme_color).toBe(THEME_COLOR);

    const icon192 = path.join(webRoot, "public/icon-192.png");
    const icon512 = path.join(webRoot, "public/icon-512.png");
    expect(existsSync(icon192)).toBe(true);
    expect(existsSync(icon512)).toBe(true);
    expect(pngDimensions(icon192)).toEqual({ width: 192, height: 192 });
    expect(pngDimensions(icon512)).toEqual({ width: 512, height: 512 });

    expect(manifest.icons).toEqual(
      expect.arrayContaining([
        expect.objectContaining({ src: "/icon-192.png", sizes: "192x192", type: "image/png", purpose: "any" }),
        expect.objectContaining({ src: "/icon-512.png", sizes: "512x512", type: "image/png", purpose: "any" }),
        expect.objectContaining({ src: "/icon-512.png", sizes: "512x512", type: "image/png", purpose: "maskable" }),
      ]),
    );
  });

  it("never caches API routes in the service worker and replaces old shell caches on activate", () => {
    const sw = readFileSync(path.join(webRoot, "public/sw.js"), "utf8");
    expect(sw).toContain('url.pathname === "/api" || url.pathname.startsWith("/api/")');
    expect(sw).toContain("self.skipWaiting()");
    expect(sw).toContain("self.clients.claim()");
    expect(sw).toContain("caches.delete(key)");
  });

  it("tracks online and offline state transitions", () => {
    const events: boolean[] = [];
    const unsubscribe = subscribeOnlineStatus(() => {
      events.push(isBrowserOnline());
    });
    try {
      reportNetworkOnline();
      expect(isBrowserOnline()).toBe(true);
      reportNetworkOffline();
      expect(isBrowserOnline()).toBe(false);
      reportNetworkOnline();
      expect(isBrowserOnline()).toBe(true);
      expect(events).toEqual([false, true]);
    } finally {
      unsubscribe();
    }
  });
});
