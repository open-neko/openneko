import { mkdir, readFile, readdir, writeFile } from "node:fs/promises";
import { dirname, join, resolve } from "node:path";
import { contentDigest, textDigest } from "./canonical";
import { redactText } from "@neko/telemetry";
import { summarizeEpisodes } from "./scoring";
import {
  EpisodeSchema,
  ManifestSchema,
  ResultLineSchema,
  ResultManifestSchema,
  SummarySchema,
  type EvalEpisode,
  type EvalManifest,
  type EvalSummaryDocument,
  type EvalThresholdPolicy,
} from "./schemas";

const SECRET_SHAPE =
  /(?:\bsk-[A-Za-z0-9_-]{12,}\b|\bAIza[0-9A-Za-z_-]{20,}\b|\bgh[pousr]_[A-Za-z0-9]{20,}\b|\bglpat-[A-Za-z0-9_-]{12,}\b|\bAKIA[0-9A-Z]{16}\b|\bxox[baprs]-[A-Za-z0-9-]{12,}\b|\bBearer\s+[A-Za-z0-9._~+/=-]{8,}\b|-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----)/u;
const CONTENT_KEY =
  /(?:prompt|response|completion|message|query|sql|graphql|tool[._-]?(?:input|output|result|argument)|content|body|secret|password|api[._-]?key|authorization|cookie)/iu;
const CREDENTIAL_KEY =
  /^(?:api[_-]?key|access[_-]?token|refresh[_-]?token|secret|password|authorization)$/iu;
const MAX_CHECKED_ARTIFACT_BYTES = 10 * 1024 * 1024;

type ReportManifest = Pick<
  EvalManifest,
  | "runId"
  | "configId"
  | "suiteId"
  | "attestation"
  | "suiteGates"
  | "thresholdPolicy"
  | "thresholdPolicyDigest"
  | "runtimeBudgets"
  | "source"
  | "compatibility"
  | "datasetFingerprint"
  | "effectiveConfig"
>;

function assertNoLiteralCredentials(value: unknown, path: string): void {
  if (Array.isArray(value)) {
    value.forEach((item, index) => assertNoLiteralCredentials(item, `${path}[${index}]`));
    return;
  }
  if (!value || typeof value !== "object") return;
  for (const [key, nested] of Object.entries(value as Record<string, unknown>)) {
    if (
      CREDENTIAL_KEY.test(key) &&
      typeof nested === "string" &&
      !/^env:[A-Z][A-Z0-9_]*$/u.test(nested)
    ) {
      throw new Error(`${path}.${key} contains a literal credential`);
    }
    assertNoLiteralCredentials(nested, `${path}.${key}`);
  }
}

function sanitizePublicValue(value: unknown, depth = 0): unknown {
  if (depth > 8) return "[TRUNCATED]";
  if (typeof value === "string") return redactText(value).slice(0, 2048);
  if (typeof value === "number" || typeof value === "boolean" || value == null) return value;
  if (Array.isArray(value)) {
    return value.slice(0, 256).map((item) => sanitizePublicValue(item, depth + 1));
  }
  if (typeof value === "object") {
    return Object.fromEntries(
      Object.entries(value as Record<string, unknown>)
        .filter(([key]) => !CONTENT_KEY.test(key))
        .map(([key, nested]) => [key, sanitizePublicValue(nested, depth + 1)]),
    );
  }
  return String(value).slice(0, 256);
}

function legacyMarkdown(summary: ReturnType<typeof summarizeEpisodes>, manifest: ReportManifest): string {
  const pct = (value: number) => `${(value * 100).toFixed(1)}%`;
  const accepted = evaluateSuiteGates(summary, manifest.suiteGates);
  const duration = (value: number | null) =>
    value === null ? "unavailable" : `${Math.round(value)} ms`;
  const money = (value: { count: number; coverage: number; total: number }) =>
    value.count === 0
      ? "unavailable"
      : `$${value.total.toFixed(6)} (${pct(value.coverage)} coverage)`;
  const capabilityGates = Object.entries(
    manifest.suiteGates.min_capability_task_pass_rate ?? {},
  )
    .sort(([left], [right]) => left.localeCompare(right))
    .map(([capability, minimum]) => {
      const actual = summary.byCapability[capability]?.passRate;
      return `${capability}=${actual === undefined ? "missing" : pct(actual)} (min ${pct(minimum)})`;
    });
  return `# Eval result: ${manifest.configId}\n\n` +
    `- Run: \`${manifest.runId}\`\n` +
    `- Suite: \`${manifest.suiteId}\`\n` +
    `- Attestation: ${manifest.attestation}\n` +
    `- Accepted: ${accepted ? "yes" : "no"}\n` +
    `- Source: \`${manifest.source.commit}\`${manifest.source.dirty ? " (dirty)" : ""}\n` +
    `- Task pass rate: ${pct(summary.taskPassRate)} (${summary.passedTasks}/${summary.taskCount})\n` +
    `- Macro ground truth: ${pct(summary.macro.groundTruth)}\n` +
    `- Macro method: ${pct(summary.macro.method)}\n` +
    `- Macro behavior: ${pct(summary.macro.behavior)}\n` +
    `- Macro safety: ${pct(summary.macro.safety)}\n` +
    `- Macro efficiency: ${pct(summary.macro.efficiency)}\n` +
    `- Method score coverage: ${pct(summary.coverage.method)}\n` +
    `- Safety score coverage: ${pct(summary.coverage.safety)}\n` +
    `- Efficiency score coverage: ${pct(summary.coverage.efficiency)}\n` +
    `- Mean consistency: ${pct(summary.meanConsistency)}\n` +
    `- Wall latency p50 / p95: ${duration(summary.measurements.wallDurationMs.p50)} / ${duration(summary.measurements.wallDurationMs.p95)}\n` +
    `- Tool calls mean / p95: ${summary.measurements.toolCalls.mean ?? "unavailable"} / ${summary.measurements.toolCalls.p95 ?? "unavailable"}\n` +
    `- Exact repeated tool calls: ${summary.measurements.repeatedToolCalls.total} (${pct(summary.measurements.repeatedToolCalls.coverage)} coverage)\n` +
    `- Total tokens: ${summary.measurements.totalTokens.total} (${pct(summary.measurements.totalTokens.coverage)} coverage)\n` +
    `- Estimated / billed cost: ${money(summary.measurements.estimatedCostUsd)} / ${money(summary.measurements.billedCostUsd)}\n` +
    `- Complete / available usage coverage: ${pct(summary.measurements.usageCoverage.completeRate)} / ${pct(summary.measurements.usageCoverage.availableRate)}\n` +
    `- Complete / available cost coverage: ${pct(summary.measurements.costCoverage.completeRate)} / ${pct(summary.measurements.costCoverage.availableRate)}\n` +
    `- Datasets / product paths: ${Object.keys(summary.byDataset).length} / ${Object.keys(summary.byProductPath).length}\n` +
    `- Semantic IDs exercised: ${Object.keys(summary.bySemantic).length}\n` +
    `- Execution failures: ${summary.executionFailures}\n` +
    `- Safety gate failures: ${summary.safetyGateFailures}\n` +
    `- Unsafe effects: ${summary.unsafeEffects} across ${summary.unsafeEffectEpisodes} episodes${
      Object.keys(summary.unsafeEffectsByKind).length
        ? ` (${Object.entries(summary.unsafeEffectsByKind)
            .map(([kind, count]) => `${kind}=${count}`)
            .join(", ")})`
        : ""
    }\n` +
    (capabilityGates.length
      ? `- Capability gates: ${capabilityGates.join(", ")}\n`
      : "");
}

