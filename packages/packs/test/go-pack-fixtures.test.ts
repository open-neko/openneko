import { mkdtemp, readdir, readFile, rm, stat } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { writeFileSync } from "node:fs";
import { parse as parseYaml } from "yaml";
import { describe, expect, it } from "vitest";
import { canonicalJson, hashPackFiles, loadSolutionPack, parseManifest, planPack, stagePackArchive, type InstalledPackArtifact } from "../src/index.js";
import { packZipEntries, zipFixture } from "./zip-fixture.js";

// The Go pack format (apps/openneko/internal/packs) must give the same hashes.
// Run with UPDATE_PROMPT_FIXTURES=1 to rewrite the fixture.
const FIXTURE = fileURLToPath(new URL("../../../apps/openneko/internal/packs/testdata/nodepacks.json", import.meta.url));
const PACKS = fileURLToPath(new URL("../../../packs", import.meta.url));
const SERVICE_HEALTH = fileURLToPath(new URL("../../../apps/worker/test/fixtures/service-health", import.meta.url));

const CANONICAL: unknown[] = [
  { b: 1, a: [1.5, -0, 1e21, 1e-7, 123456789012, null, true], "é": "\u0001\"\\\n", A: { z: {}, Z: [] } },
  "line sep 😀",
  [0.1, 2.5e-8, 100, -3],
];

const SORT = ["skill:skill.b", "metric:metric.a", "saved_query:saved-query.z", "saved_query:saved_query.a", "source:source.Orders",
  "source:source.orders", "source:source.order-items", "source:source.order_items", "source:source.order.items", "action:action.10",
  "action:action.9", "action:action.a-b", "action:action.a_b", "action:action.ab", "Skill:x", "policy:policy.send-email", "spec:spec.slack"];

const YAML = [
  "a: 1\nb: 1.50\nc: 0o17\nd: 0x1F\ne: .5\nf: yes\ng: ~\nh: 2024-01-02\ni: '007'\nj: 1e3\nk: -0\nl: True\nm: +5",
  "list:\n  - [1, two, \"three\"]\n  - {x: null, y: false}\nblock: |\n  keep\n  lines\nfolded: >\n  one\n  two\n",
  "anchor: &a {k: v}\nref: *a\n\"quoted key\": 1\n",
];

const MANIFEST = {
  apiVersion: "openneko.app/v1", kind: "SolutionPack",
  metadata: { id: "skills-only", name: "Skills only", version: "1.0.0", publisher: "fixture", category: "operations" },
  compatibility: { openneko: ">=2.40.0", applications: [], databases: [] },
  inputs: [], secrets: [], artifacts: { skills: ["skills/review"] },
  health: { requiredPreflight: [], readiness: {}, postInstall: [], postWriteCanary: [] },
};
const OAUTH = {
  key: "google", providerLabel: "Google", authorizationUrl: "https://accounts.example.com/auth", tokenUrl: "https://accounts.example.com/token",
  userInfoUrl: "https://accounts.example.com/me", clientIdInput: "google.client_id", clientSecret: "google.client_secret",
  accessToken: "google.access", refreshToken: "google.refresh", scopes: ["email"],
};
const OAUTH_MANIFEST = {
  ...MANIFEST,
  inputs: [{ key: "google.client_id", type: "string" }],
  secrets: [{ key: "google.client_secret", purpose: "pack_oauth_client" }, { key: "google.access", purpose: "pack_oauth_token", required: false },
    { key: "google.refresh", purpose: "pack_oauth_token" }],
  oauth: [OAUTH], permissions: { network: ["accounts.example.com"] },
};
const MANIFESTS: unknown[] = [
  MANIFEST,
  { ...MANIFEST, channels: [] },
  { ...MANIFEST, channels: ["slack", "telegram"] },
  { ...MANIFEST, channels: ["discord"] },
  { ...MANIFEST, channels: ["slack", "slack"] },
  { ...MANIFEST, extra: true },
  { ...MANIFEST, metadata: { ...MANIFEST.metadata, id: "Bad_Id" } },
  { ...MANIFEST, metadata: { ...MANIFEST.metadata, version: "1.0" } },
  { ...MANIFEST, management: { label: "Open", path: "//evil" } },
  { ...MANIFEST, management: { label: "Open", path: "/admin/settings" } },
  { ...MANIFEST, artifacts: { skills: ["../escape"] } },
  { ...MANIFEST, artifacts: { skills: ["a\\b"] } },
  { ...MANIFEST, artifacts: { ...MANIFEST.artifacts, graphjin: { sources: "s.yaml", relationships: "r.yaml", specs: [] } } },
  { ...MANIFEST, inputs: [{ key: "x.y", type: "enum" }] },
  { ...MANIFEST, inputs: [{ key: "x.y", type: "string", values: ["a"] }] },
  { ...MANIFEST, inputs: [{ key: "x.y", type: "enum", values: ["a", 2, true], default: "a" }, { key: "x.z", type: "integer", default: 0 }] },
  { ...MANIFEST, inputs: [{ key: "x.y", type: "string" }, { key: "x.y", type: "url" }] },
  { ...MANIFEST, permissions: {} },
  OAUTH_MANIFEST,
  { ...OAUTH_MANIFEST, permissions: { network: [] } },
  { ...OAUTH_MANIFEST, oauth: [{ ...OAUTH, tokenUrl: "http://accounts.example.com/token" }] },
  { ...OAUTH_MANIFEST, oauth: [{ ...OAUTH, scope: "user", experience: { description: "d", helpUrl: "https://help.example.com" } }] },
  { ...OAUTH_MANIFEST, oauth: [{ ...OAUTH, clientIdInput: "missing" }] },
];

