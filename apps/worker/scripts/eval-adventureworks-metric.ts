import { readFile } from "node:fs/promises";
import {
  EvalEnvironmentError,
  EvalTaskError,
  createScore,
  durationMs,
  estimateUsageCost,
  resolveCredentialRef,
  textDigest,
  type EvalDriver,
  type EvalExecution,
  type LoadedCase,
  type LoadedEval,
  type EvalPlan,
  type PricingCatalog,
  type EvalVariant,
} from "@neko/evals";
import { createTestOrg, seedDataSource, seedProvider, uniqueOrgId } from "@neko/db/test-helpers";
import { db, pool as metadataPool, processing_job } from "@neko/db";
import { enqueueMetricRefresh } from "@neko/db/jobs";
import {
  classifyQuestion,
  type AgentTokenUsage,
  type QuestionClassificationDiagnostics,
} from "@neko/llm";
import { maybeEncryptSecret } from "@neko/llm/secrets";
import pg from "pg";
import { EvalInstallHost, type EvalEpisode, type EvalInstallHostOptions } from "./eval-install";
import { RunEventFeed, type ScriptedModelProgram, type ScriptedModelServer } from "./eval-scripted-model";

type AdventureWorksOracle = {
  anchorDate: string;
  startDate: string;
  expectedValue: number;
  baselineValue: number;
  expectedDimension?: string;
};

export type MetricResult = {
  reasoning: string;
  headlineMetric: string;
  headlineLabel: string;
  insightText: string;
  detailText: string;
  mood: string;
  chartType: string;
  chartData: Array<{ d: string; v: number; t?: number }>;
  baselineValue?: number | null;
  timeWindow: { grain: string; start: string | null; end: string | null; label: string };
};

export type MetricLedgerRow = { tokens: number | null; costMicros: number; priced: string };

export type MetricJobOutcome = {
  status: string;
  error: string | null;
  result?: MetricResult;
  ledger: MetricLedgerRow[];
  durationMs: number;
};

export type MetricCardInput = {
  question: string;
  role: string;
  slug: string;
  title: string;
  why: string;
  chartHint: string;
};

export type MetricClassifier = (
  question: string,
  role: string,
  orgId: string,
  options: Parameters<typeof classifyQuestion>[3],
) => ReturnType<typeof classifyQuestion>;

export type AdventureWorksMetricDriverOptions = {
  host?: EvalInstallHostOptions;
  model?: ScriptedModelServer;
  program?: (input: MetricCardInput) => ScriptedModelProgram;
  classify?: MetricClassifier;
};

const EXEC_ROLES = ["CEO", "CFO", "COO", "CRO", "CMO", "CIO", "CPO"];
const CHART_HINTS = ["kpi", "line", "bar", "donut", "area"];
const FINAL_RIVER_STATES = new Set(["completed", "discarded", "cancelled"]);

// The Go job checks its own result; the scorer checks it again so that a
// stored output scores the same way.
const VALID_MOODS = new Set(["good", "watch", "bad"]);
const VALID_CHART_TYPES = new Set(CHART_HINTS);
const FAIL_SENTINELS = new Set([
  "error",
  "errors",
  "n/a",
  "na",
  "unavailable",
  "data unavailable",
  "data unavilable",
  "no data",
  "null",
  "undefined",
  "—",
  "-",
  "?",
  "tbd",
]);
const ALL_PUNCT_RE = /^[\s\-—?_.]+$/;
const VALID_TIME_GRAINS = new Set(["day", "week", "month", "quarter", "year", "all_time", "snapshot"]);
const ISO_DATE_RE = /^\d{4}-\d{2}-\d{2}$/;