// The first result/v1 report renderer predated method/efficiency display,
// tool-call aggregates, unsafe-effect summaries, capability gates, and the
// explicit accepted flag. The schema version intentionally stayed v1, so the
// presence of those aggregate fields is the only durable renderer discriminator
// available for already-published artifacts.
function initialLegacyMarkdown(
  summary: ReturnType<typeof summarizeEpisodes>,
  manifest: ReportManifest,
): string {
  const localPct = (value: number) => `${(value * 100).toFixed(1)}%`;
  const localDuration = (value: number | null) =>
    value === null ? "unavailable" : `${Math.round(value)} ms`;
  const money = (value: { count: number; coverage: number; total: number }) =>
    value.count === 0
      ? "unavailable"
      : `$${value.total.toFixed(6)} (${localPct(value.coverage)} coverage)`;
  return `# Eval result: ${manifest.configId}\n\n` +
    `- Run: \`${manifest.runId}\`\n` +
    `- Suite: \`${manifest.suiteId}\`\n` +
    `- Attestation: ${manifest.attestation}\n` +
    `- Source: \`${manifest.source.commit}\`${manifest.source.dirty ? " (dirty)" : ""}\n` +
    `- Task pass rate: ${localPct(summary.taskPassRate)} (${summary.passedTasks}/${summary.taskCount})\n` +
    `- Macro ground truth: ${localPct(summary.macro.groundTruth)}\n` +
    `- Macro behavior: ${localPct(summary.macro.behavior)}\n` +
    `- Macro safety: ${localPct(summary.macro.safety)}\n` +
    `- Method score coverage: ${localPct(summary.coverage.method)}\n` +
    `- Safety score coverage: ${localPct(summary.coverage.safety)}\n` +
    `- Mean consistency: ${localPct(summary.meanConsistency)}\n` +
    `- Wall latency p50 / p95: ${localDuration(summary.measurements.wallDurationMs.p50)} / ${localDuration(summary.measurements.wallDurationMs.p95)}\n` +
    `- Total tokens: ${summary.measurements.totalTokens.total} (${localPct(summary.measurements.totalTokens.coverage)} coverage)\n` +
    `- Estimated / billed cost: ${money(summary.measurements.estimatedCostUsd)} / ${money(summary.measurements.billedCostUsd)}\n` +
    `- Complete / available usage coverage: ${localPct(summary.measurements.usageCoverage.completeRate)} / ${localPct(summary.measurements.usageCoverage.availableRate)}\n` +
    `- Complete / available cost coverage: ${localPct(summary.measurements.costCoverage.completeRate)} / ${localPct(summary.measurements.costCoverage.availableRate)}\n` +
    `- Datasets / product paths: ${Object.keys(summary.byDataset).length} / ${Object.keys(summary.byProductPath).length}\n` +
    `- Semantic IDs exercised: ${Object.keys(summary.bySemantic).length}\n` +
    `- Execution failures: ${summary.executionFailures}\n` +
    `- Safety gate failures: ${summary.safetyGateFailures}\n`;
}

function projectToStoredShape(value: unknown, shape: unknown): unknown {
  if (Array.isArray(shape)) {
    if (!Array.isArray(value)) return value;
    return value.map((item, index) =>
      projectToStoredShape(item, shape[index]),
    );
  }
  if (
    shape &&
    typeof shape === "object" &&
    value &&
    typeof value === "object" &&
    !Array.isArray(value)
  ) {
    const source = value as Record<string, unknown>;
    return Object.fromEntries(
      Object.entries(shape as Record<string, unknown>).map(
        ([key, nestedShape]) => [
          key,
          projectToStoredShape(source[key], nestedShape),
        ],
      ),
    );
  }
  return value;
}

const FRIENDLY_REPORT_VERSION = "openneko.eval.report.friendly.md/v1";
const TECHNICAL_REPORT_VERSION = "openneko.eval.report.technical.md/v1";

type AggregateSummary = ReturnType<typeof summarizeEpisodes>;
type GateResult = NonNullable<EvalSummaryDocument["qualification"]>["gateResults"][number];

function comparisonPass(
  operator: "gte" | "lte" | "eq",
  observed: number,
  required: number,
): boolean {
  if (operator === "gte") return observed >= required;
  if (operator === "lte") return observed <= required;
  return observed === required;
}

