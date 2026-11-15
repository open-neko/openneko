import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { dbReachable } from "@neko/db/test-helpers";
import { createEvalPlan, loadEval } from "@neko/evals";
import { createOpenNekoBackendDriver } from "../scripts/eval-openneko-backend";

const CONFIG = fileURLToPath(new URL("../../../evals/configs/openneko-backend-scripted-good-v4.yaml", import.meta.url));
// The Node sink lost its last caller in 6fe6c6e32, so the Node side is a record
// from the in-process driver at 70fea235c (see "recordedWith" in the file).
const NODE_RECORD = fileURLToPath(new URL("./fixtures/eval-trace-parity/b11-composition.node.json", import.meta.url));
const CASE_ID = "b11-composition";

type TraceEvent = Record<string, unknown> & { operation: string; sequence: number; source: string };
type TraceStep = Record<string, unknown> & { operation: string; sequence: number };
type Evidence = { events: TraceEvent[]; steps: TraceStep[]; modelTurns?: unknown[] };

const HOST_OPERATIONS = new Set(["memory.prefetched", "skill.loaded"]);
const UUID = /[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/g;
const TIME = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?Z$/;

function nodeRecord(): Evidence {
  return (JSON.parse(readFileSync(NODE_RECORD, "utf8")) as { semanticEvidence: Evidence }).semanticEvidence;
}

function identityOf(evidence: Evidence): string {
  const org = String(evidence.events[0]?.orgId ?? "");
  return /^eval-backend-([0-9a-f]+)$/.exec(org)?.[1] ?? "";
}

function stable(value: unknown, identity: string): unknown {
  if (typeof value === "string") {
    if (TIME.test(value)) return "<time>";
    const text = value.replace(UUID, "<uuid>");
    return identity ? text.split(identity).join("<identity>") : text;
  }
  if (Array.isArray(value)) return value.map((item) => stable(item, identity));
  if (value && typeof value === "object") {
    return Object.fromEntries(
      Object.entries(value)
        .filter(([key]) => key !== "definitionDigest")
        .map(([key, item]) => [key, stable(item, identity)]),
    );
  }
  return value;
}

type Comparable = { operation: string; source?: string; event: unknown; step?: unknown };

// The response and workflow definition digests cover row ids and times; the
// step rows stand in for them.
function comparable(evidence: Evidence): Comparable[] {
  const identity = identityOf(evidence);
  return evidence.events.map((event) => {
    const { timestamp: _t, durationMs: _d, runId: _r, responseDigest: _rd, sequence, source, ...rest } = event;
    const step = evidence.steps.find((candidate) => candidate.sequence === sequence);
    const { sequence: _s, ...stepRest } = step ?? {};
    return {
      operation: event.operation,
      ...(HOST_OPERATIONS.has(event.operation) ? { source } : {}),
      event: stable(rest, identity),
      ...(step ? { step: stable(stepRest, identity) } : {}),
    };
  });
}

type Memory = { contentDigest: string; layer: string; scope?: string; kind?: string };

function memoryKey(memory: Memory): string {
  return JSON.stringify([memory.contentDigest, memory.layer, memory.scope, memory.kind]);
}

// Allowed: tool calls are "trusted-broker" in Go and were "trusted-host" in the
// old driver; Go lists the GraphJin tools at the turn start; Go's
// memory.prefetched holds every prompt memory, the old driver only the target.
function traceParityDifferences(node: Evidence, go: Evidence): string[] {
  const differences: string[] = [];
  const left = comparable(node);
  const right = comparable(go);
  const take = (list: Comparable[], operation: string) => {
    const index = list.findIndex((item) => item.operation === operation);
    return index < 0 ? undefined : list.splice(index, 1)[0];
  };

  const nodeTools = take(left, "graphjin.tools_list");
  const goTools = take(right, "graphjin.tools_list");
  if (JSON.stringify(nodeTools) !== JSON.stringify(goTools)) {
    differences.push("graphjin.tools_list differs");
  }

  const nodePrefetch = take(left, "memory.prefetched");
  const goPrefetch = take(right, "memory.prefetched");
  if (!nodePrefetch !== !goPrefetch) {
    differences.push("memory.prefetched is on one side only");
  } else if (nodePrefetch && goPrefetch) {
    const listed = (item: Comparable) =>
      ((item.event as { evidence: { memories: Memory[] } }).evidence.memories ?? []).map(memoryKey);
    const goKeys = new Set(listed(goPrefetch));
    for (const key of listed(nodePrefetch)) {
      if (!goKeys.has(key)) differences.push(`memory.prefetched misses ${key}`);
    }
    if (nodePrefetch.source !== goPrefetch.source) differences.push("memory.prefetched source differs");
  }

  const operations = (list: Comparable[]) => list.map((item) => item.operation).join(",");
  if (operations(left) !== operations(right)) {
    differences.push(`operations differ: ${operations(left)} | ${operations(right)}`);
    return differences;
  }
  left.forEach((item, index) => {
    if (JSON.stringify(item) !== JSON.stringify(right[index])) {
      differences.push(`${item.operation} differs`);
    }
  });
  if (JSON.stringify(node.modelTurns ?? []) !== JSON.stringify(go.modelTurns ?? [])) {
    differences.push("model turns differ");
  }
  return differences;
}

function asGo(record: Evidence): Evidence {
  const copy = structuredClone(record);
  const tools = copy.events.find((event) => event.operation === "graphjin.tools_list")!;
  const rest = copy.events.filter((event) => event !== tools);
  const ordered = [rest[0]!, tools, ...rest.slice(1)];
  const renumber = new Map(ordered.map((event, index) => [event.sequence, index + 1]));
  copy.steps = copy.steps.map((step) => ({ ...step, sequence: renumber.get(step.sequence)! }));
  copy.events = ordered.map((event) => ({
    ...event,
    sequence: renumber.get(event.sequence)!,
    runId: "11111111-1111-4111-8111-111111111111",
    timestamp: "2030-01-01T00:00:00.000Z",
    durationMs: 3,
    ...(HOST_OPERATIONS.has(event.operation) ? {} : { source: "trusted-broker" }),
  }));
  return JSON.parse(JSON.stringify(copy).replace(UUID, () => crypto.randomUUID())) as Evidence;
}

describe("eval trace parity: Node sink against the Go trace file", () => {
  it("holds the recorded Node trace of the case", () => {
    const record = nodeRecord();
    expect(record.events.map((event) => event.operation)).toEqual([
      "memory.prefetched",
      "memory.search",
      "skill.loaded",
      "files.search",
      "workflow.list",
      "graphjin.tools_list",
      "graphjin.execute",
    ]);
    expect(record.steps).toHaveLength(5);
  });

  it("accepts the three known differences and nothing else", () => {
    const record = nodeRecord();
    const go = asGo(record);
    expect(traceParityDifferences(record, go)).toEqual([]);

    const prefetchWider = structuredClone(go);
    const prefetch = prefetchWider.events.find((event) => event.operation === "memory.prefetched")!;
    const memories = (prefetch.evidence as { memories: Memory[] }).memories;
    memories.unshift({ ...memories[0]!, contentDigest: "sha256:other" });
    expect(traceParityDifferences(record, prefetchWider)).toEqual([]);

    const prefetchMissing = structuredClone(go);
    (prefetchMissing.events.find((event) => event.operation === "memory.prefetched")!.evidence as {
      memories: Memory[];
    }).memories = [];
    expect(traceParityDifferences(record, prefetchMissing)).toHaveLength(1);

    const row = structuredClone(go);
    const search = row.steps.find((step) => step.operation === "memory.search")!;
    (search.rows as Array<{ score: number }>)[0]!.score = 0.1;
    expect(traceParityDifferences(record, row)).toEqual(["memory.search differs"]);

    const request = structuredClone(go);
    request.events.find((event) => event.operation === "files.search")!.requestDigest = "sha256:other";
    expect(traceParityDifferences(record, request)).toEqual(["files.search differs"]);

    const status = structuredClone(go);
    status.events.find((event) => event.operation === "graphjin.execute")!.status = "error";
    expect(traceParityDifferences(record, status)).toEqual(["graphjin.execute differs"]);

    const hostSource = structuredClone(go);
    hostSource.events.find((event) => event.operation === "skill.loaded")!.source = "trusted-broker";
    expect(traceParityDifferences(record, hostSource)).toEqual(["skill.loaded differs"]);

    const dropped = structuredClone(go);
    dropped.events = dropped.events.filter((event) => event.operation !== "workflow.list");
    expect(traceParityDifferences(record, dropped)[0]).toMatch(/^operations differ/);

    const turns = structuredClone(go);
    turns.modelTurns = [];
    expect(traceParityDifferences(record, turns)).toEqual(["model turns differ"]);
  });
});

async function evalStackUp(): Promise<boolean> {
  const url = process.env.OPENNEKO_EVAL_ADVENTUREWORKS_GRAPHQL_URL;
  if (!url || !process.env.OPENNEKO_EVAL_ADVENTUREWORKS_DATABASE_URL || !(await dbReachable())) return false;
  try {
    await fetch(url, { method: "POST", signal: AbortSignal.timeout(3_000) });
    return true;
  } catch {
    return false;
  }
}

const stackUp = await evalStackUp();

describe.skipIf(!stackUp)("eval trace parity on the eval install", () => {
  it(
    "gives the Node record for the case through web, serve and the trace file",
    async () => {
      const loaded = await loadEval(CONFIG);
      const plan = createEvalPlan(loaded);
      const slot = plan.slots.find((candidate) => candidate.caseId === CASE_ID && candidate.repetition === 1)!;
      const driver = createOpenNekoBackendDriver({ loaded, plan }, { host: { lockWaitMs: 600_000 } });
      const observer = { async observe() {} };
      try {
        await driver.preflight({ loaded, plan });
        const oracle = await driver.resolveOracle(slot.case, { loaded, plan });
        const execution = await driver.execute({
          runId: "vitest-trace-parity",
          slot,
          oracle,
          observer,
          signal: new AbortController().signal,
        } as unknown as Parameters<typeof driver.execute>[0]);
        const go = execution.semanticEvidence as Evidence;
        expect(traceParityDifferences(nodeRecord(), go)).toEqual([]);
        const sources = new Set(go.events.filter((event) => !HOST_OPERATIONS.has(event.operation)).map((event) => event.source));
        expect([...sources]).toEqual(["trusted-broker"]);
        expect(execution.measurements?.totalTokens).toBeGreaterThan(0);
      } finally {
        await driver.close?.();
      }
    },
    600_000,
  );
});
