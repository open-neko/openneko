import { readFileSync, readdirSync, statSync } from "node:fs";
import path from "node:path";
import { matchRoutes } from "react-router";
import { describe, expect, it } from "vitest";
import { deviceSignInUrl } from "@/app/device/page";
import { hrefToString } from "@/spa/next/link";
import { redirects, routes } from "@/spa/routes";

const appDir = path.resolve(import.meta.dirname, "../src/app");

function appPagePaths(): string[] {
  const out: string[] = [];
  const walk = (dir: string, parts: string[]) => {
    for (const entry of readdirSync(dir)) {
      const full = path.join(dir, entry);
      if (statSync(full).isDirectory()) {
        const next = entry.startsWith("(") && entry.endsWith(")") ? parts : [...parts, entry];
        walk(full, next);
      } else if (entry === "page.tsx") {
        const route = "/" + parts.map((p) => (p.startsWith("[") && p.endsWith("]") ? `:${p.slice(1, -1)}` : p)).join("/");
        out.push(route === "/" ? "/" : route);
      }
    }
  };
  walk(appDir, []);
  return out.sort();
}

function samplePath(pattern: string): string {
  return pattern.replace(/:([^/]+)/g, (_, name: string) => `sample-${name}`);
}

describe("static app route table", () => {
  it("gives every page under src/app a route that is not the not-found route", () => {
    for (const page of appPagePaths()) {
      const matched = matchRoutes(routes, samplePath(page));
      expect(matched, page).not.toBeNull();
      const leaf = matched![matched!.length - 1].route;
      expect(leaf.path, page).not.toBe("*");
    }
  });

  it("covers the chat, work, and redirect routes", () => {
    for (const route of [
      "/",
      "/chat",
      "/chat/t",
      "/chat/t/thread-1",
      "/chat/bots/new",
      "/chat/bots/bot-1",
      "/chat/connectors",
      "/work",
      "/work/thread-1",
      "/workflows/wf-1",
      "/admin/settings/rules/pol-1",
      "/settings/rules/pol-1",
      ...Object.keys(redirects),
      ...Object.values(redirects),
    ]) {
      const matched = matchRoutes(routes, route);
      expect(matched, route).not.toBeNull();
      const leaf = matched![matched!.length - 1].route;
      expect(leaf.path, route).not.toBe("*");
    }
  });

  it("links the admin settings cards only to real pages, and has no context repository page", () => {
    const source = readFileSync(path.join(appDir, "admin/settings/page.tsx"), "utf8");
    const hrefs = [...source.matchAll(/href:\s*"([^"]+)"/g)].map((m) => m[1]);
    expect(hrefs.length).toBeGreaterThan(0);
    expect(hrefs).not.toContain("/admin/settings/repository");
    for (const href of hrefs) {
      const matched = matchRoutes(routes, href);
      expect(matched, href).not.toBeNull();
      expect(matched![matched!.length - 1].route.path, href).not.toBe("*");
    }
    const repo = matchRoutes(routes, "/admin/settings/repository");
    expect(repo![repo!.length - 1].route.path).toBe("*");
  });

  it("loads each client page in the route table", async () => {
    for (const route of [
      "/admin/settings/models",
      "/admin/settings/workflows",
      "/admin/users",
      "/admin/users/groups/grp_1",
      "/device",
      "/integrations",
      "/chat/connectors",
      "/onboarding",
      "/admin/settings/packs/magento/operations",
    ]) {
      const matched = matchRoutes(routes, route);
      expect(matched, route).not.toBeNull();
      const leaf = matched![matched!.length - 1].route;
      expect(leaf.lazy, route).toBeTypeOf("function");
      const loaded = await (leaf.lazy as () => Promise<{ Component?: unknown }>)();
      expect(loaded.Component, route).toBeTypeOf("function");
    }
  });

  it("builds the /device sign-in returnTo URL", () => {
    expect(deviceSignInUrl("BCDF-GHJK")).toBe(
      "/signin?returnTo=%2Fdevice%3Fcode%3DBCDF-GHJK",
    );
    expect(deviceSignInUrl("")).toBe(
      "/signin?returnTo=%2Fdevice",
    );
  });

  it("falls back to NotFound for unknown paths", () => {
    const matched = matchRoutes(routes, "/definitely-not-a-page");
    expect(matched).not.toBeNull();
    expect(matched![matched!.length - 1].route.path).toBe("*");
  });

  it("formats Link href objects with query strings and a hash", () => {
    expect(hrefToString("/runs?x=1")).toBe("/runs?x=1");
    expect(hrefToString({ pathname: "/runs", query: { status: "failed", skip: undefined }, hash: "top" })).toBe("/runs?status=failed#top");
  });
});