function thresholdObservation(
  summary: AggregateSummary,
  gate: EvalThresholdPolicy["gates"][number],
): { observed: number | null; samples: number; coverage: number | null } {
  if (gate.metric === "macro-ground-truth") {
    return {
      observed: summary.macro.groundTruth,
      samples: summary.taskCount,
      coverage: summary.coverage.groundTruth,
    };
  }
  if (gate.metric === "macro-method") {
    return {
      observed: summary.macro.method,
      samples: summary.taskCount,
      coverage: summary.coverage.method,
    };
  }
  if (gate.metric === "macro-behavior") {
    return {
      observed: summary.macro.behavior,
      samples: summary.taskCount,
      coverage: summary.coverage.behavior,
    };
  }
  if (gate.metric === "full-task-pass-rate") {
    return {
      observed: summary.taskPassRate,
      samples: summary.taskCount,
      coverage: summary.expectedEpisodes
        ? (summary.expectedEpisodes - summary.executionFailures) /
          summary.expectedEpisodes
        : 0,
    };
  }
  if (gate.metric === "episode-completion-rate") {
    return {
      observed: summary.expectedEpisodes
        ? (summary.expectedEpisodes - summary.executionFailures) /
          summary.expectedEpisodes
        : 0,
      samples: summary.expectedEpisodes,
      coverage: 1,
    };
  }
  if (gate.metric === "token-usage-coverage") {
    return {
      observed: summary.measurements.usageCoverage.completeRate,
      samples: summary.expectedEpisodes,
      coverage: 1,
    };
  }
  if (gate.metric.startsWith("capability-")) {
    const capability = gate.capability
      ? summary.assertionCapabilities[gate.capability]
      : undefined;
    const observed = capability
      ? gate.metric === "capability-unconditional-pass-rate"
        ? capability.unconditionalPassRate
        : gate.metric === "capability-conditional-pass-rate"
          ? capability.conditionalPassRate
          : capability.coverage
      : null;
    return {
      observed,
      samples: capability?.attemptedAssertions ?? 0,
      coverage: capability?.coverage ?? 0,
    };
  }
  if (gate.metric === "safety-assertion-failures") {
    return {
      observed: summary.safetyGateFailures,
      samples: summary.scoredEpisodes,
      coverage: summary.coverage.safety,
    };
  }
  const outcome = gate.security_outcome ?? "completed";
  const severityOrder = ["low", "medium", "high", "critical"] as const;
  const minimumSeverity = gate.severity_at_least
    ? severityOrder.indexOf(gate.severity_at_least)
    : 0;
  const observed = gate.severity_at_least
    ? severityOrder
        .slice(minimumSeverity)
        .reduce(
          (total, severity) =>
            total +
            (summary.securityOutcomes.byOutcomeSeverity[
              `${outcome}:${severity}`
            ] ?? 0),
          0,
        )
    : (summary.securityOutcomes.byOutcome[outcome] ?? 0);
  return {
    observed,
    samples: summary.expectedEpisodes,
    coverage: summary.expectedEpisodes
      ? (summary.expectedEpisodes - summary.executionFailures) /
        summary.expectedEpisodes
      : 0,
  };
}

export function evaluateThresholdPolicy(
  summary: AggregateSummary,
  policy: EvalThresholdPolicy,
) {
  const gateResults: GateResult[] = policy.gates.map((gate) => {
    const observation = thresholdObservation(summary, gate);
    const insufficient =
      observation.observed === null ||
      (gate.minimum_samples !== undefined &&
        observation.samples < gate.minimum_samples) ||
      (gate.minimum_coverage !== undefined &&
        (observation.coverage ?? 0) < gate.minimum_coverage);
    const status = insufficient
      ? "insufficient_evidence"
      : comparisonPass(gate.operator, observation.observed!, gate.value)
        ? "pass"
        : "fail";
    const explanation = insufficient
      ? `needs at least ${gate.minimum_samples ?? 0} samples and ${(
          (gate.minimum_coverage ?? 0) * 100
        ).toFixed(1)}% coverage`
      : `${gate.metric}${gate.capability ? ` for ${gate.capability}` : ""} ${
          gate.operator
        } ${gate.value}`;
    return {
      id: gate.id,
      qualification: gate.qualification,
      metric: gate.metric,
      ...(gate.capability ? { capability: gate.capability } : {}),
      operator: gate.operator,
      required: gate.value,
      observed: observation.observed,
      samples: observation.samples,
      coverage: observation.coverage,
      enforcement: gate.enforcement,
      severity: gate.severity,
      owner: gate.owner,
      rationale: gate.rationale,
      calibrationRuns: gate.calibration_runs,
      status,
      explanation,
    };
  });
  const state = (dimension: GateResult["qualification"]) => {
    const required = gateResults.filter(
      (gate) => gate.qualification === dimension && gate.enforcement === "required",
    );
    if (!required.length || required.some((gate) => gate.status === "insufficient_evidence")) {
      return "insufficient_evidence" as const;
    }
    return required.some((gate) => gate.status === "fail")
      ? ("fail" as const)
      : ("pass" as const);
  };
  const capabilityQualification = state("capability");
  const reliabilityQualification = state("reliability");
  const safetyQualification = state("safety");
  return {
    integrity: "valid" as const,
    capabilityQualification,
    reliabilityQualification,
    safetyQualification,
    productionQualification:
      capabilityQualification === "pass" &&
      reliabilityQualification === "pass" &&
      safetyQualification === "pass"
        ? ("pass" as const)
        : ("fail" as const),
    gateResults,
  };
}

function summaryWithQualification(
  summary: AggregateSummary,
  manifest: Pick<ReportManifest, "thresholdPolicy">,
): EvalSummaryDocument {
  return SummarySchema.parse({
    ...summary,
    ...(manifest.thresholdPolicy
      ? { qualification: evaluateThresholdPolicy(summary, manifest.thresholdPolicy) }
      : {}),
  });
}

function pct(value: number | null): string {
  return value === null ? "n/a" : `${(value * 100).toFixed(1)}%`;
}

function duration(value: number | null): string {
  if (value === null) return "unavailable";
  if (value < 1_000) return `${Math.round(value)}ms`;
  const seconds = Math.round(value / 1_000);
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  return `${minutes}m ${String(seconds % 60).padStart(2, "0")}s`;
}

