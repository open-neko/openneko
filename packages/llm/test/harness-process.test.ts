import { createHash } from "node:crypto";
import { mkdtemp, mkdir, readFile, symlink, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, expect, it } from "vitest";
import { parseHarnessProcessInput, runHarnessProcess, type HarnessProcessBinding } from "../src/work/harness-process";
import { rm } from "node:fs/promises";

const roots: string[] = [];
afterEach(async () => {
  for (const root of roots.splice(0)) await rm(root, {recursive: true, force: true});
});

async function fixture(): Promise<HarnessProcessBinding> {
  const orgRoot = await mkdtemp(join(tmpdir(), "harness-process-broker-"));
  roots.push(orgRoot);
  const runRoot = join(orgRoot, "runs", "run-1");
  const artifactRoot = join(runRoot, "artifacts");
  const uploadsRoot = join(orgRoot, "uploads", "thread-1");
  await mkdir(artifactRoot, {recursive: true});
  await mkdir(uploadsRoot, {recursive: true});
  const binary = join(orgRoot, "fixture-process");
  const source = `#!/bin/sh
set -eu
test -z "\${OPENNEKO_BROKER_TOKEN:-}"
test -f "$HARNESS_PROCESS_INPUT_ROOT/run.py"
test -f "$HARNESS_PROCESS_INPUT_ROOT/source.csv"
mkdir "$HARNESS_PROCESS_OUTPUT_ROOT"
printf 'lead_id\\nLEAD-42\\n' > "$HARNESS_PROCESS_OUTPUT_ROOT/result.csv"
printf '%s\\n' '{"ok":true,"result":{"Files":["result.csv"],"Output":"done","InputDigest":"${"a".repeat(64)}"}}'
`;
  await writeFile(binary, source, {mode: 0o700});
  await writeFile(join(uploadsRoot, "source.csv"), "lead_id\nLEAD-42\n");
  return {binary, binarySha256: createHash("sha256").update(source).digest("hex"),
    openshell: "/usr/bin/false", gateway: "fixture", image: "fixture:local",
    orgRoot, runRoot, artifactRoot, uploadsRoot};
}

it("stages only selected uploads and returns a bound artifact receipt", async () => {
  const binding = await fixture();
  const receipt = await runHarnessProcess(binding, "run-1", 2,
    JSON.stringify({language: "python", script: "print('ok')", uploads: ["source.csv"], outputs: ["result.csv"]}));
  expect(receipt.ok).toBe(true);
  expect(receipt.output).toBe("done");
  expect(receipt.files).toEqual([{path: "runs/run-1/artifacts/process-2/result.csv",
    sha256: createHash("sha256").update("lead_id\nLEAD-42\n").digest("hex"), bytes: 16}]);
  expect(await readFile(join(binding.artifactRoot, "process-2", "result.csv"), "utf8")).toBe("lead_id\nLEAD-42\n");
});

it("rejects traversal and symlinked uploads before dispatch", async () => {
  const binding = await fixture();
  expect(() => parseHarnessProcessInput(JSON.stringify({language: "python", script: "print(1)",
    uploads: ["../outside"], outputs: ["result.csv"]}))).toThrow();
  await symlink("source.csv", join(binding.uploadsRoot, "link.csv"));
  await expect(runHarnessProcess(binding, "run-1", 1,
    JSON.stringify({language: "python", script: "print(1)", uploads: ["link.csv"], outputs: ["result.csv"]}))).rejects.toThrow();
  await expect(readFile(join(binding.artifactRoot, "process-1", "result.csv"))).rejects.toThrow();
});

it("refuses a changed host executable before dispatch", async () => {
  const binding = await fixture();
  await writeFile(binding.binary, "#!/bin/sh\nexit 0\n", {mode: 0o700});
  await expect(runHarnessProcess(binding, "run-1", 1,
    JSON.stringify({language: "python", script: "print(1)", outputs: ["result.csv"]}))).rejects.toThrow("executable changed");
});
