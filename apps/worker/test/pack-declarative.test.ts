import { describe, expect, it } from "vitest";
import type { PackArtifact, SolutionPackBundle } from "@neko/packs";
import { bindPackQueries, declarativeGraphjinUpdate, packValue } from "../src/packs/declarative.js";

function bundle(): SolutionPackBundle {
  const artifact = (kind: PackArtifact["kind"], content: unknown, path = ""): PackArtifact => ({ kind, content, path, key: `${kind}.health`, targetRef: "health", hash: "fixture" });
  return { manifest: { metadata: { id: "fixture" }, health: { requiredPreflight: [], postInstall: [], postWriteCanary: [], readiness: {} } }, artifacts: [
    artifact("source", { name: "service_health", kind: "api", base_url: "{{service.base_url}}", openapi: "graphjin/specs/service-health.yaml", auth: { type: "bearer", token: "{{secret.service.api_token}}" } }),
    artifact("spec", { openapi: "3.0.3", paths: { "/health-summary": { get: { operationId: "getHealthSummary" } } } }, "graphjin/specs/service-health.yaml"),
    artifact("relationships", { source: "service_health", relationships: [] }),
    artifact("saved_query", "query Health { health { healthy } }"),
  ] } as SolutionPackBundle;
}
const inputs = { "service.base_url": "https://health.example.test" };
const secrets = { "service.api_token": 'fixture-"token\\with\ncharacters' };

describe("declarative pack configuration", () => {
  it("rejects GraphJin readiness checks for a pack without GraphJin", () => {
    const pack = bundle();
    pack.manifest.artifacts = { skills: [] };
    pack.artifacts = [];
    pack.manifest.health.postInstall = ["graphjin-reload"];
    expect(() => declarativeGraphjinUpdate(pack, {}, {})).toThrow("requires GraphJin artifacts");
  });

  it("maps a bundled connector to the existing read-only GraphJin contract without mutating the pack", () => {
    const pack = bundle();
    const original = JSON.stringify(pack);
    expect(declarativeGraphjinUpdate(pack, inputs, secrets, ["old_health"])).toMatchObject({
      update_sources: [{ name: "service_health", kind: "api", read_only: true, specs_dir: "/config/specs",
        access: { read: "authenticated", write: "blocked", delete: "blocked" },
        specs: { "service-health": { base_url: inputs["service.base_url"], auth: { scheme: "bearer", token: secrets["service.api_token"] } } } }],
      source_patches: [{ name: "old_health", access: { read: "blocked" } }],
      relationships: [],
    });
    expect(JSON.stringify(pack)).toBe(original);
  });

  it("rejects unsupported and unsafe declarations before mutation", () => {
    for (const change of [
      (pack: SolutionPackBundle) => { (pack.artifacts[0]!.content as Record<string, unknown>).read_only = false; },
      (pack: SolutionPackBundle) => { (pack.artifacts[0]!.content as Record<string, unknown>).specs_dir = "/arbitrary"; },
      (pack: SolutionPackBundle) => { (pack.artifacts[0]!.content as Record<string, unknown>).openapi = "https://example.test/spec"; },
      (pack: SolutionPackBundle) => { (pack.artifacts[0]!.content as Record<string, unknown>).auth = { type: "bearer", token: "plaintext" }; },
      (pack: SolutionPackBundle) => { pack.artifacts[3]!.content = "mutation { delete_records { id } }"; },
      (pack: SolutionPackBundle) => { pack.artifacts[1]!.content = { $ref: "file:///private" }; },
      (pack: SolutionPackBundle) => { pack.artifacts[3]!.kind = "action"; },
      (pack: SolutionPackBundle) => { pack.manifest.health.requiredPreflight = ["unknown-check"]; },
      (pack: SolutionPackBundle) => { pack.manifest.health.requiredPreflight = ["queries"]; pack.artifacts.pop(); },
      (pack: SolutionPackBundle) => { pack.artifacts[3]!.content = "query One { health { healthy } } query Two { health { healthy } }"; },
    ]) {
      const pack = bundle();
      change(pack);
      expect(() => declarativeGraphjinUpdate(pack, inputs, secrets)).toThrow();
    }
    expect(() => declarativeGraphjinUpdate(bundle(), inputs, {})).toThrow(/missing pack template/);
  });

  it("binds database query roots and named directives without rewriting literals or nested fields", () => {
    const pack = bundle();
    pack.artifacts[0]!.content = { name: "service_health", kind: "database" };
    pack.artifacts[3]!.content = '# mutation in a comment is harmless\nquery Health { ...Root } fragment Root on Query { alias: health(where: { label: { eq: "mutation" } }) { healthy details { id } } }';
    const bound = bindPackQueries(pack, { "source.health": "customer_db" });
    expect(bound.tables).toEqual([{ name: expect.stringMatching(/^pack_[a-f0-9]{20}$/), table: "health", source: "customer_db" }]);
    expect(String(bound.bundle.artifacts[3]!.content)).toContain('@database(name: "customer_db")');
    expect(String(bound.bundle.artifacts[3]!.content).match(/@database/g)).toHaveLength(1);
    expect(String(bound.bundle.artifacts[3]!.content)).toContain('eq: "mutation"');
    expect(String(pack.artifacts[3]!.content)).not.toContain("@database");
    pack.artifacts[3]!.content = '{ health @database(name: service_health) { healthy } }';
    expect(String(bindPackQueries(pack, { "source.health": "customer_db" }).bundle.artifacts[3]!.content)).toContain('@database(name: "customer_db")');
    for (const query of ['{ health @database(name: "wrong") { healthy } }', 'query ($db: String!) { health @database(name: $db) { healthy } }', 'query { ...Loop } fragment Loop on Query { ...Loop }']) {
      pack.artifacts[3]!.content = query;
      expect(() => bindPackQueries(pack, { "source.health": "customer_db" })).toThrow();
    }
    pack.artifacts.push({ ...pack.artifacts[0]!, key: "source.other", content: { name: "other", kind: "database" } });
    pack.artifacts[3]!.content = '{ health { healthy } }';
    expect(() => bindPackQueries(pack, { "source.health": "customer_db" })).toThrow(/select a declared database/);
    pack.artifacts[3]!.content = '{ a: health @database(name: service_health) { healthy } b: health @database(name: other) { healthy } }';
    const text = String(bindPackQueries(pack, { "source.health": "customer_db" }).bundle.artifacts[3]!.content);
    expect(text).toContain('@database(name: "customer_db")');
    expect(text).toContain('@database(name: "other")');
  });

  it("preserves template value types and fails on missing or interpolated values", () => {
    expect(packValue({ count: "{{count}}", enabled: "{{enabled}}" }, { count: 3, enabled: false })).toEqual({ count: 3, enabled: false });
    expect(() => packValue("prefix-{{missing}}", {})).toThrow();
    expect(() => packValue("{{missing}}", {})).toThrow();
  });
});