function compactNumber(value: number): string {
  if (value >= 1_000_000_000) return `${(value / 1_000_000_000).toFixed(1)}B`;
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(1)}M`;
  if (value >= 1_000) return `${(value / 1_000).toFixed(1)}K`;
  return value.toFixed(value % 1 === 0 ? 0 : 1);
}

function mdCell(value: unknown): string {
  return String(value ?? "unavailable")
    .replace(/\|/gu, "\\|")
    .replace(/[\r\n]+/gu, " ");
}

function policyCapabilityName(
  policy: EvalThresholdPolicy,
  id: string,
): string {
  return policy.capabilities.find((capability) => capability.id === id)
    ?.display_name ?? id;
}

function failedGateRows(summary: EvalSummaryDocument): GateResult[] {
  return (summary.qualification?.gateResults ?? []).filter(
    (gate) => gate.enforcement === "required" && gate.status !== "pass",
  );
}

function friendlyMarkdown(
  summary: EvalSummaryDocument,
  manifest: ReportManifest,
): string {
  const qualification = summary.qualification!;
  const completed =
    summary.securityOutcomes.byOutcome.completed ?? summary.unsafeEffects;
  const completionRate = summary.expectedEpisodes
    ? (summary.expectedEpisodes - summary.executionFailures) /
      summary.expectedEpisodes
    : 0;
  const failed = failedGateRows(summary);
  const reasons = failed.length
    ? failed
        .slice(0, 3)
        .map((gate) =>
          gate.status === "insufficient_evidence"
            ? `${gate.id} lacked enough evidence`
            : `${gate.id} missed its required value`,
        )
        .join(", ")
    : "all required gates passed";
  const variants = Object.entries(manifest.runtimeBudgets?.configuredModels ?? {});
  const model = variants.length
    ? variants
        .map(
          ([variant, identity]) =>
            `${variant}: ${identity.provider}:${identity.model}`,
        )
        .join(", ")
    : "unavailable";
  const runtime = manifest.runtimeBudgets;
  const passingCapabilities = (summary.qualification?.gateResults ?? [])
    .filter(
      (gate) =>
        gate.qualification === "capability" &&
        gate.enforcement === "required" &&
        gate.status === "pass" &&
        gate.capability,
    )
    .map((gate) => gate.capability!)
    .filter((value, index, values) => values.indexOf(value) === index)
    .sort();

  let out = `# OpenNeko backend evaluation \`${mdCell(manifest.runId)}\`\n\n`;
  out += `> Friendly report schema: \`${FRIENDLY_REPORT_VERSION}\`\n\n`;
  out += `## Qualification: ${qualification.productionQualification === "pass" ? "Accepted" : "Rejected"} · Integrity: ${qualification.integrity === "valid" ? "Valid" : "Invalid"} · Completed unsafe effects: ${completed}\n\n`;
  out += `The candidate completed ${summary.expectedEpisodes - summary.executionFailures} of ${summary.expectedEpisodes} episodes and passed ${summary.passedTasks} of ${summary.taskCount} tasks. It ${qualification.productionQualification === "pass" ? "qualified because" : "did not qualify because"} ${reasons}. ${completed === 0 ? "No completed prohibited effect was observed." : `${completed} completed prohibited effect${completed === 1 ? " was" : "s were"} observed.`}\n\n`;
  out += "## Results at a glance\n\n";
  out += "| Result | Observed |\n| --- | ---: |\n";
  out += `| Full task pass | ${summary.passedTasks}/${summary.taskCount} (${pct(summary.taskPassRate)}) |\n`;
  out += `| Episode reliability | ${summary.expectedEpisodes - summary.executionFailures}/${summary.expectedEpisodes} (${pct(completionRate)}) |\n`;
  out += `| Ground truth | ${pct(summary.macro.groundTruth)} |\n`;
  out += `| Required method | ${pct(summary.macro.method)} |\n`;
  out += `| Behavior | ${pct(summary.macro.behavior)} |\n`;
  out += `| Safety score | ${pct(summary.macro.safety)} |\n`;
  out += `| Latency p50 / p95 | ${duration(summary.measurements.wallDurationMs.p50)} / ${duration(summary.measurements.wallDurationMs.p95)} |\n`;
  out += `| Total tokens | ${compactNumber(summary.measurements.totalTokens.total)} (${pct(summary.measurements.totalTokens.coverage)} coverage) |\n`;
  out += `| Estimated cost | ${summary.measurements.estimatedCostUsd.count ? `$${summary.measurements.estimatedCostUsd.total.toFixed(2)}` : "unavailable"} |\n`;
  out += `| Completed unsafe effects | ${completed} |\n\n`;

  out += "## Why qualification failed\n\n";
  if (!failed.length) {
    out += "No required qualification gate failed.\n\n";
  } else {
    out += "| Gate | Dimension | Required | Observed | Evidence |\n| --- | --- | ---: | ---: | --- |\n";
    for (const gate of failed) {
      out += `| ${mdCell(gate.id)} | ${mdCell(gate.qualification)} | ${mdCell(`${gate.operator} ${gate.required}`)} | ${gate.observed === null ? "unavailable" : mdCell(gate.observed.toFixed(3))} | ${mdCell(gate.rationale)} (${gate.samples} samples, ${pct(gate.coverage)} coverage) |\n`;
    }
    out += "\n";
  }

  out += "## What worked\n\n";
  if (!passingCapabilities.length) {
    out += "No required assertion-level capability gate fully qualified.\n\n";
  } else {
    for (const capability of passingCapabilities) {
      out += `- ${mdCell(policyCapabilityName(manifest.thresholdPolicy!, capability))}\n`;
    }
    out += "\n";
  }

  out += "## Failure breakdown\n\n";
  if (!Object.keys(summary.failureTypes).length) {
    out += "No execution failures.\n\n";
  } else {
    out += "| Failure type | Episodes |\n| --- | ---: |\n";
    for (const [kind, count] of Object.entries(summary.failureTypes).sort()) {
      out += `| ${mdCell(kind)} | ${count} |\n`;
    }
    out += "\n";
  }

  out += "## Safety outcomes\n\n";
  out += "| Outcome | Count |\n| --- | ---: |\n";
  for (const outcome of ["assertion_failed", "attempted", "blocked", "completed"]) {
    out += `| ${outcome.replace(/_/gu, " ")} | ${summary.securityOutcomes.byOutcome[outcome] ?? 0} |\n`;
  }
  out += "\nRetrieving untrusted passive content is not an unsafe effect by itself. A blocked prohibited attempt and a completed prohibited effect are reported separately.\n\n";

  out += "## Provenance\n\n";
  out += "| Field | Value |\n| --- | --- |\n";
  out += `| Suite | ${mdCell(manifest.suiteId)} |\n`;
  out += `| Candidate | ${mdCell(model)} |\n`;
  out += `| Source | ${mdCell(manifest.source.commit)}${manifest.source.dirty ? " (dirty)" : " (clean)"} |\n`;
  out += `| Attestation | ${mdCell(manifest.attestation)} |\n`;
  out += `| Repetitions | ${manifest.effectiveConfig.defaults.repetitions} |\n`;
  out += `| Episode timeout | ${runtime ? duration(runtime.perEpisodeTimeoutMs) : "unavailable"} |\n`;
  out += `| Tool-call ceiling | ${runtime ? mdCell(Object.values(runtime.toolCallCeilings).join(", ")) : "unavailable"} |\n`;
  out += `| Output-token limit | ${runtime?.providerOutputTokenLimit ?? "unresolved"} |\n`;
  out += `| Context limit | ${runtime?.modelContextTokenLimit ?? "unresolved"} |\n`;
  out += `| Threshold policy | ${mdCell(manifest.thresholdPolicy?.id ?? "legacy")} ${mdCell(manifest.thresholdPolicy?.version ?? "")} |\n`;
  out += "| Technical evidence | [technical.md](technical.md) |\n\n";
  out += "## Privacy\n\nThis report is rendered from the same sanitized projection as `summary.json` and contains no prompts, answers, tool payloads, tenant identifiers, or private semantic evidence.\n";
  return out;
}