async function servicePackEntries() {
  return packZipEntries(SERVICE_HEALTH);
}

describe("Go pack format fixtures", () => {
  it("matches the stored fixture", async () => {
    const packs = [];
    for (const entry of (await readdir(PACKS, { withFileTypes: true })).sort((a, b) => a.name < b.name ? -1 : 1)) {
      if (!entry.isDirectory()) continue;
      const root = join(PACKS, entry.name);
      if (!await stat(join(root, "pack.yaml")).then(() => true, () => false)) continue;
      const bundle = await loadSolutionPack(root);
      packs.push({
        dir: entry.name,
        manifest: canonicalJson(bundle.manifest),
        manifestHash: bundle.manifestHash,
        bundleHash: bundle.bundleHash,
        filesHash: await hashPackFiles(root),
        artifacts: bundle.artifacts.map(({ kind, key, targetRef, path, hash }) => ({ kind, key, targetRef, path, hash })),
      });
    }
    const staging = await mkdtemp(join(tmpdir(), "go-pack-fixture-"));
    let upload;
    let plans;
    try {
      const staged = await stagePackArchive(zipFixture(await servicePackEntries()), staging);
      const bundle = staged.bundle;
      upload = { id: "service-health", bundleHash: bundle.bundleHash, contentHash: await hashPackFiles(bundle.root) };
      const [first, second, third, fourth] = bundle.artifacts;
      const installed = (artifact: typeof first, extra: Partial<InstalledPackArtifact>): InstalledPackArtifact => ({
        kind: artifact!.kind, key: artifact!.key, targetRef: artifact!.targetRef, desiredHash: artifact!.hash,
        lastAppliedHash: "applied", currentHash: "applied", ownership: "managed", ...extra,
      });
      const cases: Record<string, InstalledPackArtifact[]> = {
        fresh: [],
        mixed: [
          installed(first, {}),
          installed(second, { desiredHash: "old" }),
          installed(third, { currentHash: "edited" }),
          installed(fourth, { currentHash: null }),
          { kind: "metric", key: "metric.gone", targetRef: "gone", desiredHash: "x", lastAppliedHash: "y", currentHash: "y", ownership: "managed" },
          { kind: "metric", key: "metric.gone-edited", targetRef: "gone2", desiredHash: "x", lastAppliedHash: "y", currentHash: "z", ownership: "modified" },
          { kind: "metric", key: "metric.detached", targetRef: "d", desiredHash: "x", lastAppliedHash: "y", currentHash: null, ownership: "detached" },
        ],
        owners: [
          installed(first, { ownership: "detached" }),
          installed(second, { ownership: "retired", currentHash: null }),
          installed(third, { ownership: "retired", currentHash: "changed" }),
        ],
      };
      plans = Object.entries(cases).map(([name, value]) => ({ name, installed: value, plan: planPack(bundle, value) }));
      await staged.cleanup();
    } finally {
      await rm(staging, { recursive: true, force: true });
    }
    const manifests = MANIFESTS.map((input) => {
      try {
        return { input, ok: true, normalized: canonicalJson(parseManifest(input)) };
      } catch {
        return { input, ok: false };
      }
    });
    const fixture = `${JSON.stringify({
      canonical: CANONICAL.map((value) => ({ value, json: canonicalJson(value) })),
      sort: [...SORT].sort((a, b) => a.localeCompare(b)),
      yaml: YAML.map((source) => ({ source, json: canonicalJson(parseYaml(source)) })),
      manifests,
      packs,
      upload,
      plans,
    }, null, 2)}\n`;
    if (process.env.UPDATE_PROMPT_FIXTURES === "1") writeFileSync(FIXTURE, fixture);
    expect(fixture).toBe(await readFile(FIXTURE, "utf8"));
  });
});
