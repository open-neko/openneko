import { mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import type { PackArtifact } from "@neko/packs";
import {
  inspectInstalledPackArtifactCurrent,
  liveSourceStateHash,
  nativeArtifactStateHash,
  packArtifactLocator,
  policyArtifactStateHash,
} from "../src/packs/artifact-state";

// The Go plan (apps/openneko/internal/packs/state.go) must read the same
// state hashes. Run with UPDATE_PROMPT_FIXTURES=1 to rewrite the fixture.
const FIXTURE = fileURLToPath(new URL("../../openneko/internal/packs/testdata/nodestate.json", import.meta.url));

const NATIVE: Array<{ kind: "metric" | "workflow" | "watcher" | "policy" | "action"; row: Record<string, unknown> }> = [
  { kind: "metric", row: { role: "owner", slug: "orders", source: "pack", title: "Orders", description: null, active: true, cadence: "daily",
    execution_mode: "deterministic", definition_json: { query: "q", map: { value: "$.n" }, list: [1, 2.5, null] }, definition_version: 2,
    definition_hash: "abc", last_refresh_status: "ok" } },
  { kind: "workflow", row: { name: "Daily", description: "", enabled: true, status: "active", goal: "g", cron: "0 7 * * *",
    cron_timezone: "UTC", cron_enabled: true, output_contract: { kind: "text" } } },
  { kind: "watcher", row: { workflow_id: "1b4e28ba-2fa1-11d2-883f-0016d3cca427", name: "Stock", description: "", enabled: true, query: "q",
    value_path: "$.a", op: "lt", threshold: 5, cadence_seconds: 300, debounce_seconds: 3600, cooldown_seconds: 0, dedupe_key: null,
    activation: "edge", variables_json: { sku: "A" }, severity: "high" } },
  { kind: "action", row: { kind: "shop.refund", description: "Refund", definition: { steps: [{ call: "x" }] }, definition_hash: "d", enabled: false } },
];

const POLICY = {
  name: "Governed changes", description: "Approval required", applies_to_kinds: ["magento.manage_catalog"], applies_to_scopes: ["external"],
  mode: "approval_required", allowed_targets: { source: "magento_operator" }, limits: { retry: "never" }, priority: 10, enabled: false,
};

const LIVE_SOURCES: Array<Record<string, unknown>> = [
  { name: "shop", kind: "api", read_only: false, access: { roles: [], tables: null, note: "", keep: false, n: 0 }, capabilities: { "api.write": true },
    specs: { shop: { base_url: "https://x", operations: { list: { allow: [] }, get: { allow: ["a"] } }, document: "x: 1" } }, password: "***" },
  { name: "db", kind: "database", read_only: true },
];

const ARTIFACTS: PackArtifact[] = [
  { kind: "source", key: "source.db", targetRef: "db", path: "s.yaml", hash: "h", content: { name: "db" } },
  { kind: "relationships", key: "rel", targetRef: "db", path: "r.yaml", hash: "h", content: { source: "orders" } },
  { kind: "metric", key: "m", targetRef: "orders", path: "m.yaml", hash: "h", content: { role: "owner" } },
  { kind: "metric", key: "m2", targetRef: "orders", path: "m.yaml", hash: "h", content: {} },
  { kind: "watcher", key: "w", targetRef: "stock", path: "w.yaml", hash: "h", content: { name: "Stock" } },
  { kind: "action", key: "a", targetRef: "refund", path: "a.yaml", hash: "h", content: { kind: 7 } },
  { kind: "skill", key: "k", targetRef: "skill", path: "k", hash: "h", content: "x" },
] as PackArtifact[];

const FILES: Record<string, string> = {
  "graphjin.yml": [
    "sources:", "  - name: db", "    kind: database", "    read_only: true", "    port: 5432",
    "tables:", "  - name: orders", "    source: db", "  - name: pack_0123456789abcdef0123", "    database: db", "  - name: other", "    source: x",
    "relationships:", "  - from: db:orders.id", "    to: x:items.order_id", "  - from: y:a", "    to: y:b", "",
  ].join("\n"),
  "queries/top.graphql": "query Top {\r\n  orders { id }\r\n}\r\n",
  "specs/shop.yaml": "openapi: 3.0.0\r\ninfo:\r\n  title: Shop\r\n  version: '1'\r\n",
  "specs/shop.json": "{\"b\": [1, 2.50], \"a\": {\"z\": null}}",
  "skills/review/SKILL.md": "# Review\r\nRead it.\n",
  "skills/review/notes/a.md": "a",
  "skills/review/notes/B.md": "b",
  "skills/review/notes/b.md": "b2",
  "skills/review/empty/.keep.md": "",
};

const STATE_CASES: Array<{ name: string; kind: PackArtifact["kind"]; targetRef: string; metadata: Record<string, unknown> }> = [
  { name: "source", kind: "source", targetRef: "db", metadata: {} },
  { name: "source missing", kind: "source", targetRef: "gone", metadata: {} },
  { name: "relationships", kind: "relationships", targetRef: "db", metadata: {} },
  { name: "relationships without aliases", kind: "relationships", targetRef: "x", metadata: { locator: { source: "db", ignorePackAliases: true } } },
  { name: "relationships by source name only", kind: "relationships", targetRef: "none", metadata: { locator: { source: "db2" } } },
  { name: "saved query", kind: "saved_query", targetRef: "top", metadata: { materializedTarget: "queries/top.graphql" } },
  { name: "spec yaml", kind: "spec", targetRef: "shop", metadata: { materializedTarget: "specs/shop.yaml" } },
  { name: "spec json", kind: "spec", targetRef: "shop", metadata: { materializedTarget: "specs/shop.json" } },
  { name: "spec missing", kind: "spec", targetRef: "shop", metadata: { materializedTarget: "specs/none.yaml" } },
  { name: "spec escapes", kind: "spec", targetRef: "shop", metadata: { materializedTarget: "../outside.yaml" } },
  { name: "spec on the api", kind: "spec", targetRef: "shop", metadata: { materializedTarget: "graphjin:spec:specs/shop.yaml" } },
  { name: "skill", kind: "skill", targetRef: "review", metadata: { materializedTarget: "skills/review" } },
  { name: "skill missing", kind: "skill", targetRef: "review", metadata: { materializedTarget: "skills/none" } },
];

describe("Go pack state fixture", () => {
  it("writes the state hashes that the Go plan must match", async () => {
    const root = await mkdtemp(join(tmpdir(), "go-pack-state-"));
    try {
      for (const [path, text] of Object.entries(FILES)) {
        await mkdir(join(root, path, ".."), { recursive: true });
        await writeFile(join(root, path), text);
      }
      const graphjin = { mode: "files" as const, endpoint: "http://graphjin", orgId: "org", configFile: join(root, "graphjin.yml") };
      const state = [];
      for (const item of STATE_CASES) {
        const metadata = typeof item.metadata.materializedTarget === "string" && !item.metadata.materializedTarget.startsWith("graphjin:")
          ? { ...item.metadata, materializedTarget: join(root, item.metadata.materializedTarget) }
          : item.metadata;
        try {
          const hash = await inspectInstalledPackArtifactCurrent({ orgId: "org", kind: item.kind, targetRef: item.targetRef, metadata, graphjin });
          state.push({ ...item, hash, error: null });
        } catch (error) {
          state.push({ ...item, hash: null, error: (error as Error).message.replaceAll(root, "<root>") });
        }
      }
      const legacyPolicy = nativeArtifactStateHash("policy", { ...POLICY, approver_role: "admin" });
      const legacySource = liveSourceStateHash(LIVE_SOURCES[0]!, "legacy-miss");
      const fixture = `${JSON.stringify({
        files: FILES,
        native: NATIVE.map(item => ({ ...item, hash: nativeArtifactStateHash(item.kind, item.row) })),
        policy: {
          row: POLICY,
          legacyReceipt: legacyPolicy,
          current: policyArtifactStateHash(POLICY),
          withLegacy: policyArtifactStateHash(POLICY, legacyPolicy),
          withOther: policyArtifactStateHash(POLICY, "other"),
        },
        liveSources: LIVE_SOURCES.map(source => ({ source, hash: liveSourceStateHash(source) })),
        liveSourceMiss: legacySource,
        locators: ARTIFACTS.map(artifact => ({ artifact, locator: packArtifactLocator(artifact) })),
        state,
      }, null, 2)}\n`;
      if (process.env.UPDATE_PROMPT_FIXTURES === "1") writeFileSync(FIXTURE, fixture);
      expect(await readFile(FIXTURE, "utf8")).toBe(fixture);
    } finally {
      await rm(root, { recursive: true, force: true });
    }
  });
});