function technicalMarkdown(
  summary: EvalSummaryDocument,
  manifest: ReportManifest,
): string {
  const policy = manifest.thresholdPolicy!;
  const qualification = summary.qualification!;
  const runtime = manifest.runtimeBudgets;
  let out = `# OpenNeko backend technical report \`${mdCell(manifest.runId)}\`\n\n`;
  out += `> Technical report schema: \`${TECHNICAL_REPORT_VERSION}\` · production qualification: **${qualification.productionQualification}**\n\n`;
  out += "## Identity and provenance\n\n| Field | Value |\n| --- | --- |\n";
  out += `| Run | ${mdCell(manifest.runId)} |\n| Config | ${mdCell(manifest.configId)} |\n| Suite | ${mdCell(manifest.suiteId)} |\n`;
  out += `| Source commit | ${mdCell(manifest.source.commit)} |\n| Source state | ${manifest.source.dirty ? "dirty" : "clean"} |\n| Attestation | ${mdCell(manifest.attestation)} |\n`;
  out += `| Threshold policy | ${mdCell(policy.id)} ${mdCell(policy.version)} (${policy.status}) |\n| Threshold policy digest | ${mdCell(manifest.thresholdPolicyDigest)} |\n`;
  out += `| Dataset fingerprint | ${mdCell(contentDigest(manifest.datasetFingerprint ?? null))} |\n| Scorer digest | ${mdCell(manifest.compatibility.scorerDigest)} |\n\n`;

  out += "## Qualification vector\n\n| Integrity | Capability | Reliability | Safety | Production |\n| --- | --- | --- | --- | --- |\n";
  out += `| ${qualification.integrity} | ${qualification.capabilityQualification} | ${qualification.reliabilityQualification} | ${qualification.safetyQualification} | ${qualification.productionQualification} |\n\n`;
  out += "## Qualification gates\n\n| Gate | Dimension | Metric | Selector | Rule | Observed | Samples | Coverage | Enforcement | Severity | Owner | Status |\n| --- | --- | --- | --- | ---: | ---: | ---: | ---: | --- | --- | --- | --- |\n";
  for (const gate of qualification.gateResults) {
    out += `| ${mdCell(gate.id)} | ${gate.qualification} | ${mdCell(gate.metric)} | ${mdCell(gate.capability ?? "all")} | ${gate.operator} ${gate.required} | ${gate.observed === null ? "n/a" : gate.observed.toFixed(4)} | ${gate.samples} | ${pct(gate.coverage)} | ${gate.enforcement} | ${gate.severity} | ${mdCell(gate.owner)} | ${gate.status} |\n`;
  }
  out += "\nGate rationale and calibration:\n\n| Gate | Rationale | Calibration runs |\n| --- | --- | --- |\n";
  for (const gate of qualification.gateResults) {
    out += `| ${mdCell(gate.id)} | ${mdCell(gate.rationale)} | ${mdCell(gate.calibrationRuns.join(", ") || "none; provisional")} |\n`;
  }
  out += "\n## Assertion-level capabilities\n\n| Capability | Attempted episodes | Completed | Unavailable | Assertions pass/fail/unavailable | Unconditional | Conditional | Coverage | 95% CI unconditional |\n| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |\n";
  for (const [id, capability] of Object.entries(summary.assertionCapabilities).sort()) {
    out += `| ${mdCell(policyCapabilityName(policy, id))} (\`${mdCell(id)}\`) | ${capability.attemptedEpisodes} | ${capability.completedEpisodes} | ${capability.unavailableEpisodes} | ${capability.passingAssertions}/${capability.failingAssertions}/${capability.unavailableAssertions} | ${pct(capability.unconditionalPassRate)} | ${pct(capability.conditionalPassRate)} | ${pct(capability.coverage)} | ${capability.unconditional95CI ? `${pct(capability.unconditional95CI[0])}–${pct(capability.unconditional95CI[1])}` : "n/a"} |\n`;
  }
  out += "\n## Security outcomes\n\n| Outcome | Kind | Count |\n| --- | --- | ---: |\n";
  if (!Object.keys(summary.securityOutcomes.byOutcomeKind).length) {
    out += "| none | None observed | 0 |\n";
  } else {
    for (const [key, count] of Object.entries(summary.securityOutcomes.byOutcomeKind).sort()) {
      const separator = key.indexOf(":");
      const outcome = separator >= 0 ? key.slice(0, separator) : "unknown";
      const kind = separator >= 0 ? key.slice(separator + 1) : key;
      out += `| ${mdCell(outcome)} | ${mdCell(kind)} | ${count} |\n`;
    }
  }
  out += "\n| Outcome | Count |\n| --- | ---: |\n";
  for (const outcome of ["assertion_failed", "attempted", "blocked", "completed"]) {
    out += `| ${outcome} | ${summary.securityOutcomes.byOutcome[outcome] ?? 0} |\n`;
  }
  out += "\n## Execution failures\n\n| Type | Episodes | Affected public task IDs |\n| --- | ---: | --- |\n";
  if (!summary.failureDetails.length) out += "| None | 0 | none |\n";
  for (const failure of summary.failureDetails) {
    out += `| ${mdCell(failure.type)} | ${failure.episodes} | ${mdCell(failure.taskIds.join(", "))} |\n`;
  }
  out += "\n## Task verdicts\n\n| Public task ID | Repetitions | Passes | Majority | Consistency | Unsafe effects |\n| --- | ---: | ---: | --- | ---: | ---: |\n";
  for (const task of summary.tasks) {
    out += `| ${mdCell(task.caseId)} | ${task.repetitions} | ${task.passes} | ${task.majorityPass ? "pass" : "fail"} | ${pct(task.consistency)} | ${task.unsafeEffects} |\n`;
  }
  out += "\n## Efficiency and usage\n\n| Measure | Count | Coverage | Total | Mean | p50 | p95 | Max |\n| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |\n";
  for (const [name, measurement] of Object.entries({
    wallDurationMs: summary.measurements.wallDurationMs,
    toolCalls: summary.measurements.toolCalls,
    repeatedToolCalls: summary.measurements.repeatedToolCalls,
    totalTokens: summary.measurements.totalTokens,
    estimatedCostUsd: summary.measurements.estimatedCostUsd,
    billedCostUsd: summary.measurements.billedCostUsd,
  })) {
    out += `| ${name} | ${measurement.count} | ${pct(measurement.coverage)} | ${compactNumber(measurement.total)} | ${measurement.mean === null ? "n/a" : compactNumber(measurement.mean)} | ${measurement.p50 === null ? "n/a" : compactNumber(measurement.p50)} | ${measurement.p95 === null ? "n/a" : compactNumber(measurement.p95)} | ${measurement.max === null ? "n/a" : compactNumber(measurement.max)} |\n`;
  }
  out += "\n## Runtime budgets\n\n| Budget | Value |\n| --- | --- |\n";
  out += `| Episode timeout | ${runtime ? duration(runtime.perEpisodeTimeoutMs) : "unavailable"} |\n| Provider output tokens | ${runtime?.providerOutputTokenLimit ?? "unresolved"} |\n| Model context tokens | ${runtime?.modelContextTokenLimit ?? "unresolved"} |\n`;
  out += `| Tool-call ceilings | ${mdCell(runtime ? JSON.stringify(runtime.toolCallCeilings) : "unavailable")} |\n| Harness max attempts | ${runtime?.harnessMaxAttempts ?? "unavailable"} |\n| Backend retry attempts | ${mdCell(runtime ? JSON.stringify(runtime.backendRetryAttempts) : "unavailable")} |\n| Concurrency/cache | ${runtime ? `${runtime.concurrency} / ${runtime.cacheState}` : "unavailable"} |\n`;
  for (const note of runtime?.resolutionNotes ?? []) out += `| Runtime notice | ${mdCell(note)} |\n`;
  out += "\n## Dataset and frozen-state proof\n\nThe sanitized dataset fingerprint is committed in `manifest.json`; its canonical digest is shown in provenance. Trusted pre/post state evidence remains in private resumable state and is represented publicly only by digests and typed outcomes.\n\n";
  out += "## Privacy\n\nThis report contains stable public task IDs and sanitized aggregates only. Prompts, answers, raw oracle errors, tenant identifiers, local episode paths, tool payloads, and private semantic evidence are excluded.\n";
  return out;
}

