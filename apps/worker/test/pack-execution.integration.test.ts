import { mkdtemp, mkdir, writeFile, rm, cp } from "node:fs/promises";
import { tmpdir, homedir } from "node:os";
import { join } from "node:path";
import { afterAll, beforeAll, expect, it, describe, vi } from "vitest";
import { stringify } from "yaml";
import { pool } from "@neko/db";
import { PackService } from "../src/packs/service";
import { zipFixture } from "../../../packages/packs/test/zip-fixture";

vi.mock("../src/plugins/plugin-registry", () => { throw new Error("Pack execution must not load plugins"); });
const image = process.env.OPENNEKO_PACK_EXEC_TEST_IMAGE;
describe.skipIf(!image)("pack execution through OpenShell and PostgreSQL", () => {
  const org = `pack-exec-${Date.now()}`;
  let root: string;
  let service: PackService;
  const manifest = {
    apiVersion: "openneko.app/v1", kind: "SolutionPack",
    metadata: { id: "exec-fixture", name: "Execution fixture", version: "1.0.0", publisher: "fixture", category: "operations" },
    compatibility: { openneko: ">=2.40.0", applications: [], databases: [] },
    inputs: [], secrets: [], artifacts: { skills: [] },
    health: { requiredPreflight: [], postInstall: [], postWriteCanary: [], readiness: {} },
    connectors: [{ id: "fixture", image, entrypoint: "/app/connector", network: [], operations: [
      { id: "echo", description: "Echo input", effect: "read" },
      { id: "network", description: "Check denied network access", effect: "read" },
      { id: "wait", description: "Check timeout", effect: "read" },
      { id: "write", description: "Blocked until action dispatch exists", effect: "write" },
    ] }],
  };
  beforeAll(async () => {
    root = await mkdtemp(join(tmpdir(), "pack-execution-"));
    await cp(join(process.env.XDG_CONFIG_HOME || join(homedir(), ".config"), "openshell"), join(root, "config/openshell"), { recursive: true });
    vi.stubEnv("XDG_CONFIG_HOME", join(root, "config"));
    await mkdir(join(root, "packs/exec-fixture"), { recursive: true });
    await writeFile(join(root, "packs/exec-fixture/pack.yaml"), stringify(manifest));
    await pool().query("insert into organization (id,name) values ($1,'Pack execution fixture')", [org]);
    service = new PackService(org, join(root, "packs"));
  });
  afterAll(async () => {
    await pool().query("delete from organization where id=$1", [org]);
    await pool().end();
    vi.unstubAllEnvs();
    await rm(root, { recursive: true, force: true });
  });
  it("requires review and executes only installed, unchanged first-party pack reads", async () => {
    await expect(service.runConnector("exec-fixture", "fixture", "echo", {})).rejects.toThrow("not installed");
    await expect(service.install("exec-fixture")).rejects.toThrow("review is missing or stale");
    const review = await service.review("exec-fixture");
    await service.install("exec-fixture", { reviewHash: review.reviewHash });
    const result = await service.runConnector("exec-fixture", "fixture", "echo", { value: "fixture" }) as { value: string; uid: number };
    expect(result.value).toBe("fixture");
    expect(result.uid).toBeGreaterThan(0);
    await expect(service.runConnector("exec-fixture", "fixture", "write", {})).rejects.toThrow("action dispatch");
    await writeFile(join(root, "packs/exec-fixture/pack.yaml"), stringify({ ...manifest, metadata: { ...manifest.metadata, name: "Changed" } }));
    await expect(service.runConnector("exec-fixture", "fixture", "echo", {})).rejects.toThrow("contents changed");
    await writeFile(join(root, "packs/exec-fixture/pack.yaml"), stringify(manifest));
  }, 180_000);
  it("enforces denied network access and the remote execution timeout", async () => {
    const result = await service.runConnector("exec-fixture", "fixture", "network", {}) as { blocked?: boolean; status?: number };
    expect(result.blocked || result.status === 403).toBe(true);
    const execution = expect(service.runConnector("exec-fixture", "fixture", "wait", {})).rejects.toThrow("exec failed or exceeded");
    const probe = await pool().connect();
    try {
      await expect.poll(async () => {
        const { rows } = await probe.query("SELECT pg_try_advisory_lock(hashtextextended($1, 0)) AS acquired", [`pack:${org}:exec-fixture`]);
        if (rows[0].acquired) await probe.query("SELECT pg_advisory_unlock(hashtextextended($1, 0))", [`pack:${org}:exec-fixture`]);
        return rows[0].acquired;
      }).toBe(false);
    } finally { probe.release(); }
    let removed = false;
    const removal = service.uninstall("exec-fixture").then(() => { removed = true; });
    await new Promise(resolve => setTimeout(resolve, 200));
    expect(removed).toBe(false);
    await execution;
    await removal;
    await expect(service.runConnector("exec-fixture", "fixture", "echo", {})).rejects.toThrow("not installed");
  }, 180_000);
  it("uses the same execution path for an uploaded pack", async () => {
    const uploaded = { ...manifest, metadata: { ...manifest.metadata, id: "uploaded-exec" } };
    await service.upload(zipFixture([{ name: "uploaded-exec/pack.yaml", data: stringify(uploaded) }]));
    const review = await service.review("uploaded-exec");
    await service.install("uploaded-exec", { reviewHash: review.reviewHash });
    const restored = new PackService(org, join(root, "packs"));
    expect(await restored.runConnector("uploaded-exec", "fixture", "echo", { value: "uploaded" })).toMatchObject({ value: "uploaded" });
    await restored.uninstall("uploaded-exec");
  }, 180_000);
  it("rejects an unavailable image during review", async () => {
    const invalid = { ...manifest, connectors: [{ ...manifest.connectors[0], image: image!.replace(/sha256:.*/, `sha256:${"0".repeat(64)}`) }] };
    await writeFile(join(root, "packs/exec-fixture/pack.yaml"), stringify(invalid));
    await expect(service.review("exec-fixture")).rejects.toThrow("create failed");
  }, 180_000);

});