export function validateMetricResult(r: MetricResult): string | null {
  if (!r.headlineMetric || !r.headlineMetric.trim()) return "empty headlineMetric";
  const headlineNorm = r.headlineMetric.trim().toLowerCase();
  if (FAIL_SENTINELS.has(headlineNorm) || ALL_PUNCT_RE.test(headlineNorm)) {
    return `agent emitted error sentinel as headlineMetric: "${r.headlineMetric}"`;
  }
  if (!r.headlineLabel) return "empty headlineLabel";
  if (!r.insightText) return "empty insightText";
  if (!VALID_MOODS.has(r.mood)) return `invalid mood: ${r.mood}`;
  if (!VALID_CHART_TYPES.has(r.chartType)) return `invalid chartType: ${r.chartType}`;
  if (!Array.isArray(r.chartData) || r.chartData.length === 0) return "empty chartData";
  for (const item of r.chartData) {
    if (typeof item.v !== "number" || isNaN(item.v)) return "chartData item has non-numeric v";
    if (!item.d) return "chartData item missing d label";
  }
  if (r.chartType === "kpi" && r.chartData.length !== 1) {
    return `kpi chartType requires exactly 1 chartData item, got ${r.chartData.length}`;
  }
  if (r.chartType === "kpi" && r.chartData[0]!.t == null) {
    return "kpi chartType requires a baseline (t) value";
  }
  if (!r.timeWindow) return "missing timeWindow";
  if (!VALID_TIME_GRAINS.has(r.timeWindow.grain)) {
    return `invalid timeWindow.grain: ${r.timeWindow.grain}`;
  }
  if (!r.timeWindow.label) return "empty timeWindow.label";
  if (r.timeWindow.grain !== "all_time") {
    if (!r.timeWindow.start || !ISO_DATE_RE.test(r.timeWindow.start)) {
      return `timeWindow.start must be yyyy-mm-dd (got "${r.timeWindow.start}")`;
    }
    if (!r.timeWindow.end || !ISO_DATE_RE.test(r.timeWindow.end)) {
      return `timeWindow.end must be yyyy-mm-dd (got "${r.timeWindow.end}")`;
    }
  }
  return null;
}

// spend_ledger keeps total tokens and a priced cost per row, with no input
// and output split, so the job's usage is complete when every row has
// tokens and a priced cost.
export function metricUsageMeasurements(input: {
  variant: EvalVariant;
  catalog: PricingCatalog | undefined;
  classifier: AgentTokenUsage;
  ledger: MetricLedgerRow[];
}): Record<string, unknown> {
  const { classifier, ledger } = input;
  const reasons: string[] = [];
  const classifierEstimate = estimateUsageCost({
    usage: classifier,
    provider: input.variant.outer_model.provider,
    model: input.variant.outer_model.model,
    catalog: input.catalog,
  });
  const classifierCost = classifier.billedCostUsd ?? classifierEstimate.estimatedCostUsd;
  if (classifier.coverage !== "complete") {
    reasons.push(...(classifier.missingReasons ?? ["classification usage unavailable"]).map((r) => `classification: ${r}`));
  }
  if (classifierCost === undefined) {
    reasons.push(...(classifierEstimate.missingReasons ?? ["cost unavailable"]).map((r) => `classification: ${r}`));
  }
  const jobTokens = ledger.filter((row) => row.tokens !== null);
  const jobUsageComplete = ledger.length > 0 && jobTokens.length === ledger.length;
  const jobPriced = ledger.length > 0 && ledger.every((row) => row.priced !== "fallback");
  if (ledger.length === 0) reasons.push("metric_refresh: no spend_ledger row");
  else {
    if (!jobUsageComplete) reasons.push("metric_refresh: a spend_ledger row has no tokens");
    if (!jobPriced) reasons.push("metric_refresh: a spend_ledger row has the fallback price");
  }
  const tokenParts = [
    classifier.totalTokens,
    jobTokens.length > 0 ? jobTokens.reduce((sum, row) => sum + row.tokens!, 0) : undefined,
  ].filter((value): value is number => value !== undefined);
  const costParts = [
    classifierCost,
    jobPriced ? ledger.reduce((sum, row) => sum + row.costMicros, 0) / 1_000_000 : undefined,
  ].filter((value): value is number => value !== undefined);
  const coverage = (complete: boolean, parts: number) =>
    complete ? "complete" : parts > 0 ? "partial" : "unavailable";
  return {
    usageCoverage: coverage(classifier.coverage === "complete" && jobUsageComplete, tokenParts.length),
    ...(tokenParts.length > 0 ? { totalTokens: tokenParts.reduce((sum, value) => sum + value, 0) } : {}),
    costCoverage: coverage(classifierCost !== undefined && jobPriced, costParts.length),
    ...(costParts.length > 0 ? { estimatedCostUsd: costParts.reduce((sum, value) => sum + value, 0) } : {}),
    ...(costParts.length > 0 && input.catalog
      ? { currency: input.catalog.currency, pricingCatalogVersion: input.catalog.version }
      : {}),
    ...(reasons.length > 0 ? { usageMissingReasons: reasons } : {}),
  };
}

