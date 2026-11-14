import { readFileSync, readdirSync, statSync } from "node:fs";
import path from "node:path";
import { matchRoutes } from "react-router";
import { describe, expect, it } from "vitest";
import { hrefToString } from "@/spa/next/link";
import { redirects, routes } from "@/spa/routes";
import { serverPagePattern, serverPages } from "@/spa/server-pages";

const appDir = path.join(__dirname, "../src/app");

function nextPagePaths(dir = appDir, prefix = ""): string[] {
  const found: string[] = [];
  for (const entry of readdirSync(dir)) {
    const full = path.join(dir, entry);
    if (statSync(full).isDirectory()) {
      if (entry === "api" && dir === appDir) continue;
      const segment = /^\(.*\)$/.test(entry) ? "" : `/${entry.replace(/^\[(.+)\]$/, ":$1")}`;
      found.push(...nextPagePaths(full, prefix + segment));
    } else if (entry === "page.tsx") {
      found.push(prefix || "/");
    }
  }
  return found;
}

function nextPageFile(page: string, dir = appDir): string {
  const [head, ...rest] = page.split("/").filter(Boolean);
  if (head === undefined) {
    const file = path.join(dir, "page.tsx");
    statSync(file);
    return path.relative(appDir, file);
  }
  for (const entry of readdirSync(dir)) {
    const full = path.join(dir, entry);
    if (!statSync(full).isDirectory()) continue;
    if (/^\(.*\)$/.test(entry)) {
      try {
        return nextPageFile(page, full);
      } catch {
        continue;
      }
    }
    if (entry === head || entry === head.replace(/^:(.+)$/, "[$1]")) {
      try {
        return nextPageFile(`/${rest.join("/")}`, full);
      } catch {
        continue;
      }
    }
  }
  throw new Error(`no page file for ${page}`);
}

function concrete(page: string): string {
  return page.replace(/:[^/]+/g, "sample");
}

describe("static web app routes", () => {
  const pages = nextPagePaths().filter((page) => !page.startsWith("/dev/"));

  it("gives every Next page a route that is not the not-found route", () => {
    expect(pages.length).toBeGreaterThan(50);
    const missing = pages.filter((page) => {
      const matches = matchRoutes(routes, concrete(page)) ?? [];
      return matches.length === 0 || matches.at(-1)?.route.path === "*";
    });
    expect(missing).toEqual([]);
  });

  it("links the admin settings cards only to real pages, and has no context repository page", () => {
    const source = readFileSync(path.join(appDir, "admin/settings/page.tsx"), "utf8");
    const hrefs = [...source.matchAll(/href: "(\/admin\/settings\/[^"]+)"/g)].map((m) => m[1]!);
    expect(hrefs.length).toBeGreaterThan(5);
    expect(hrefs.filter((href) => !pages.includes(href))).toEqual([]);
    expect(pages).not.toContain("/admin/settings/repository");
    expect(serverPages).not.toContain("/admin/settings/repository");
  });

  it("loads each page that left the server list as a client page", () => {
    const ported = [
      "/profile",
      "/admin/rules",
      "/admin/rules/:policyId",
      "/admin/settings/channels",
      "/admin/settings/packs",
      "/admin/settings/signin",
      "/admin/settings/agent",
      "/admin/settings/data",
      "/admin/settings/models",
      "/admin/settings/research",
      "/admin/settings/security",
      "/admin/settings/spend",
      "/admin/settings/sso",
      "/admin",
      "/admin/access",
      "/admin/settings",
      "/admin/settings/workflows",
      "/admin/users",
      "/admin/users/groups/:groupId",
      "/admin/settings/graphjin",
    ];
    const wrong = ported.filter((page) => {
      const route = (matchRoutes(routes, concrete(page)) ?? []).at(-1)?.route;
      const source = readFileSync(path.join(appDir, nextPageFile(page)), "utf8");
      return serverPages.includes(page) || !route?.lazy || !source.startsWith('"use client"');
    });
    expect(wrong).toEqual([]);
  });

  it("lists only real Next pages as server pages", () => {
    expect(serverPages.filter((page) => !pages.includes(page))).toEqual([]);
  });

  it("passes server pages to Node and keeps ported pages in the app", () => {
    const pattern = serverPagePattern();
    expect(pattern.test("/device")).toBe(true);
    expect(pattern.test("/admin/users/groups/g1?tab=members")).toBe(false);
    expect(pattern.test("/chat")).toBe(false);
    expect(pattern.test("/device/extra")).toBe(false);
    expect(pattern.test("/admin/settings/graphjin")).toBe(false);
    expect(pattern.test("/api/v1/version")).toBe(false);
  });

  it("keeps each redirect target of the Next pages", () => {
    expect(redirects["/"]).toBe("/chat");
    expect(redirects["/approvals"]).toBe("/actions");
    expect(Object.keys(redirects).every((from) => pages.includes(from))).toBe(true);
  });
});

describe("Link href", () => {
  it("writes an object href as a path with a query and a hash", () => {
    expect(hrefToString("/runs?x=1")).toBe("/runs?x=1");
    expect(hrefToString({ pathname: "/runs", query: { status: "failed", skip: undefined }, hash: "top" })).toBe("/runs?status=failed#top");
  });
});
