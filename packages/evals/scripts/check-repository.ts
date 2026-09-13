import { readFile, readdir } from "node:fs/promises";
import { dirname, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { createEvalPlan, loadEval, verifyResult } from "../src";
import { parse } from "yaml";
import { assertReadmeMetrics } from "../src/report";
import { SummarySchema } from "../src/schemas";

const workspace = fileURLToPath(new URL("../../../", import.meta.url));
const configRoot = resolve(workspace, "evals/configs");
const resultsRoot = resolve(workspace, "evals/results");
const semanticInventoryPath = resolve(workspace, "evals/semantic-inventory.yaml");

async function filesBelow(root: string, suffix: string): Promise<string[]> {
  const files: string[] = [];
  let entries;
  try {
    entries = await readdir(root, { withFileTypes: true });
  } catch (cause) {
    if ((cause as NodeJS.ErrnoException).code === "ENOENT") return [];
    throw cause;
  }
  for (const entry of entries) {
    const path = resolve(root, entry.name);
    if (entry.isDirectory()) files.push(...(await filesBelow(path, suffix)));
    else if (entry.name.endsWith(suffix)) files.push(path);
  }
  return files.sort();
}

const configPaths = await filesBelow(configRoot, ".yaml");
if (!configPaths.length) throw new Error("evals/configs contains no configurations");

const configIds = new Set<string>();
const coveredSemantics = new Set<string>();
let registry:
  | NonNullable<Awaited<ReturnType<typeof loadEval>>["semantics"]>
  | undefined;
let cases = 0;
let calls = 0;
for (const configPath of configPaths) {
  const loaded = await loadEval(configPath);
  const plan = createEvalPlan(loaded);
  if (configIds.has(loaded.config.id)) {
    throw new Error(`duplicate eval config id ${loaded.config.id}`);
  }
  configIds.add(loaded.config.id);
  cases += loaded.cases.length;
  calls += plan.calls;
  registry ??= loaded.semantics;
  for (const evalCase of loaded.cases) {
    for (const semantic of evalCase.semantics) coveredSemantics.add(semantic);
  }
  for (const variant of loaded.config.variants) {
    if (variant.data_path === "graphjin-direct") coveredSemantics.add("DATA-DIRECT");
    if (variant.data_path === "graphjin-agent") coveredSemantics.add("DATA-DELEGATED");
    if (variant.data_path === "planner-host-execute") {
      coveredSemantics.add("DATA-PLANNER-EXECUTE");
    }
  }
}
if (!registry) throw new Error("no eval config references the semantic registry");
const missingEvalSemantics = registry.entries
  .filter(
    (entry) =>
      entry.disposition === "eval" && !coveredSemantics.has(entry.id),
  )
  .map((entry) => entry.id);
if (missingEvalSemantics.length) {
  throw new Error(
    `semantic entries marked eval have no configured coverage: ${missingEvalSemantics.join(", ")}`,
  );
}

type InventoryMap = Record<string, string[]>;
type SemanticInventory = {
  schema_version: string;
  agent_events: InventoryMap;
  worker_queues: InventoryMap;
  observation_kinds: InventoryMap;
};

const inventory = parse(
  await readFile(semanticInventoryPath, "utf8"),
) as SemanticInventory;
if (inventory.schema_version !== "openneko.eval.semantic-inventory/v1") {
  throw new Error("semantic inventory has an unsupported schema_version");
}
const semanticIds = new Set(registry.entries.map((entry) => entry.id));
for (const [kind, mapping] of Object.entries({
  agent_events: inventory.agent_events,
  worker_queues: inventory.worker_queues,
  observation_kinds: inventory.observation_kinds,
})) {
  if (!mapping || typeof mapping !== "object" || Array.isArray(mapping)) {
    throw new Error(`semantic inventory ${kind} must be a mapping`);
  }
  for (const [item, ids] of Object.entries(mapping)) {
    if (!Array.isArray(ids) || ids.length === 0) {
      throw new Error(`semantic inventory ${kind}.${item} has no semantic owners`);
    }
    const unknown = ids.filter((id) => !semanticIds.has(id));
    if (unknown.length) {
      throw new Error(
        `semantic inventory ${kind}.${item} references unknown IDs: ${unknown.join(", ")}`,
      );
    }
  }
}

function quotedValues(source: string): string[] {
  return [...source.matchAll(/"([A-Za-z0-9_.-]+)"/gu)].map((match) => match[1]!);
}

function between(source: string, start: string, end: string): string {
  const from = source.indexOf(start);
  const to = source.indexOf(end, from + start.length);
  if (from < 0 || to < 0) throw new Error(`cannot inspect code inventory ${start}`);
  return source.slice(from, to);
}

function assertExactInventory(
  label: string,
  actualValues: readonly string[],
  mapped: InventoryMap,
): void {
  const actual = [...new Set(actualValues)].sort();
  const expected = Object.keys(mapped).sort();
  const missing = actual.filter((item) => !expected.includes(item));
  const stale = expected.filter((item) => !actual.includes(item));
  if (missing.length || stale.length) {
    throw new Error(
      `${label} semantic inventory mismatch` +
        `${missing.length ? `; unmapped: ${missing.join(", ")}` : ""}` +
        `${stale.length ? `; stale: ${stale.join(", ")}` : ""}`,
    );
  }
}

const agentBackendSource = await readFile(
  resolve(workspace, "packages/llm/src/agent-backend.ts"),
  "utf8",
);
const agentEventSource = between(
  agentBackendSource,
  "export type AgentEvent =",
  "export type AgentChatMessage",
);
assertExactInventory(
  "AgentEvent",
  [...agentEventSource.matchAll(/type:\s*"([a-z_]+)"/gu)].map(
    (match) => match[1]!,
  ),
  inventory.agent_events,
);

const jobsSource = await readFile(
  resolve(workspace, "packages/db/src/jobs.ts"),
  "utf8",
);
assertExactInventory(
  "worker queue",
  quotedValues(between(jobsSource, "export const QUEUE = {", "} as const")),
  inventory.worker_queues,
);

const observationSource = await readFile(
  resolve(workspace, "packages/telemetry/src/types.ts"),
  "utf8",
);
assertExactInventory(
  "observation kind",
  quotedValues(
    between(
      observationSource,
      "export const OBSERVATION_KINDS = [",
      "] as const",
    ),
  ),
  inventory.observation_kinds,
);

const readme = await readFile(resolve(workspace, "README.md"), "utf8");
// These two previously published runs lack measurements required for new submissions.
const historicalRuns = new Map([
  ["run-20260811t074134722z-8ae32696", "sha256:56da2f37c54fd46ee4ea3af52df42c1298668a7eb4a17c97fc4cec39a8aabe73"],
  ["run-20260905t052736658z-f6ed98f5", "sha256:0f61cfdcb89b24a36660f911aeb76979a112012d2a14474bdb374f6d85d12c69"],
]);
const resultManifests = await filesBelow(resultsRoot, "manifest.json");
let acceptedResults = 0;
let rejectedResults = 0;
for (const manifestPath of resultManifests) {
  const resultDir = dirname(manifestPath);
  const verification = await verifyResult(resultDir);
  const markdown = await readFile(resolve(resultDir, "summary.md"), "utf8");
  const manifest = JSON.parse(await readFile(manifestPath, "utf8"));
  const historical = historicalRuns.get(verification.runId) === manifest.files["results.jsonl"];
  if (!markdown.includes("openneko.eval.report.facts.md/v2") && !historical) {
    throw new Error(`${resultDir} must include the required v2 report metrics`);
  }
  const reportLink = relative(workspace, resolve(resultDir, "summary.md"));
  if (!historical || readme.includes(reportLink)) assertReadmeMetrics(readme,
    SummarySchema.parse(JSON.parse(await readFile(resolve(resultDir, "summary.json"), "utf8"))),
    reportLink,
  );

  if (!verification.gatesPassed) {
    if (manifest.accepted !== false) {
      throw new Error(
        `${dirname(manifestPath)} failed its suite gates but is not explicitly marked accepted: false`,
      );
    }
    rejectedResults += 1;
  } else {
    acceptedResults += 1;
  }
}

process.stdout.write(
  `eval repository valid: ${configPaths.length} configs, ${cases} selected case references, ${calls} planned calls, ${coveredSemantics.size}/${registry.entries.length} semantic IDs covered, ${Object.keys(inventory.agent_events).length} agent events, ${Object.keys(inventory.worker_queues).length} worker queues, ${Object.keys(inventory.observation_kinds).length} observation kinds mapped, ${resultManifests.length} checked-in results verified (${acceptedResults} accepted, ${rejectedResults} rejected)\n`,
);