const HEADLINE_PATTERN = /^([+-]?\d+(?:\.\d+)?)\s*(?:(thousand|million|billion)\b|([kmb])\b)?\s*(?:[%x]|[a-z][a-z\s]*)?$/iu;

const INFRASTRUCTURE_ERROR = /openneko serve|sandbox|gateway|ECONNREFUSED|socket hang up/i;

function parseHeadline(value: string): number | null {
  const match = value.replace(/[$,]/gu, "").trim().match(HEADLINE_PATTERN);
  if (!match) return null;
  let number = Number.parseFloat(match[1] ?? "");
  if (!Number.isFinite(number)) return null;
  const scale = (match[2] ?? match[3] ?? "").toLowerCase().charAt(0);
  if (scale === "t" || scale === "k") number *= 1_000;
  if (scale === "m") number *= 1_000_000;
  if (scale === "b") number *= 1_000_000_000;
  return number;
}

function datasetSetting(
  loaded: LoadedEval,
  datasetId: string,
  field: "agent_graphql_ref" | "agent_mcp_ref" | "oracle_database_ref",
  defaultKey: string,
): string {
  const dataset = loaded.datasets.get(datasetId);
  if (!dataset) throw new Error(`dataset ${datasetId} is not loaded`);
  const ref = dataset.connection[field];
  const fromEnv = ref ? process.env[ref.slice("env:".length)]?.trim() : undefined;
  const fallback = dataset.connection.defaults?.[defaultKey];
  const value = fromEnv || fallback;
  if (!value) throw new EvalEnvironmentError(`${datasetId} has no ${field}`, "dataset_connection_missing");
  return value;
}

function outputOf(execution: EvalExecution): MetricResult {
  if (!execution.output || typeof execution.output !== "object") {
    throw new Error("metric execution did not retain a structured output");
  }
  return execution.output as MetricResult;
}

/**
 * Runs the Go metric_refresh job (question path) once, in the episode that
 * is active, and reads its processing_job row, its snapshot and its
 * spend_ledger rows.
 */
export async function runMetricRefreshJob(
  orgId: string,
  card: MetricCardInput,
  timeoutMs: number,
  signal?: AbortSignal,
): Promise<MetricJobOutcome> {
  const started = Date.now();
  const [job] = await db()
    .insert(processing_job)
    .values({ org_id: orgId, kind: "metric_refresh", status: "queued", trigger: "eval", trigger_payload: card })
    .returning({ id: processing_job.id });
  if (!job) throw new Error("the metric_refresh processing_job row was not written");
  const riverId = await enqueueMetricRefresh({ processingJobId: job.id, orgId }, 1);
  const deadline = started + timeoutMs;
  for (;;) {
    signal?.throwIfAborted();
    const { rows } = await metadataPool().query<{ state: string }>(
      `select state::text as state from river.river_job where id = $1`,
      [riverId],
    );
    if (FINAL_RIVER_STATES.has(rows[0]?.state ?? "")) break;
    if (Date.now() >= deadline) {
      throw new EvalTaskError(`the metric_refresh job did not end within ${timeoutMs / 1000} s`, "timeout", {
        measurements: { wallDurationMs: Date.now() - started },
      });
    }
    await new Promise((wake) => setTimeout(wake, 250));
  }
  const durationMs = Date.now() - started;
  const { rows: jobs } = await metadataPool().query<{ status: string; error: string | null }>(
    `select status, error from processing_job where id = $1`,
    [job.id],
  );
  const { rows: snapshots } = await metadataPool().query<{ payload: MetricResult }>(
    `select s.payload from metric_snapshot s join metric m on m.id = s.metric_id
      where m.org_id = $1 and m.last_refresh_job_id = $2 order by s.captured_at desc limit 1`,
    [orgId, job.id],
  );
  const { rows: ledger } = await metadataPool().query<{ tokens: string | null; cost_micros: string; priced: string }>(
    `select l.tokens::text as tokens, l.cost_micros::text as cost_micros, l.priced
       from spend_ledger l join spend_reservation r on r.id = l.reservation_id
      where l.org_id = $1 and r.source = 'metric' order by l.created_at`,
    [orgId],
  );
  return {
    status: jobs[0]?.status ?? "missing",
    error: jobs[0]?.error ?? null,
    ...(snapshots[0] ? { result: snapshots[0].payload } : {}),
    ledger: ledger.map((row) => ({
      tokens: row.tokens === null ? null : Number(row.tokens),
      costMicros: Number(row.cost_micros),
      priced: row.priced,
    })),
    durationMs,
  };
}

