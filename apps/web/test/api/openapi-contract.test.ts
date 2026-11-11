import { readdir, readFile } from "node:fs/promises";
import { join, relative } from "node:path";
import { describe, expect, it } from "vitest";
import { parse } from "yaml";
import { accessPolicyFor } from "@/lib/access-policy";

const webDirectory = join(import.meta.dirname, "..", "..");
const sourceDirectory = join(webDirectory, "src");
const apiDirectory = join(sourceDirectory, "app", "api");
const contractFile = join(webDirectory, "..", "..", "api", "openapi.yaml");
const goRoutesFile = join(webDirectory, "..", "openneko", "internal", "webfront", "api.go");
const GRAPHJIN_PATHS = new Set(["/api/v1/graphql", "/api/v1/mcp"]);
const METHODS = ["GET", "POST", "PUT", "PATCH", "DELETE"] as const;

interface Operation {
  operationId?: string;
  summary?: string;
  tags?: string[];
  "x-access"?: string;
}
type Contract = { servers: { url: string }[]; paths: Record<string, Record<string, Operation>> };

async function files(directory: string, match: (name: string) => boolean): Promise<string[]> {
  const found: string[] = [];
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    const path = join(directory, entry.name);
    if (entry.isDirectory()) found.push(...(await files(path, match)));
    else if (match(entry.name)) found.push(path);
  }
  return found;
}

function contractPath(routeFile: string): string {
  const segments = relative(join(apiDirectory, "v1"), routeFile).split("/").slice(0, -1);
  return `/${segments.map((s) => s.replace(/^\[(?:\.\.\.)?(\w+)\]$/, "{$1}")).join("/")}`;
}

async function routeOperations(): Promise<string[]> {
  const operations: string[] = [];
  for (const file of await files(apiDirectory, (name) => name === "route.ts")) {
    const source = await readFile(file, "utf8");
    for (const method of METHODS) {
      if (new RegExp(`^export (?:async )?(?:function|const) ${method}\\b`, "m").test(source)) {
        operations.push(`${method} ${contractPath(file)}`);
      }
    }
  }
  return operations.sort();
}

async function goOperations(): Promise<string[]> {
  const source = await readFile(goRoutesFile, "utf8");
  const block = /^var GoRoutes = map\[string\]\[\]string\{\n([\s\S]*?)^\}/m.exec(source)?.[1] ?? "";
  const operations: string[] = [];
  for (const [, path, methods] of block.matchAll(/^\s*"\/api\/v1(\/[^"]+)":\s*\{([^}]*)\}/gm)) {
    for (const [, method] of methods!.matchAll(/"([A-Z]+)"/g)) operations.push(`${method} ${path}`);
  }
  return operations.sort();
}

const contract = parse(await readFile(contractFile, "utf8")) as Contract;

function contractOperations(): [string, Operation][] {
  return Object.entries(contract.paths).flatMap(([path, item]) =>
    Object.entries(item).map(([method, op]) => [`${method.toUpperCase()} ${path}`, op] as [string, Operation]),
  );
}

function matches(callSegments: string[], pathSegments: string[]): boolean {
  if (pathSegments.length === 0) return callSegments.length === 0;
  const [head, ...rest] = pathSegments;
  if (head === "{path}" && rest.length === 0) return callSegments.length > 0;
  if (callSegments.length === 0) return false;
  const call = callSegments[0]!;
  const fits = call === "*" || call === head || head!.startsWith("{");
  return fits && matches(callSegments.slice(1), rest);
}

describe("OpenAPI contract", () => {
  it("serves every route under /api/v1", async () => {
    const outside = (await files(apiDirectory, (name) => name === "route.ts"))
      .filter((file) => !relative(apiDirectory, file).startsWith("v1/"));
    expect(outside).toEqual([]);
    expect(contract.servers).toEqual([{ url: "/api/v1" }]);
  });

  it("lists every route and method that Next or Go answers, and nothing else", async () => {
    const node = await routeOperations();
    const go = await goOperations();
    expect(go.length).toBeGreaterThan(20);
    expect(node.filter((op) => go.includes(op))).toEqual([]);
    expect(contractOperations().map(([key]) => key).sort()).toEqual([...node, ...go].sort());
  });

  it("gives each operation a unique id, a summary, a tag and the gate's access rule", () => {
    const ids = new Set<string>();
    for (const [key, op] of contractOperations()) {
      expect(op.operationId, key).toBeTruthy();
      expect(ids.has(op.operationId!), key).toBe(false);
      ids.add(op.operationId!);
      expect(op.summary, key).toBeTruthy();
      expect(op.tags?.length, key).toBe(1);
      const path = key.split(" ")[1]!.replace(/\{[^}]+\}/g, "x");
      expect(op["x-access"], key).toBe(accessPolicyFor(`/api/v1${path}`)?.rule);
    }
  });

  it("has the web app call only paths in the contract", async () => {
    const paths = Object.keys(contract.paths).map((path) => path.split("/").filter(Boolean));
    const sources = await files(sourceDirectory, (name) => /\.tsx?$/.test(name));
    const unknown: string[] = [];
    let calls = 0;
    for (const file of sources) {
      const name = relative(sourceDirectory, file);
      if (relative(apiDirectory, file).startsWith("v1/") || name === "lib/access-policy.ts" || name === "proxy.ts") continue;
      const source = await readFile(file, "utf8");
      for (const match of source.matchAll(/["'`(](\/api\/[^"'`?#\s]*)/g)) {
        const call = match[1]!.replace(/(\w)\$\{[^}]*\}/g, "$1").replace(/\$\{[^}]*\}/g, "*");
        if (GRAPHJIN_PATHS.has(call)) continue;
        if (!call.startsWith("/api/v1/")) {
          if (/^\/api\/(?:auth|integrations\/connect|pack-accounts)\b/.test(call)) continue;
          if (name === "lib/linkify-workspace-paths.tsx" && call === "/api/work/files/") continue;
          unknown.push(`${relative(sourceDirectory, file)}: ${match[1]}`);
          continue;
        }
        calls += 1;
        const segments = call.slice("/api/v1/".length).split("/").filter(Boolean);
        if (segments.length === 0 || segments.at(-1) === "*" && segments.length === 1) continue;
        const open = call.endsWith("/");
        if (!paths.some((path) => matches(segments, path) || (open && path.length > segments.length && matches([...segments, ...path.slice(segments.length).map(() => "*")], path)))) {
          unknown.push(`${relative(sourceDirectory, file)}: ${match[1]}`);
        }
      }
    }
    expect(calls).toBeGreaterThan(100);
    expect(unknown).toEqual([]);
  });
});
