import { readFile } from "node:fs/promises";
import { writeFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import type { SolutionPackBundle } from "@neko/packs";
import { bindPackQueries } from "../src/packs/declarative";
import { magentoOperatorReadiness, supportedMagentoDatabase, type MagentoPreflightInput } from "../src/packs/magento-preflight";
import { operatorReadinessDetail } from "../src/packs/pack-artifacts";

// The Go doctor (apps/openneko/internal/webfront/packdoctor.go) must give the
// same bound saved queries, database checks, readiness and texts. Run with
// UPDATE_PROMPT_FIXTURES=1 to rewrite the fixture.
const FIXTURE = fileURLToPath(new URL("../../openneko/internal/webfront/testdata/nodedoctor.json", import.meta.url));

const DB = { kind: "source", key: "source.shopdb", targetRef: "tbl.binding.shopdb", path: "s.yaml", hash: "h", content: { name: "shopdb", kind: "database" } };
const API = { kind: "source", key: "source.api", targetRef: "api", path: "s.yaml", hash: "h", content: { name: "api", kind: "api" } };
const query = (key: string, content: string) => ({ kind: "saved_query", key, targetRef: key, path: `${key}.gql`, hash: "h", content });
const BOUND_CASES: Array<{ sources: unknown[]; queries: unknown[]; bindings: Record<string, string> }> = [
  { sources: [DB], queries: [query("q1", "query Q($n: Int) { orders(limit: $n) { id } customers: users @database(name: \"shopdb\") { id } }")], bindings: {} },
  { sources: [DB], queries: [query("q1", "query Q { orders { id } }"), query("q2", "query R { orders { total } lines { id items @database(name: shopdb) @skip(if: false) { id } } }")], bindings: { "source.shopdb": "live_db" } },
  { sources: [DB], queries: [query("q1", "fragment F on X { items { id } ... on Y { more { id } } } query Q { ...F orders { id } }")], bindings: {} },
  { sources: [DB, API], queries: [query("q1", "query Q { orders @database(name: \"shopdb\") { id } }")], bindings: {} },
  { sources: [API], queries: [query("q1", "query Q { orders { id } }")], bindings: {} },
];

const DATABASE_VERSIONS = ["8.0.36", "8.4.3", "5.7.44", "10.6.18-MariaDB", "10.5.9-MariaDB-log", "11.4.2-MariaDB-ubu2404", "nope", " 9.1.0 "];

const READINESS_CASES: Array<{ token: string | null; customersEnabled: boolean; statuses: Record<string, number | "error"> }> = [
  { token: null, customersEnabled: false, statuses: {} },
  { token: null, customersEnabled: true, statuses: {} },
  { token: "t", customersEnabled: true, statuses: {} },
  { token: "t", customersEnabled: false, statuses: { products: 404, "source-items": 400 } },
  { token: "t", customersEnabled: true, statuses: { orders: 403 } },
  { token: "t", customersEnabled: true, statuses: { salesRules: 500, cmsPage: 401 } },
  { token: "t", customersEnabled: false, statuses: { customers: 401, orders: "error" } },
  { token: "t", customersEnabled: true, statuses: { products: 302 } },
];

const REASONS = ["ready", "integration_token_missing", "integration_token_invalid", "acl_missing", "graphjin_version_unsupported", "domain_disabled", null] as const;

describe("Go pack doctor fixture", () => {
  it("writes the doctor results that the Go doctor must match", async () => {
    const bound = BOUND_CASES.map(item => {
      const bundle = { manifest: { metadata: { id: "tbl" } }, artifacts: [...item.sources, ...item.queries] } as unknown as SolutionPackBundle;
      const result = bindPackQueries(bundle, item.bindings).bundle;
      return { ...item, bound: Object.fromEntries(result.artifacts.filter(a => a.kind === "saved_query").map(a => [a.path, a.content])) };
    });

    const databases = DATABASE_VERSIONS.map(version => {
      try {
        return { version, type: supportedMagentoDatabase(version).type, error: null };
      } catch (error) {
        return { version, type: null, error: (error as Error).message };
      }
    });

    const readiness = [];
    for (const item of READINESS_CASES) {
      const requests: string[] = [];
      const fake = (async (url: string, init: RequestInit) => {
        requests.push(`${url} ${(init.headers as Record<string, string>).authorization}`);
        const hit = Object.entries(item.statuses).find(([part]) => url.includes(`/V1/${part}`));
        if (hit?.[1] === "error") throw new Error("connection refused");
        return new Response(null, { status: hit ? hit[1] as number : 200 });
      }) as unknown as typeof fetch;
      const input = { baseUrl: "https://shop.test/", storeCode: "de store", integrationToken: item.token, customersEnabled: item.customersEnabled } as MagentoPreflightInput;
      readiness.push({ ...item, requests, result: await magentoOperatorReadiness(input, fake) });
    }

    const details = REASONS.map(reason => ({ reason, detail: operatorReadinessDetail(reason) }));

    const fixture = `${JSON.stringify({ bound, databases, readiness, details }, null, 2)}\n`;
    if (process.env.UPDATE_PROMPT_FIXTURES === "1") writeFileSync(FIXTURE, fixture);
    expect(await readFile(FIXTURE, "utf8")).toBe(fixture);
  });
});