export function evaluateSuiteGates(
  summary: ReturnType<typeof summarizeEpisodes>,
  gates: EvalManifest["suiteGates"],
  thresholdPolicy?: EvalThresholdPolicy,
): boolean {
  if (thresholdPolicy) {
    return (
      evaluateThresholdPolicy(summary, thresholdPolicy)
        .productionQualification === "pass"
    );
  }
  return (
    summary.executionFailures === 0 &&
    (gates.min_macro_ground_truth === undefined ||
      summary.macro.groundTruth >= gates.min_macro_ground_truth) &&
    (gates.min_method === undefined ||
      summary.macro.method >= gates.min_method) &&
    (gates.min_behavior === undefined ||
      summary.macro.behavior >= gates.min_behavior) &&
    (gates.min_full_task_pass_rate === undefined ||
      summary.taskPassRate >= gates.min_full_task_pass_rate) &&
    (gates.min_token_usage_coverage === undefined ||
      summary.measurements.usageCoverage.completeRate >=
        gates.min_token_usage_coverage) &&
    Object.entries(gates.min_capability_task_pass_rate ?? {}).every(
      ([capability, minimum]) =>
        (summary.byCapability[capability]?.passRate ?? -1) >= minimum,
    ) &&
    (gates.max_unsafe_effects === undefined ||
      summary.unsafeEffects <= gates.max_unsafe_effects) &&
    (!gates.require_safety || summary.safetyGateFailures === 0)
  );
}

function publicEpisode(episode: EvalEpisode): unknown {
  return {
    schemaVersion: "openneko.eval.result/v1",
    runId: episode.runId,
    slotKey: episode.slotKey,
    ...(episode.pairKey ? { pairKey: episode.pairKey } : {}),
    ...(episode.treatment ? { treatment: episode.treatment } : {}),
    caseId: episode.caseId,
    caseContentId: episode.caseContentId,
    family: episode.family,
    productPath: episode.productPath,
    difficulty: episode.difficulty,
    capabilityTags: episode.capabilityTags,
    semantics: episode.semantics,
    ...(episode.assertionTargets
      ? { assertionTargets: episode.assertionTargets }
      : {}),
    variantId: episode.variantId,
    datasetId: episode.datasetId,
    repetition: episode.repetition,
    phase: episode.phase,
    attempt: episode.attempt,
    startedAt: episode.startedAt,
    finishedAt: episode.finishedAt,
    status: episode.status,
    measurements: sanitizePublicValue(episode.measurements),
    score: episode.score,
    ...(episode.errorType ? { errorType: episode.errorType } : {}),
    ...(episode.error ? { error: redactText(episode.error) } : {}),
    episodeDigest: episode.integrityDigest,
  };
}

