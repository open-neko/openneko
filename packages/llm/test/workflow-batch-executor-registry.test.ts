import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { expect, it } from "vitest";
import { activeBatchExecutor, pinnedBatchExecutor, parsePinnedQueryToFileContract } from "../src/workflows/batch-executor-registry";

it("pins a version across an active switch and rejects changed registry bytes", () => {
  const dir = mkdtempSync(join(tmpdir(), "neko-executor-registry-"));
  const prior = process.env.HARNESS_BATCH_EXECUTOR_REGISTRY;
  const file = join(dir, "registry.json");
  process.env.HARNESS_BATCH_EXECUTOR_REGISTRY = file;
  const workflowId = "11111111-1111-4111-8111-111111111111";
  const entry = (revision: string, active: boolean) => ({
    workflowId, revision, active, binary: "/tmp/batch", binarySha256: "a".repeat(64),
    openshellBin: "/tmp/openshell", openshellSha256: "b".repeat(64),
    gateway: "fixture", image: "fixture", script: "/tmp/run.py",
    scriptSha256: "c".repeat(64), bundleDir: "/tmp/bundle", bundleSha256: "d".repeat(64),
  });
  try {
    writeFileSync(file, JSON.stringify({ version: 1, executors: [entry("v1", true), entry("v2", false)] }));
    const selected = activeBatchExecutor(workflowId)!;
    const binding = { revision: selected.revision, fingerprint: selected.fingerprint };
    expect(parsePinnedQueryToFileContract({ version: 1, executor: "query-to-file", artifactName: "out.csv",
      columns: ["id"], binding })).not.toBeNull();
    writeFileSync(file, JSON.stringify({ version: 1, executors: [entry("v1", false), entry("v2", true)] }));
    expect(activeBatchExecutor(workflowId)?.revision).toBe("v2");
    expect(pinnedBatchExecutor(workflowId, binding)?.revision).toBe("v1");
    writeFileSync(file, JSON.stringify({ version: 1, executors: [{ ...entry("v1", false), image: "changed" }, entry("v2", true)] }));
    expect(pinnedBatchExecutor(workflowId, binding)).toBeNull();
    writeFileSync(file, JSON.stringify({ version: 1, executors: [entry("v1", true), entry("v2", true)] }));
    expect(() => activeBatchExecutor(workflowId)).toThrow("Invalid batch executor registry");
  } finally {
    if (prior === undefined) delete process.env.HARNESS_BATCH_EXECUTOR_REGISTRY;
    else process.env.HARNESS_BATCH_EXECUTOR_REGISTRY = prior;
    rmSync(dir, { recursive: true, force: true });
  }
});
