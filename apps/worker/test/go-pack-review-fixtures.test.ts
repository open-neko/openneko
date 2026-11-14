import { cp, mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { loadSolutionPack, planPack, type SolutionPackBundle } from "@neko/packs";
import { resolveInputs, resolveSecrets } from "../src/packs/configuration";
import { bindPackQueries, declarativeGraphjinUpdate } from "../src/packs/declarative";
import { PackService } from "../src/packs/service";

// The Go review (apps/openneko/internal/webfront/packreview.go) must give the
// same inputs, secrets, tables, refusals and review hash. Run with
// UPDATE_PROMPT_FIXTURES=1 to rewrite the fixture.
const FIXTURE = fileURLToPath(new URL("../../openneko/internal/webfront/testdata/nodereview.json", import.meta.url));
const SERVICE_HEALTH = fileURLToPath(new URL("./fixtures/service-health", import.meta.url));

const INPUTS = [
  { key: "shop.url", type: "url", required: true },
  { key: "shop.zone", type: "timezone", default: "UTC" },
  { key: "shop.name", type: "string" },
  { key: "shop.limit", type: "integer", default: 10 },
  { key: "shop.live", type: "boolean" },
  { key: "shop.mode", type: "enum", values: ["a", "b", 3] },
  { key: "database.port", type: "integer" },
];

const INPUT_CASES: Array<Record<string, unknown>> = [
  { "shop.url": " https://x.test/// " },
  { "shop.zone": "Europe/Berlin", "shop.url": "https://x", "shop.limit": "42", "shop.live": true, "shop.mode": "b", "shop.name": "  n  ", "database.port": 5432 },
  { "shop.url": "https://x", "shop.mode": 3, "shop.limit": null, "shop.name": "" },
  { "shop.url": "https://x", "shop.other": 1 },
  {},
  { "shop.url": "ftp://x" },
  { "shop.url": "not a url" },
  { "shop.url": "   " },
  { "shop.url": "https://x", "shop.zone": "Mars/Base" },
  { "shop.url": "https://x", "shop.limit": "4.5" },
  { "shop.url": "https://x", "shop.live": "true" },
  { "shop.url": "https://x", "shop.mode": "c" },
  { "shop.url": "https://x", "database.port": 70000 },
  { "shop.url": "https://x", "shop.name": 5 },
];

const SECRETS = [
  { key: "svc.token", purpose: "graphjin_api_auth", required: true },
  { key: "svc.extra", purpose: "graphjin_api_auth", required: false },
  { key: "svc.user_access", purpose: "pack_oauth_token", required: false },
];
const OAUTH = [{ key: "conn", scope: "user", accessToken: "svc.user_access", refreshToken: "svc.user_refresh", scopes: [] }];
const STORE = { "pack.svc": { SVC_TOKEN: "stored", OTHER: "other", SVC_EXTRA: "" } };
const SECRET_CASES: Array<Record<string, unknown>> = [
  {},
  { secrets: { "svc.token": "direct" } },
  { secretRefs: { "svc.token": "OTHER" } },
  { secrets: { "svc.extra": "  " } },
  { secrets: { "svc.extra": "x" }, secretRefs: { "svc.extra": "OTHER" } },
  { secrets: { "svc.token": 5 } },
  { secrets: { "svc.token": "" } },
  { secrets: { "svc.user_access": "x" } },
  { secrets: { nope: "x" } },
  { secretRefs: { "svc.token": "MISSING" } },
];

const DB = { kind: "source", key: "source.shopdb", targetRef: "tbl.binding.shopdb", path: "s.yaml", hash: "h", content: { name: "shopdb", kind: "database" } };
const API = { kind: "source", key: "source.api", targetRef: "api", path: "s.yaml", hash: "h", content: { name: "api", kind: "api" } };
const query = (key: string, content: string) => ({ kind: "saved_query", key, targetRef: key, path: `${key}.gql`, hash: "h", content });
const TABLE_CASES: Array<{ sources: unknown[]; queries: unknown[]; bindings: Record<string, string> }> = [
  { sources: [DB], queries: [query("q1", "query Q { orders { id } customers: users @database(name: \"shopdb\") { id } }")], bindings: {} },
  { sources: [DB], queries: [query("q1", "query Q { orders { id } }"), query("q2", "query R { orders { total } lines { id items @database(name: shopdb) { id } } }")], bindings: { "source.shopdb": "live_db" } },
  { sources: [DB], queries: [query("q1", "fragment F on X { items { id } ... on Y { more { id } } } query Q { ...F orders { id } }")], bindings: {} },
  { sources: [DB, API], queries: [query("q1", "query Q { orders { id } }")], bindings: {} },
  { sources: [DB, API], queries: [query("q1", "query Q { orders @database(name: \"shopdb\") { id } }")], bindings: {} },
  { sources: [DB], queries: [query("q1", "query Q { orders @database(name: \"a\") @database(name: \"b\") { id } }")], bindings: {} },
  { sources: [DB], queries: [query("q1", "query Q { orders @database(name: \"other\") { id } }")], bindings: {} },
  { sources: [DB], queries: [query("q1", "query Q { ...Missing }")], bindings: {} },
  { sources: [DB], queries: [query("q1", "fragment A on X { ...B } fragment B on X { ...A } query Q { ...A }")], bindings: {} },
  { sources: [DB], queries: [query("q1", "mutation M { orders { id } }")], bindings: {} },
  { sources: [DB], queries: [query("q1", "query A { a } query B { b }")], bindings: {} },
  { sources: [DB], queries: [query("q1", "query { a(")], bindings: {} },
  { sources: [DB], queries: [query("q1", "query Q { orders @database(name: $x) { id } }")], bindings: {} },
  { sources: [API], queries: [query("q1", "query Q { orders { id } }")], bindings: {} },
];

const BASE_INPUTS = { "service.base_url": "https://h.test", "service.timezone": "UTC" };
const BASE_SECRETS = { "service.api_token": "t" };
type Mutation = (bundle: SolutionPackBundle) => void;
const content = (bundle: SolutionPackBundle, kind: string) => bundle.artifacts.find(a => a.kind === kind)!.content as Record<string, unknown>;
const ACTION = {
  kind: "action", key: "action.reset", targetRef: "service-health.reset", path: "actions/reset.yaml", hash: "h",
  content: { kind: "health.reset", adapter: { kind: "graphjin_api_operation", source: "service_health", operations: { reset: { operationId: "resetHealth", mutationRoot: "reset_health" } } } },
};
const CHECK_CASES: Array<{ name: string; inputs?: Record<string, unknown>; secrets?: Record<string, string>; mutate?: Mutation }> = [
  { name: "base" },
  { name: "missing secret", secrets: {} },
  { name: "ftp base url", inputs: { ...BASE_INPUTS, "service.base_url": "ftp://h.test" } },
  { name: "base url with credentials", inputs: { ...BASE_INPUTS, "service.base_url": "https://u:p@h.test" } },
  { name: "bad base url", inputs: { ...BASE_INPUTS, "service.base_url": "nope" } },
  { name: "bad workflow timezone", inputs: { ...BASE_INPUTS, "service.timezone": "Mars/Base" } },
  { name: "unknown readiness check", mutate: b => { b.manifest.health.readiness = { reporting: ["nope"] }; } },
  { name: "db check without database", mutate: b => { b.manifest.health.postInstall = ["db-read-only"]; } },
  { name: "unknown readiness signal", mutate: b => { content(b, "watcher").readinessSignals = ["other"]; } },
  { name: "extra source property", mutate: b => { content(b, "source").extra = 1; } },
  { name: "inline credential", mutate: b => { (content(b, "source").auth as Record<string, unknown>).token = "plain"; } },
  { name: "bad api key auth", mutate: b => { content(b, "source").auth = { type: "api_key", key_name: "bad name", key_value: "{{secret.service.api_token}}" }; } },
  { name: "good api key auth", mutate: b => { content(b, "source").auth = { type: "api_key", key_name: "X-Key", key_in: "query", key_value: "{{secret.service.api_token}}" }; } },
  { name: "remote spec ref", mutate: b => { content(b, "spec").components = { schemas: { a: { $ref: "https://x/y.json" } } }; } },
  { name: "write without read_only false", mutate: b => { content(b, "source").capabilities = { "api.write": true }; } },
  { name: "read_only false without write", mutate: b => { content(b, "source").read_only = false; } },
  { name: "bad capability", mutate: b => { content(b, "source").capabilities = { "api.read": "yes" }; } },
  { name: "write source", mutate: b => { Object.assign(content(b, "source"), { read_only: false, capabilities: { "api.write": true } }); } },
  { name: "read override on post", mutate: b => { content(b, "source").operations = { resetHealth: { expose_top_level: true } }; } },
  { name: "read override extra key", mutate: b => { content(b, "source").operations = { getHealthSummary: { expose_top_level: true, x: 1 } }; } },
  { name: "read override", mutate: b => { content(b, "source").operations = { getHealthSummary: { expose_top_level: true } }; } },
  { name: "action", mutate: b => { b.artifacts.push(structuredClone(ACTION) as never); } },
  { name: "action on get", mutate: b => { const a = structuredClone(ACTION); a.content.adapter.operations.reset.operationId = "getHealthSummary"; b.artifacts.push(a as never); } },
  { name: "action on missing operation", mutate: b => { const a = structuredClone(ACTION); a.content.adapter.operations.reset.operationId = "none"; b.artifacts.push(a as never); } },
  { name: "action bad root", mutate: b => { const a = structuredClone(ACTION); a.content.adapter.operations.reset.mutationRoot = "bad-root"; b.artifacts.push(a as never); } },
  { name: "action other spec", mutate: b => { const a = structuredClone(ACTION); (a.content.adapter as Record<string, unknown>).spec = "other"; b.artifacts.push(a as never); } },
  { name: "action missing source", mutate: b => { const a = structuredClone(ACTION); a.content.adapter.source = "none"; b.artifacts.push(a as never); } },
  { name: "action conflicts with read override", mutate: b => { content(b, "source").operations = { getHealthSummary: { expose_top_level: true } }; const a = structuredClone(ACTION); b.artifacts.push(a as never); const c = structuredClone(ACTION); c.key = "action.other"; c.content.adapter.operations.reset.mutationRoot = "other_root"; b.artifacts.push(c as never); } },
  { name: "metric variable kind", mutate: b => { (content(b, "metric").execution as Record<string, unknown>).variables = { from: { kind: "later" } }; } },
  { name: "metric variable template", mutate: b => { (content(b, "metric").execution as Record<string, unknown>).variables = { tz: { kind: "literal", value: "{{service.missing}}" } }; } },
  { name: "partial template", mutate: b => { content(b, "source").base_url = "https://{{service.base_url}}"; } },
  { name: "bad saved query", mutate: b => { b.artifacts.find(a => a.kind === "saved_query")!.content = "mutation { a }"; } },
];

function failure(run: () => unknown) {
  try {
    return { result: run(), error: null };
  } catch (error) {
    return { result: null, error: (error as Error).message };
  }
}

describe("Go pack review fixture", () => {
  it("writes the review results that the Go review must match", async () => {
    const root = await mkdtemp(join(tmpdir(), "go-pack-review-"));
    const xdg = process.env.XDG_CONFIG_HOME;
    try {
      await mkdir(join(root, "openneko"), { recursive: true });
      await writeFile(join(root, "openneko", "secret-key"), "go-pack-review-fixture-key\n");
      await writeFile(join(root, "openneko", "secrets.json"), JSON.stringify(STORE));
      process.env.XDG_CONFIG_HOME = root;
      const pack = join(root, "service-health");
      await cp(SERVICE_HEALTH, pack, { recursive: true });
      await mkdir(join(pack, "actions"));
      await mkdir(join(pack, "policies"));

      const inputBundle = { manifest: { inputs: INPUTS } } as unknown as SolutionPackBundle;
      const inputs = INPUT_CASES.map(supplied => ({ supplied, ...failure(() => resolveInputs(inputBundle, supplied)) }));

      const secretBundle = { manifest: { metadata: { id: "svc" }, secrets: SECRETS, oauth: OAUTH } } as unknown as SolutionPackBundle;
      const secrets = [];
      for (const request of SECRET_CASES) {
        try {
          secrets.push({ request, result: (await resolveSecrets(secretBundle, request as never)).values, error: null });
        } catch (error) {
          secrets.push({ request, result: null, error: (error as Error).message });
        }
      }

      const tables = TABLE_CASES.map(item => {
        const bundle = { manifest: { metadata: { id: "tbl" } }, artifacts: [...item.sources, ...item.queries] } as unknown as SolutionPackBundle;
        return { ...item, ...failure(() => bindPackQueries(bundle, item.bindings).tables) };
      });

      const checks = [];
      for (const item of CHECK_CASES) {
        const bundle = await loadSolutionPack(pack);
        item.mutate?.(bundle);
        const run = failure(() => declarativeGraphjinUpdate(bundle, item.inputs ?? BASE_INPUTS, item.secrets ?? BASE_SECRETS, [], {}));
        checks.push({ name: item.name, inputs: item.inputs ?? BASE_INPUTS, secrets: item.secrets ?? BASE_SECRETS,
          health: bundle.manifest.health, artifacts: bundle.artifacts, error: run.error });
      }

      const bundle = await loadSolutionPack(pack);
      const plan = planPack(bundle, []);
      const service = new PackService("org-fixture", join(root, "packs"), join(root, "uploads")) as unknown as {
        reviewHash(...args: unknown[]): string;
      };
      const HASH_CASES = [
        { operation: "install", request: { actorUserId: "user-1", secretRefs: { "service.api_token": "TOKEN" } }, inputs: BASE_INPUTS, secrets: BASE_SECRETS,
          runtime: { source: { id: "11111111-1111-1111-1111-111111111111", graphqlUrl: "http://gj.test", authMode: "jwt" }, bindings: {}, bindingHashes: {}, readiness: ["reporting"], tables: [] } },
        { operation: "upgrade", request: {}, inputs: { "service.timezone": "UTC" }, secrets: {},
          runtime: { bindings: { "source.db": "live" }, bindingHashes: { "source.db": "abc" }, readiness: [], tables: [{ name: "pack_x", table: "orders", source: "live" }] } },
      ];
      const hashes = HASH_CASES.map(item => ({ ...item, plan,
        hash: service.reviewHash(bundle, plan, item.operation, item.request, item.inputs, item.secrets, item.runtime) }));

      const fixture = `${JSON.stringify({ inputDeclarations: INPUTS, inputs, secretDeclarations: SECRETS, oauth: OAUTH, store: STORE, secrets, tables, checks, hashes }, null, 2)}\n`;
      if (process.env.UPDATE_PROMPT_FIXTURES === "1") writeFileSync(FIXTURE, fixture);
      expect(await readFile(FIXTURE, "utf8")).toBe(fixture);
    } finally {
      if (xdg === undefined) delete process.env.XDG_CONFIG_HOME;
      else process.env.XDG_CONFIG_HOME = xdg;
      await rm(root, { recursive: true, force: true });
    }
  });
});
