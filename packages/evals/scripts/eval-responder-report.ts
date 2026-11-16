import { readFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { parse } from "yaml";

type Check = { assertionId: string; passed: boolean; gate: boolean };
type Grade = { due: boolean; passed: boolean };
type Result = {
  runId: string;
  caseId: string;
  status: string;
  measurements?: Record<string, unknown>;
  score?: { verdict: string; checks: Check[] };
};

const CASES = fileURLToPath(new URL("../../../evals/datasets/openneko-backend/cases/", import.meta.url));
const NAMED_CASES = ["b12a-prompt-injection", "s03-workflow-action-approve", "s04-workflow-action-reject", "s07-approval-bypass"];
const GRADERS = ["closingBlocks", "cards", "noLeak"] as const;

const kinds = new Map<string, string>();
function assertionKind(caseId: string, assertionId: string): string | undefined {
  const key = `${caseId}/${assertionId}`;
  if (!kinds.has(key)) {
    const doc = parse(readFileSync(join(CASES, `${caseId}.yaml`), "utf8")) as {
      assertions?: { id: string; kind: string }[];
    };
    for (const assertion of doc.assertions ?? []) kinds.set(`${caseId}/${assertion.id}`, assertion.kind);
  }
  return kinds.get(key);
}

function percentile(values: number[], p: number): number | undefined {
  if (!values.length) return undefined;
  const sorted = [...values].sort((a, b) => a - b);
  return sorted[Math.min(sorted.length - 1, Math.ceil((p / 100) * sorted.length) - 1)];
}

function numbers(results: Result[], name: string): number[] {
  return results
    .map((result) => result.measurements?.[name])
    .filter((value): value is number => typeof value === "number" && Number.isFinite(value));
}

function seconds(ms: number | undefined): string {
  return ms === undefined ? "-" : `${(ms / 1000).toFixed(1)} s`;
}

export function summarizeRun(results: Result[]) {
  const scored = results.filter((result) => result.score);
  const answerGates = new Map<string, { passed: number; total: number }>();
  for (const result of scored) {
    for (const check of result.score!.checks) {
      const kind = assertionKind(result.caseId, check.assertionId);
      if (!check.gate || !kind?.startsWith("answer.")) continue;
      const entry = answerGates.get(kind) ?? { passed: 0, total: 0 };
      entry.total += 1;
      if (check.passed) entry.passed += 1;
      answerGates.set(kind, entry);
    }
  }
  const graders = Object.fromEntries(
    GRADERS.map((name) => {
      const grades = results
        .map((result) => (result.measurements?.responderGrades as Record<string, Grade> | undefined)?.[name])
        .filter((grade): grade is Grade => Boolean(grade?.due));
      return [name, { passed: grades.filter((grade) => grade.passed).length, due: grades.length }];
    }),
  );
  const latency = numbers(results, "responderLatencyMs");
  const firstOutput = numbers(results, "firstOutputMs");
  const wall = numbers(results, "wallDurationMs");
  return {
    runId: results[0]?.runId ?? "",
    episodes: results.length,
    passed: scored.filter((result) => result.score!.verdict === "pass").length,
    executionFailures: results.filter((result) => result.status !== "completed").length,
    responderEfforts: [...new Set(results.map((result) => String(result.measurements?.responderReasoningEffort ?? "default")))],
    answerGates: Object.fromEntries([...answerGates].sort(([a], [b]) => a.localeCompare(b))),
    graders,
    namedCases: Object.fromEntries(
      NAMED_CASES.map((id) => [id, results.find((result) => result.caseId === id)?.score?.verdict ?? "missing"]),
    ),
    responderLatencyMs: { p50: percentile(latency, 50), p95: percentile(latency, 95), episodes: latency.length },
    firstOutputMs: { p50: percentile(firstOutput, 50), p95: percentile(firstOutput, 95) },
    wallDurationMs: { p50: percentile(wall, 50), p95: percentile(wall, 95), total: wall.reduce((a, b) => a + b, 0) },
    estimatedCostUsd: numbers(results, "estimatedCostUsd").reduce((a, b) => a + b, 0),
  };
}

export function summaryLine(label: string, summary: ReturnType<typeof summarizeRun>): string {
  const graders = GRADERS.map((name) => `${name} ${summary.graders[name]!.passed}/${summary.graders[name]!.due}`).join(", ");
  return [
    label,
    summary.runId,
    `passed ${summary.passed}/${summary.episodes}`,
    `effort ${summary.responderEfforts.join("+")}`,
    graders,
    `responder p50 ${seconds(summary.responderLatencyMs.p50)} p95 ${seconds(summary.responderLatencyMs.p95)}`,
    `first text p50 ${seconds(summary.firstOutputMs.p50)}`,
    `cost $${summary.estimatedCostUsd.toFixed(2)}`,
  ].join(" | ");
}

function readResults(folder: string): Result[] {
  return readFileSync(join(folder, "results.jsonl"), "utf8")
    .split("\n")
    .filter((line) => line.trim())
    .map((line) => JSON.parse(line) as Result);
}

if (process.argv[1] && fileURLToPath(import.meta.url) === process.argv[1]) {
  const [mode, ...rest] = process.argv.slice(2);
  if (mode === "--line" && rest.length === 2) {
    console.log(summaryLine(rest[0]!, summarizeRun(readResults(rest[1]!))));
  } else if (mode === "--json" && rest.length > 0) {
    console.log(JSON.stringify(rest.map((folder) => ({ folder, ...summarizeRun(readResults(folder)) })), null, 2));
  } else {
    console.error("usage: eval-responder-report --line <label> <result folder> | --json <result folder>...");
    process.exit(2);
  }
}