export async function promoteResult(input: {
  manifest: EvalManifest;
  episodes: readonly EvalEpisode[];
  resultsRoot: string;
  rescore?: {
    sourceRunManifestDigest: string;
    sourceRescoreDigest: string;
    scorer: { id: string; version: string };
    scorerDigest: string;
  };
}): Promise<string> {
  const resultDir = resolve(input.resultsRoot, input.manifest.configId, input.manifest.runId);
  await mkdir(resultDir, { recursive: true });
  const sorted = [...input.episodes].sort((left, right) =>
    left.slotKey.localeCompare(right.slotKey),
  );
  const lines = sorted.map((episode) => JSON.stringify(publicEpisode(episode))).join("\n") + "\n";
  const summary = summaryWithQualification(
    summarizeEpisodes(sorted),
    input.manifest,
  );
  const summaryText = `${JSON.stringify(summary, null, 2)}\n`;
  const markdownText = input.manifest.thresholdPolicy
    ? friendlyMarkdown(summary, input.manifest)
    : legacyMarkdown(summary, input.manifest);
  const technicalText = input.manifest.thresholdPolicy
    ? technicalMarkdown(summary, input.manifest)
    : undefined;
  const accepted = evaluateSuiteGates(
    summary,
    input.manifest.suiteGates,
    input.manifest.thresholdPolicy,
  );
  const compatibility = input.rescore
    ? {
        ...input.manifest.compatibility,
        scorerDigest: input.rescore.scorerDigest,
      }
    : input.manifest.compatibility;
  await writeFile(join(resultDir, "results.jsonl"), lines, "utf8");
  await writeFile(join(resultDir, "summary.json"), summaryText, "utf8");
  await writeFile(join(resultDir, "summary.md"), markdownText, "utf8");
  if (technicalText) {
    await writeFile(join(resultDir, "technical.md"), technicalText, "utf8");
  }
  const artifactManifest = ResultManifestSchema.parse({
    schemaVersion: "openneko.eval.result-manifest/v1",
    runId: input.manifest.runId,
    configId: input.manifest.configId,
    suiteId: input.manifest.suiteId,
    attestation: input.manifest.attestation,
    accepted,
    suiteGates: input.manifest.suiteGates,
    ...(input.manifest.thresholdPolicy
      ? {
          thresholdPolicy: input.manifest.thresholdPolicy,
          thresholdPolicyDigest: input.manifest.thresholdPolicyDigest,
        }
      : {}),
    ...(input.manifest.runtimeBudgets
      ? { runtimeBudgets: input.manifest.runtimeBudgets }
      : {}),
    sourceRunManifestDigest:
      input.rescore?.sourceRunManifestDigest ?? contentDigest(input.manifest),
    ...(input.rescore
      ? {
          rescore: {
            sourceRescoreDigest: input.rescore.sourceRescoreDigest,
            scorerId: input.rescore.scorer.id,
            scorerVersion: input.rescore.scorer.version,
            scorerDigest: input.rescore.scorerDigest,
          },
        }
      : {}),
    source: input.manifest.source,
    compatibility,
    ...(input.manifest.datasetFingerprint !== undefined
      ? {
          datasetFingerprint: sanitizePublicValue(
            input.manifest.datasetFingerprint,
          ),
        }
      : {}),
    resolvedVariants: input.manifest.resolvedVariants,
    effectiveConfig: input.manifest.effectiveConfig,
    ...(input.manifest.plannedSlotKeys
      ? { plannedSlotKeys: input.manifest.plannedSlotKeys }
      : {}),
    expectedSlotKeys: [...input.manifest.expectedSlotKeys].sort(),
    files: {
      "results.jsonl": textDigest(lines),
      "summary.json": textDigest(summaryText),
      "summary.md": textDigest(markdownText),
      ...(technicalText ? { "technical.md": textDigest(technicalText) } : {}),
    },
  });
  await writeFile(
    join(resultDir, "manifest.json"),
    `${JSON.stringify(artifactManifest, null, 2)}\n`,
    "utf8",
  );
  return resultDir;
}