export function createAdventureWorksMetricDriver(
  context: { loaded: LoadedEval; plan: EvalPlan },
  options: AdventureWorksMetricDriverOptions = {},
): EvalDriver {
  const classify: MetricClassifier = options.classify ?? classifyQuestion;
  let host: EvalInstallHost | undefined;
  let pool: pg.Pool | undefined;
  const datasetId = context.loaded.cases[0]?.dataset ?? "adventureworks";
  const oracleUrl = datasetSetting(context.loaded, datasetId, "oracle_database_ref", "oracle_database_url");

  function oraclePool(): pg.Pool {
    pool ??= new pg.Pool({ connectionString: oracleUrl, max: 1 });
    return pool;
  }

  async function seedOrg(variant: EvalVariant, episode: EvalEpisode): Promise<string> {
    const orgId = uniqueOrgId(`eval-${variant.id}`);
    await createTestOrg(orgId, `Eval ${variant.id}`);
    await seedDataSource(orgId, {
      graphqlUrl: datasetSetting(context.loaded, datasetId, "agent_graphql_ref", "agent_graphql_url"),
      mcpUrl: datasetSetting(context.loaded, datasetId, "agent_mcp_ref", "agent_mcp_url"),
      label: "AdventureWorks eval",
    });
    if (variant.backend.startsWith("scripted-")) {
      if (!options.model) {
        throw new EvalEnvironmentError("the scripted model is not running", "backend_runtime_missing");
      }
      await seedProvider(orgId, {
        scope: "primary",
        provider: "openai-compatible",
        model: variant.outer_model.model,
        enabled: true,
        config: { url: options.model.url },
        secrets: { apiKey: maybeEncryptSecret(episode.modelKey) },
      });
    } else {
      const apiKey = resolveCredentialRef(variant.outer_model.credential_ref);
      if (!apiKey) {
        throw new EvalEnvironmentError(`variant ${variant.id} has no credential_ref`, "provider_credential_missing");
      }
      await seedProvider(orgId, {
        scope: "primary",
        provider: variant.outer_model.provider,
        model: variant.outer_model.model,
        enabled: true,
        config: variant.outer_model.config,
        secrets: { apiKey: maybeEncryptSecret(apiKey) },
      });
    }
    await seedProvider(orgId, { scope: "agent", provider: "ax", enabled: true, config: { backend: "ax" } });
    return orgId;
  }

  return {
    id: "adventureworks.metric",
    scorer: {
      id: "adventureworks.metric",
      version: "2.0.0",
      definition: {
        classification: "natural question to card metadata via production classifier",
        numeric: "relative error against host SQL",
        baseline: "baselineValue (else the first chart target) relative error against host SQL",
        timeWindow: "exact grain/start/end",
        contract: "OpenNeko metric result contract",
      },
    },
    async preflight({ loaded, plan }) {
      if (plan.slots.some((slot) => slot.case.product_path !== "metric")) {
        throw new EvalEnvironmentError(
          "adventureworks.metric only supports the metric product path",
          "adapter_incompatible",
        );
      }
      for (const variant of loaded.config.variants) {
        if (variant.data_path !== "graphjin-direct") {
          throw new EvalEnvironmentError(
            `adventureworks.metric does not support ${variant.data_path}`,
            "data_path_unsupported",
          );
        }
        if (variant.backend.startsWith("scripted-")) continue;
        if (variant.backend !== "ax") {
          throw new EvalEnvironmentError(
            `the metric_refresh job runs on Ax; variant ${variant.id} asks for ${variant.backend}`,
            "backend_adapter_missing",
          );
        }
        resolveCredentialRef(variant.outer_model.credential_ref);
      }
      const graphqlUrl = datasetSetting(loaded, datasetId, "agent_graphql_ref", "agent_graphql_url");
      const response = await fetch(graphqlUrl, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ query: "query OpenNekoEvalHealth { __typename }" }),
        signal: AbortSignal.timeout(5_000),
      }).catch((cause) => {
        throw new EvalEnvironmentError(
          `GraphJin preflight failed: ${cause instanceof Error ? cause.message : cause}`,
          "graphjin_unreachable",
        );
      });
      if (!response.ok) {
        throw new EvalEnvironmentError(
          `GraphJin preflight returned HTTP ${response.status}`,
          "graphjin_unhealthy",
        );
      }
      const fingerprint = await oraclePool().query<{
        database_name: string;
        server_version: string;
        anchor_date: string;
        orders: string;
        details: string;
        schema_digest: string;
      }>(`
        select
          current_database() as database_name,
          current_setting('server_version') as server_version,
          (select max(orderdate)::date::text from sales.salesorderheader) as anchor_date,
          (select count(*)::text from sales.salesorderheader) as orders,
          (select count(*)::text from sales.salesorderdetail) as details,
          (select md5(string_agg(table_schema || '.' || table_name || '.' || column_name || ':' || data_type, ',' order by table_schema, table_name, ordinal_position))
             from information_schema.columns
            where table_schema in ('sales', 'production', 'purchasing', 'person')) as schema_digest
      `);
      host ??= await EvalInstallHost.prepare(options.host);
      const endpoint = new URL(graphqlUrl);
      endpoint.username = "";
      endpoint.password = "";
      endpoint.search = "";
      endpoint.hash = "";
      return {
        datasetFingerprint: {
          dataset: datasetId,
          snapshot: loaded.datasetSnapshots.get(datasetId),
          agentEndpoint: endpoint.toString(),
          ...fingerprint.rows[0],
        },
        resolvedVariants: Object.fromEntries(
          loaded.config.variants.map((variant) => [
            variant.id,
            {
              backend: variant.backend,
              provider: variant.outer_model.provider,
              model: variant.outer_model.model,
              dataPath: variant.data_path,
            },
          ]),
        ),
      };
    },
    async resolveOracle(evalCase: LoadedCase) {
      if (evalCase.oracle?.kind !== "sql.metric" || !evalCase.oraclePath) {
        throw new EvalEnvironmentError(
          `${evalCase.id} requires a sql.metric oracle ref`,
          "oracle_invalid",
        );
      }
      const sql = await readFile(evalCase.oraclePath, "utf8");
      const result = await oraclePool().query<{
        anchor_date: string;
        start_date: string;
        expected_value: string;
        baseline_value: string;
        expected_dimension: string | null;
      }>(sql);
      const row = result.rows[0];
      const oracle: AdventureWorksOracle = {
        anchorDate: row?.anchor_date ?? "",
        startDate: row?.start_date ?? "",
        expectedValue: Number(row?.expected_value),
        baselineValue: Number(row?.baseline_value),
        ...(row?.expected_dimension
          ? { expectedDimension: row.expected_dimension }
          : {}),
      };
      if (
        !oracle.anchorDate ||
        !oracle.startDate ||
        !Number.isFinite(oracle.expectedValue) ||
        !Number.isFinite(oracle.baselineValue)
      ) {
        throw new EvalEnvironmentError(
          `${evalCase.id} oracle returned no usable row`,
          "oracle_invalid",
        );
      }
      return { ...oracle, oracleSqlDigest: textDigest(sql) };
    },
    // Each episode runs on its own database, which stop() drops.
    async reset() {},
    async execute({ runId, slot, observer, signal }) {
      const input = slot.case.input as { question?: unknown; role?: unknown };
      const question = String(input.question ?? "").trim();
      const role = String(input.role ?? "").trim();
      if (!question) throw new Error(`${slot.caseId} has no natural-language question`);
      if (!EXEC_ROLES.includes(role)) throw new Error(`${slot.caseId} has an invalid executive role`);
      const scripted = slot.variant.backend.startsWith("scripted-");
      if (scripted && (!options.model || !options.program)) {
        throw new EvalEnvironmentError("the scripted model is not running", "backend_runtime_missing");
      }
      host ??= await EvalInstallHost.prepare(options.host);
      const started = Date.now();
      const operationId = `metric:${runId}-${slot.caseId}-${slot.repetition}`;
      const episode = await host.startEpisode({ label: slot.caseId, env: { OPENNEKO_METRICS_REFRESH: "on" } });
      try {
        episode.activate();
        const orgId = await seedOrg(slot.variant, episode);
        let classificationDiagnostics: QuestionClassificationDiagnostics | undefined;
        const classified = await classify(question, role, orgId, {
          observer,
          operationId: `${operationId}:classification`,
          parentOperationId: operationId,
          observationAttributes: {
            "openneko.run.kind": "eval",
            "openneko.model.scope": "classification",
            "gen_ai.provider.name": slot.variant.outer_model.provider,
            "gen_ai.request.model": slot.variant.outer_model.model,
            "openneko.eval.case.id": slot.caseId,
            "openneko.eval.variant.id": slot.variantId,
          },
          onDiagnostics(value) {
            classificationDiagnostics = value;
          },
        });
        const card: MetricCardInput = {
          question,
          role,
          slug: classified.slug || `chat-${slot.caseId}`,
          title: classified.title || question.slice(0, 80),
          why: classified.why || question,
          chartHint: CHART_HINTS.includes(classified.chartHint) ? classified.chartHint : "bar",
        };
        if (scripted) options.model!.arm(options.program!(card), new RunEventFeed());
        const timeoutMs = durationMs(slot.case.timeout ?? context.loaded.config.defaults.timeout);
        const job = await runMetricRefreshJob(orgId, card, timeoutMs, signal);
        const wallDurationMs = Date.now() - started;
        const measurements = {
          wallDurationMs,
          metricJobDurationMs: job.durationMs,
          ...(classificationDiagnostics ? { classificationDurationMs: classificationDiagnostics.durationMs } : {}),
          ...metricUsageMeasurements({
            variant: slot.variant,
            catalog: context.loaded.pricing,
            classifier: classificationDiagnostics?.usage ?? {
              coverage: "unavailable",
              missingReasons: ["classification usage unavailable"],
            },
            ledger: job.ledger,
          }),
        };
        if (job.status !== "succeeded" || !job.result) {
          const message = job.error || `the metric_refresh job ended ${job.status} with no snapshot`;
          if (INFRASTRUCTURE_ERROR.test(message)) throw new Error(message);
          throw new EvalTaskError(message, "agent_failure", { measurements });
        }
        await observer.observe({
          kind: "output.contract",
          operationId: `${operationId}:contract`,
          parentOperationId: operationId,
          status: validateMetricResult(job.result) ? "error" : "ok",
        });
        return { output: job.result, measurements };
      } finally {
        await episode.stop();
      }
    },
    score({ case: evalCase, oracle: rawOracle, execution }) {
      const oracle = rawOracle as AdventureWorksOracle;
      const output = outputOf(execution);
      const parsed =
        parseHeadline(output.headlineMetric) ?? output.chartData[0]?.v ?? null;
      const checks = evalCase.assertions.map((assertion) => {
        if (assertion.kind === "numeric.relative-error") {
          const tolerance = Number(assertion.params?.max_relative_error ?? 0.01);
          const relativeError =
            parsed == null
              ? Number.POSITIVE_INFINITY
              : oracle.expectedValue === 0
                ? Math.abs(parsed)
                : Math.abs(parsed - oracle.expectedValue) / Math.abs(oracle.expectedValue);
          return {
            assertionId: assertion.id,
            dimension: assertion.dimension,
            passed: relativeError <= tolerance,
            score: Number.isFinite(relativeError)
              ? Math.max(0, 1 - relativeError / Math.max(tolerance, 1e-9))
              : 0,
            gate: assertion.gate,
            diagnostic: `relative_error=${Number.isFinite(relativeError) ? relativeError : "unavailable"}`,
          };
        }
        if (assertion.kind === "numeric.baseline-relative-error") {
          const tolerance = Number(assertion.params?.max_relative_error ?? 0.01);
          const actual = output.baselineValue ?? output.chartData[0]?.t;
          const relativeError =
            actual == null
              ? Number.POSITIVE_INFINITY
              : oracle.baselineValue === 0
                ? Math.abs(actual)
                : Math.abs(actual - oracle.baselineValue) /
                  Math.abs(oracle.baselineValue);
          return {
            assertionId: assertion.id,
            dimension: assertion.dimension,
            passed: relativeError <= tolerance,
            score: Number.isFinite(relativeError)
              ? Math.max(0, 1 - relativeError / Math.max(tolerance, 1e-9))
              : 0,
            gate: assertion.gate,
            diagnostic: `relative_error=${Number.isFinite(relativeError) ? relativeError : "unavailable"}`,
          };
        }
        if (assertion.kind === "time-window.exact") {
          const expectedGrain = String(assertion.params?.grain ?? "");
          const passed =
            output.timeWindow.grain === expectedGrain &&
            output.timeWindow.start === oracle.startDate &&
            output.timeWindow.end === oracle.anchorDate;
          return {
            assertionId: assertion.id,
            dimension: assertion.dimension,
            passed,
            gate: assertion.gate,
            diagnostic: `expected=${expectedGrain}:${oracle.startDate}:${oracle.anchorDate}`,
          };
        }
        if (assertion.kind === "metric.contract") {
          const validationError = validateMetricResult(output);
          return {
            assertionId: assertion.id,
            dimension: assertion.dimension,
            passed: validationError === null,
            gate: assertion.gate,
            ...(validationError ? { diagnostic: validationError } : {}),
          };
        }
        if (assertion.kind === "dimension.contains") {
          const expected = oracle.expectedDimension?.toLocaleLowerCase();
          const passed = Boolean(
            expected && JSON.stringify(output).toLocaleLowerCase().includes(expected),
          );
          return {
            assertionId: assertion.id,
            dimension: assertion.dimension,
            passed,
            gate: assertion.gate,
            diagnostic: `expected_dimension=${oracle.expectedDimension ?? "missing"}`,
          };
        }
        if (assertion.kind === "duration.under") {
          const actual = Number(execution.measurements?.wallDurationMs);
          const maximum = Number(assertion.params?.max_ms ?? 600_000);
          return {
            assertionId: assertion.id,
            dimension: assertion.dimension,
            passed: Number.isFinite(actual) && actual <= maximum,
            score: Number.isFinite(actual) ? Math.max(0, Math.min(1, maximum / Math.max(actual, 1))) : 0,
            gate: assertion.gate,
            diagnostic: `duration_ms=${actual}`,
          };
        }
        throw new Error(`unknown metric assertion ${assertion.kind}`);
      });
      return createScore({
        scorerId: "adventureworks.metric",
        scorerVersion: "2.0.0",
        scorerDefinition: {
          classification: "natural question to card metadata via production classifier",
          numeric: "relative error against host SQL",
          baseline: "baselineValue (else the first chart target) relative error against host SQL",
          timeWindow: "exact grain/start/end",
          contract: "OpenNeko metric result contract",
        },
        checks,
      });
    },
    async close() {
      await pool?.end();
      await host?.close();
    },
  };
}
