import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { isAbsolute } from "node:path";
import { parseQueryToFileContract, type QueryToFileContract } from "./api-contract";

type Executor = {
  workflowId: string;
  revision: string;
  active: boolean;
  binary: string;
  binarySha256: string;
  openshellBin: string;
  openshellSha256: string;
  gateway: string;
  image: string;
  script: string;
  scriptSha256: string;
  bundleDir: string;
  bundleSha256: string;
};

export type BatchExecutorBinding = { revision: string; fingerprint: string };
export type PinnedQueryToFileContract = QueryToFileContract & { binding: BatchExecutorBinding };
export type BatchExecutor = Executor & { fingerprint: string };

const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const digest = /^[0-9a-f]{64}$/;
const revision = /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/;

function parseExecutor(value: unknown): BatchExecutor {
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error("Invalid batch executor registry");
  const entry = value as Record<string, unknown>;
  if (typeof entry.workflowId !== "string" || !uuid.test(entry.workflowId) ||
      typeof entry.revision !== "string" || !revision.test(entry.revision) ||
      typeof entry.active !== "boolean" ||
      ["binary", "openshellBin", "script", "bundleDir"].some((key) =>
        typeof entry[key] !== "string" || entry[key].length > 2048 || !isAbsolute(entry[key])) ||
      ["binarySha256", "openshellSha256", "scriptSha256", "bundleSha256"].some((key) =>
        typeof entry[key] !== "string" || !digest.test(entry[key])) ||
      ["gateway", "image"].some((key) =>
        typeof entry[key] !== "string" || entry[key].length < 1 || entry[key].length > 256)) {
    throw new Error("Invalid batch executor registry");
  }
  const parsed = {
    workflowId: (entry.workflowId as string).toLowerCase(), revision: entry.revision as string,
    active: entry.active as boolean, binary: entry.binary as string,
    binarySha256: entry.binarySha256 as string,
    openshellBin: entry.openshellBin as string,
    openshellSha256: entry.openshellSha256 as string,
    gateway: entry.gateway as string, image: entry.image as string,
    script: entry.script as string, scriptSha256: entry.scriptSha256 as string,
    bundleDir: entry.bundleDir as string, bundleSha256: entry.bundleSha256 as string,
  };
  const { active: _active, ...identity } = parsed;
  return { ...parsed, fingerprint: createHash("sha256").update(JSON.stringify(identity)).digest("hex") };
}

function registry(): BatchExecutor[] {
  const file = process.env.HARNESS_BATCH_EXECUTOR_REGISTRY;
  if (!file || !isAbsolute(file)) throw new Error("Batch executor registry is unavailable");
  const bytes = readFileSync(file);
  if (bytes.length > 256 * 1024) throw new Error("Invalid batch executor registry");
  const value = JSON.parse(bytes.toString("utf8")) as { version?: unknown; executors?: unknown };
  if (value?.version !== 1 || !Array.isArray(value.executors) || value.executors.length > 512) {
    throw new Error("Invalid batch executor registry");
  }
  const entries = value.executors.map(parseExecutor);
  const seen = new Set<string>();
  const active = new Set<string>();
  for (const entry of entries) {
    const key = `${entry.workflowId}:${entry.revision}`;
    if (seen.has(key) || (entry.active && active.has(entry.workflowId))) {
      throw new Error("Invalid batch executor registry");
    }
    seen.add(key);
    if (entry.active) active.add(entry.workflowId);
  }
  return entries;
}

export function activeBatchExecutor(workflowId: string): BatchExecutor | null {
  return registry().find((entry) => entry.workflowId === workflowId && entry.active) ?? null;
}

export function pinnedBatchExecutor(workflowId: string, binding: BatchExecutorBinding): BatchExecutor | null {
  return registry().find((entry) => entry.workflowId === workflowId &&
    entry.revision === binding.revision && entry.fingerprint === binding.fingerprint) ?? null;
}

export function parsePinnedQueryToFileContract(value: unknown): PinnedQueryToFileContract | null {
  const contract = parseQueryToFileContract(value);
  const binding = value && typeof value === "object" && !Array.isArray(value)
    ? (value as Record<string, unknown>).binding : null;
  if (!contract || !binding || typeof binding !== "object" || Array.isArray(binding)) return null;
  const fields = binding as Record<string, unknown>;
  if (typeof fields.revision !== "string" || !revision.test(fields.revision) ||
      typeof fields.fingerprint !== "string" || !digest.test(fields.fingerprint)) return null;
  return { ...contract, binding: { revision: fields.revision, fingerprint: fields.fingerprint } };
}