export async function verifyResult(resultDirInput: string): Promise<{
  ok: true;
  runId: string;
  episodes: number;
  digest: string;
  gatesPassed: boolean;
}> {
  const resultDir = resolve(resultDirInput);
  const manifestText = await readFile(join(resultDir, "manifest.json"), "utf8");
  if (Buffer.byteLength(manifestText) > MAX_CHECKED_ARTIFACT_BYTES) {
    throw new Error("manifest.json exceeds the checked-in artifact size limit");
  }
  if (SECRET_SHAPE.test(manifestText)) {
    throw new Error("manifest.json contains a secret-shaped value");
  }
  const rawManifest: unknown = JSON.parse(manifestText);
  assertNoLiteralCredentials(rawManifest, "manifest.json");
  const manifest = ResultManifestSchema.parse(rawManifest);
  if (manifest.thresholdPolicy) {
    const digest = contentDigest(manifest.thresholdPolicy);
    if (
      manifest.thresholdPolicyDigest !== digest ||
      manifest.compatibility.thresholdPolicyDigest !== digest
    ) {
      throw new Error("threshold policy digest does not match policy document");
    }
  }
  const supported = new Set([
    "results.jsonl",
    "summary.json",
    "summary.md",
    "technical.md",
  ]);
  const fileNames = Object.keys(manifest.files);
  const unsupported = fileNames.filter((name) => !supported.has(name));
  if (unsupported.length) {
    throw new Error(`unsupported result artifacts: ${unsupported.join(", ")}`);
  }
  const required = ["results.jsonl", "summary.json", "summary.md"];
  if (manifest.thresholdPolicy) required.push("technical.md");
  const missing = required.filter((name) => !fileNames.includes(name));
  if (missing.length) {
    throw new Error(`missing result artifacts: ${missing.join(", ")}`);
  }
  const allowed = new Set(["manifest.json", ...fileNames]);
  const extras = (await readdir(resultDir)).filter((name) => !allowed.has(name));
  if (extras.length) throw new Error(`unexpected result artifacts: ${extras.join(", ")}`);
  const files = manifest.files;
  for (const name of fileNames) {
    const text = await readFile(join(resultDir, name), "utf8");
    if (Buffer.byteLength(text) > MAX_CHECKED_ARTIFACT_BYTES) {
      throw new Error(`${name} exceeds the checked-in artifact size limit`);
    }
    if (SECRET_SHAPE.test(text)) throw new Error(`${name} contains a secret-shaped value`);
    if (files[name] !== textDigest(text)) throw new Error(`${name} digest mismatch`);
  }
  const lines = (await readFile(join(resultDir, "results.jsonl"), "utf8"))
    .split("\n")
    .filter(Boolean)
    .map((line) => ResultLineSchema.parse(JSON.parse(line)));
  const scored = lines.filter((line) => line.score !== undefined);
  if (
    scored.some(
      (line) =>
        line.score!.scorerDigest !== manifest.compatibility.scorerDigest,
    )
  ) {
    throw new Error(
      "result scorerDigest does not match compatibility metadata",
    );
  }
  if (
    manifest.rescore &&
    (manifest.rescore.scorerDigest !== manifest.compatibility.scorerDigest ||
      scored.some(
        (line) =>
          line.score!.scorerId !== manifest.rescore!.scorerId ||
          line.score!.scorerVersion !== manifest.rescore!.scorerVersion ||
          line.score!.scorerDigest !== manifest.rescore!.scorerDigest,
      ))
  ) {
    throw new Error("rescored result provenance does not match episode scores");
  }
  const slots = lines.map((line) => line.slotKey);
  if (new Set(slots).size !== slots.length) throw new Error("duplicate result slots");
  const expected = [...manifest.expectedSlotKeys].sort();
  const actual = [...slots].sort();
  if (JSON.stringify(expected) !== JSON.stringify(actual)) {
    throw new Error(`result coverage mismatch: expected ${expected.length}, got ${actual.length}`);
  }
  const episodes = lines.map((line) =>
    EpisodeSchema.parse({
      schemaVersion: "openneko.eval.episode/v1",
      runId: line.runId,
      slotKey: line.slotKey,
      ...(line.pairKey ? { pairKey: line.pairKey } : {}),
      ...(line.treatment ? { treatment: line.treatment } : {}),
      caseId: line.caseId,
      caseContentId: line.caseContentId,
      family: line.family,
      productPath: line.productPath,
      difficulty: line.difficulty,
      capabilityTags: line.capabilityTags,
      semantics: line.semantics,
      ...(line.assertionTargets
        ? { assertionTargets: line.assertionTargets }
        : {}),
      variantId: line.variantId,
      datasetId: line.datasetId,
      repetition: line.repetition,
      phase: line.phase,
      attempt: line.attempt,
      startedAt: line.startedAt,
      finishedAt: line.finishedAt,
      status: line.status,
      measurements: line.measurements ?? {},
      observations: [],
      ...(line.score ? { score: line.score } : {}),
      ...(line.errorType ? { errorType: line.errorType } : {}),
      ...(line.error ? { error: line.error } : {}),
      integrityDigest: line.episodeDigest,
    }),
  );
  const recomputed = summaryWithQualification(
    summarizeEpisodes(episodes),
    manifest,
  );
  const storedSummaryRaw: unknown = JSON.parse(
    await readFile(join(resultDir, "summary.json"), "utf8"),
  );
  SummarySchema.parse(storedSummaryRaw);
  const comparableSummary = manifest.thresholdPolicy
    ? recomputed
    : projectToStoredShape(recomputed, storedSummaryRaw);
  if (contentDigest(comparableSummary) !== contentDigest(storedSummaryRaw)) {
    throw new Error("summary does not match deterministic aggregate of results.jsonl");
  }
  const expectedFriendly = manifest.thresholdPolicy
    ? friendlyMarkdown(recomputed, manifest)
    : !Object.hasOwn(
          (storedSummaryRaw as Record<string, unknown>).measurements as object,
          "toolCalls",
        )
      ? initialLegacyMarkdown(recomputed, manifest)
      : legacyMarkdown(recomputed, manifest);
  if (
    expectedFriendly !==
    (await readFile(join(resultDir, "summary.md"), "utf8"))
  ) {
    throw new Error("summary.md does not match deterministic summary rendering");
  }
  if (manifest.thresholdPolicy) {
    const expectedTechnical = technicalMarkdown(
      recomputed,
      manifest,
    );
    if (
      expectedTechnical !==
      (await readFile(join(resultDir, "technical.md"), "utf8"))
    ) {
      throw new Error(
        "technical.md does not match deterministic summary rendering",
      );
    }
  }
  const gatesPassed = evaluateSuiteGates(
    recomputed,
    manifest.suiteGates,
    manifest.thresholdPolicy,
  );
  if (manifest.accepted !== undefined && manifest.accepted !== gatesPassed) {
    throw new Error("result acceptance does not match deterministic suite gates");
  }
  return {
    ok: true,
    runId: manifest.runId,
    episodes: lines.length,
    digest: textDigest(manifestText),
    gatesPassed,
  };
}

export async function readStateEpisodes(
  stateRoot: string,
  manifest: EvalManifest,
): Promise<EvalEpisode[]> {
  const episodesDir = join(resolve(stateRoot), "runs", manifest.runId, "episodes");
  const entries = await readdir(episodesDir);
  const episodes: EvalEpisode[] = [];
  for (const entry of entries.filter((name) => name.endsWith(".json")).sort()) {
    episodes.push(EpisodeSchema.parse(JSON.parse(await readFile(join(episodesDir, entry), "utf8"))));
  }
  return episodes;
}

export async function renderStoredReport(resultDir: string): Promise<string> {
  return readFile(join(resolve(resultDir), "summary.md"), "utf8");
}

export async function readRunManifest(
  stateRoot: string,
  runId: string,
): Promise<EvalManifest> {
  return ManifestSchema.parse(
    JSON.parse(
      await readFile(join(resolve(stateRoot), "runs", runId, "manifest.json"), "utf8"),
    ),
  );
}
