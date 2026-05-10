import { data_source, db, eq } from "@neko/db";
import { shellToolName } from "./agent-backend";
import { resolveAgentBackend } from "./agent-backend-resolver";
import { parseJsonFromOutput } from "./agent-backends/hermes";
import { detectUpstreamError } from "./agent-error";
import {
  discoveryUrlFromMcpUrl,
  knowledgePackPaths,
  prefetchKnowledgePack,
  type KnowledgePackPaths,
} from "./knowledge-pack";
import { ensureOrgWorkspace } from "./work/workspace";

export type MetricAgentInput = {
  orgId: string;
  role: "CEO" | "CFO" | "COO" | "CRO" | "CMO";
  slug: string;
  title: string;
  why: string;
  chartHint: "kpi" | "line" | "bar" | "donut" | "area";
  jobId?: string;
  debug?: boolean;
};

export const TIME_WINDOW_GRAINS = [
  "day",
  "week",
  "month",
  "quarter",
  "year",
  "all_time",
  "snapshot",
] as const;

export type TimeWindowGrain = (typeof TIME_WINDOW_GRAINS)[number];

export type TimeWindow = {
  grain: TimeWindowGrain;
  start: string | null;
  end: string | null;
  label: string;
};

export type MetricAgentResult = {
  reasoning: string;
  headlineMetric: string;
  headlineLabel: string;
  insightText: string;
  detailText: string;
  mood: "good" | "watch" | "bad";
  chartType: "kpi" | "line" | "bar" | "donut" | "area";
  chartData: Array<{ d: string; v: number; t?: number }>;
  timeWindow: TimeWindow;
};

function buildPrompt(
  input: MetricAgentInput,
  knowledge: KnowledgePackPaths,
  shellTool: string,
): string {
  return `You answer ONE dashboard card by writing GraphQL queries and executing them against a GraphJin server, then returning a single JSON object describing the snapshot.

EXECUTION PATTERN:
1. Read the GraphJin knowledge pack on disk before writing any query — its INDEX.md tells you which file covers what. Don't run any schema-discovery commands; that information is already on disk.

   Knowledge files (read with your \`${shellTool}\` tool, e.g. \`cat\`):
   - ${knowledge.files.index} (start here — index of the rest)
   - ${knowledge.files.tables} (every table, schema, column count)
   - ${knowledge.files.namespaces} (multi-DB namespace routing)
   - ${knowledge.files.insights} (hub tables, hot relationships, query templates, data-quality flags)
   - ${knowledge.files.syntax} (DSL operators, aggregations, pagination, expression aggregates)

2. Write ONE bulk GraphQL query that computes both the current value AND a baseline (prior period of equal length) in the same request when possible. Prefer one bulk query over many small ones.
3. Run it via the \`${shellTool}\` tool:
     graphjin cli execute_graphql --args '{"query":"<your graphql>"}'
   (\`graphjin cli\` is already pointed at the running server. Use --args-file - and stdin if the query is large enough to be awkward to escape inline.)
4. If the response contains an "errors" array, use:
     graphjin cli fix_query_error --args '{"query":"<failing query>","error":"<error message>"}'
   to get a corrected query, then run execute_graphql again.
5. When you have the data, emit the final JSON object exactly per the OUTPUT CONTRACT. No prose around it.

DATA ACCESS — READ-ONLY:
The database is queried exclusively via \`graphjin cli\` run through the \`${shellTool}\` tool. GraphJin speaks GraphQL (not raw SQL). Mutations and subscriptions are forbidden and will be denied at the tool gate. DO NOT use \`execute_code\`, Python, raw HTTP requests, or any other tool to talk to GraphJin — only \`${shellTool}\` running \`graphjin cli\`.

- Every database read goes through \`graphjin cli execute_graphql\` via \`${shellTool}\`.
- DO NOT call \`graphjin cli list_tables\` / \`describe_table\` / \`get_query_syntax\` / \`find_path\` / \`explore_relationships\` / \`get_schema_insights\` / \`get_discovery_schema\` — every one of those returns information already on disk in the knowledge files listed in step 1 (\`insights.json\` covers relationship paths and hub tables; \`tables.json\` covers schemas and column counts; \`syntax.json\` covers query syntax).
- Other useful subcommands: \`graphjin cli explain --args '{"query":"..."}'\` (compile-only, no execution); \`graphjin cli health\` (sanity check).
- Never invent data — every number in the output must trace back to a \`graphjin cli execute_graphql\` response from this run.

QUERY CONSTRUCTION — let the database aggregate:
Prefer one bulk query with server-side aggregation (count, sum, avg) over multiple round-trips that pull rows back to the agent. Specific GraphJin capabilities to reach for (consult \`syntax.json\` for full DSL reference):

- Expression aggregates — sum(expr: {...}), ratio(expr: {...}) — USE THESE FIRST when the metric involves arithmetic across columns (e.g. SUM(price × qty), margin %). They express what single-column sum_/count_/avg_ cannot.
- Joined-column access via dot-notation: { col: "product.standardcost" } works across FKs up to 3 hops — unlocks revenue × cost calculations in a single server-side aggregate.
- order_by on an expression alias: server-side top-N by computed metric, no over-fetch.
- Global single-row aggregate: a top-level select whose fields are ALL aggregates collapses to one row, no distinct needed.
- Only fall back to multiple paginated queries + in-agent math if expr: genuinely cannot express the metric.

HARD CONSTRAINTS (violating any of these is a critical failure):
- Never hardcode calendar years (e.g. "2025-07-20", "2014-01-01"); compute periods with relative arithmetic. If you need an anchor date, use a recent orderdate from the data.
- Never hardcode baseline values or magic numbers. Always compute baselines from the data (prior period of equal length, YoY, rolling average, etc).
- Never use a bare limit without pagination. Use cursor-based pagination to process all rows, or use GraphQL aggregation with distinct to let the database aggregate.
- Never compute a sum-of-products (revenue = price × qty per row) by multiplying avg_<price> × sum_<quantity> — mathematically wrong. Use sum(expr: { mul: [...] }) instead.
- Watch the silent 20-row default limit on every query level (top AND nested) — set explicit limit or use distinct+aggregation.
- Never invent or interpolate. If a query returned no rows, the answer is "no data", not a guess.
- If your queries fail or return data you cannot reason from, DO NOT narrate the failure as a metric. The worker treats this as a successful run and the dashboard renders your error string as if it were data. Instead: exit with the raw error text on stdout (any non-JSON output triggers a job failure → automatic retry). Specifically: do NOT emit \`headlineMetric\` values like "Error" / "errors" / "N/A" / "Unavailable" / "Data Unavailable" / "No data" / "—" / "TBD" / pure punctuation — those are rejected by the validator anyway.

TIME WINDOW (always declared in output):
Every card's headline is computed against a specific time window. The window
goes in the \`timeWindow\` output field so the operator (and downstream code)
knows what slice of data produced the headline.

The \`grain\` field is one of: day | week | month | quarter | year | all_time | snapshot.

Pick the window like this:
1. If the card's title or rationale names an explicit window — "this quarter",
   "last 30 days", "MTD", "YTD", "since launch", "all time", etc. — use that.
2. Otherwise default to a year-grain TTM (trailing twelve months ending at
   the most recent data date). TTM is what most CXO dashboards expect and
   gives stable period-over-period comparisons.
3. For metrics that are inherently snapshot-style — current employee count,
   open opportunities, accounts at risk, inventory on hand — use grain="snapshot"
   with start = end = today.

Field rules:
- grain  : enum value above. Match the natural duration of the window.
           "TTM" → year. "Last 30 days" → month. "Q3" → quarter. "Today" → day.
- start  : ISO yyyy-mm-dd. Required for all grains except "all_time".
- end    : ISO yyyy-mm-dd. Required for all grains except "all_time".
- label  : 1–4 words a CXO would say — "TTM", "YTD", "Q3 2025", "Last 30 days",
           "All time", "Snapshot".

Quarter convention: calendar quarters start Jan 1 / Apr 1 / Jul 1 / Oct 1.
Q1 2025 = 2025-01-01 to 2025-03-31. Q2 = 04-01 to 06-30, Q3 = 07-01 to 09-30,
Q4 = 10-01 to 12-31. "Previous quarter" means the most recently-completed
calendar quarter — if today is mid-Q2 2025, previous quarter = Q1 2025
(2025-01-01 to 2025-03-31). "Current quarter" / QTD = the partial quarter
in progress.

Consistency: the timeWindow MUST match whatever date filter the query
actually used. If you ran "where orderdate >= '2024-06-29' and orderdate <= '2025-06-29'",
emit grain="year", start="2024-06-29", end="2025-06-29", label="TTM".

Mood is mandatory and must be derived from the data, never guessed:
- Always fetch a baseline (prior period of equal length, or rolling average) in the same workflow as the current value.
- "good" only if current is materially better than baseline.
- "bad" if current is materially worse than baseline (a >15% drop on a metric where higher is better, or a >15% rise on a metric where lower is better).
- "watch" if within ±15%.
- A "good" mood with a >15% downward delta is a contradiction. Re-check before responding.

chartType MUST match the shape of chartData. Mismatches will not render:
- kpi: exactly 1 item with both v (current) and t (baseline). Use only when there is one headline number with a comparison.
- donut: 2-6 items, each item is a category share. Use for "X by category" mixes.
- bar: 2-20 items, each item is a category or period. Use for category breakdowns or short time series with comparisons.
- line / area: 4+ items, time series. Use only for trends over time.
- If you have multiple categories (e.g. 3 countries, 5 work centers), the chartType is donut or bar — never kpi. kpi is for a single number only.

================================================================================
OUTPUT CONTRACT — respond with ONE JSON object, exactly this shape, no prose:
================================================================================

{
  "reasoning": "string — which tables, aggregation, timeframe you used",
  "headlineMetric": "string — the single most important number with units, e.g. \\"$7.98M\\"",
  "headlineLabel": "string — short label for the headline metric, e.g. \\"Revenue MTD\\"",
  "insightText": "string — one-sentence plain-English observation a CXO can act on",
  "detailText": "string — 1-2 sentence drill-down explaining the driver",
  "mood": "good | watch | bad",
  "chartType": "kpi | line | bar | donut | area",
  "chartData": [{ "d": "string label", "v": 0, "t": 0 }],
  "timeWindow": {
    "grain": "day | week | month | quarter | year | all_time | snapshot",
    "start": "yyyy-mm-dd or null (only when grain='all_time')",
    "end":   "yyyy-mm-dd or null (only when grain='all_time')",
    "label": "1–4 word display label, e.g. \\"TTM\\", \\"YTD\\", \\"Q3 2025\\", \\"Snapshot\\""
  }
}

Rules for the JSON:
- Output a SINGLE JSON object. No markdown fences, no prose before or after.
- mood and chartType must be one of the listed enum values, lowercase.
- chartData must be a non-empty array. For kpi, exactly 1 item with both v and t. For donut, 2-6 items. For bar, 2-20 items. For line/area, 4+ items.
- All numbers in chartData.v and chartData.t must be plain numbers (not strings, no units).
- timeWindow.grain must be one of the listed enum values, lowercase.
- timeWindow.start and timeWindow.end must be ISO yyyy-mm-dd strings (or null only when grain='all_time'). They must match the date filter the query actually used.

================================================================================
INPUT — the card you must answer:
================================================================================

${JSON.stringify(
  {
    cardTitle: input.title,
    cardRationale: input.why,
    cardRole: input.role,
    cardSlug: input.slug,
    chartHint: input.chartHint,
  },
  null,
  2,
)}
`;
}

export async function runMetricAgent(
  input: MetricAgentInput,
): Promise<MetricAgentResult> {
  const sources = await db()
    .select({ mcp_url: data_source.mcp_url })
    .from(data_source)
    .where(eq(data_source.org_id, input.orgId))
    .limit(1);
  const mcpUrl = sources[0]?.mcp_url;
  if (!mcpUrl) {
    throw new Error(
      `no mcp_url for org ${input.orgId} — set data_source.mcp_url`,
    );
  }

  console.log(
    `[metric-agent] org=${input.orgId} role=${input.role} slug=${input.slug} mcp=${mcpUrl}`,
  );

  const workspace = await ensureOrgWorkspace(input.orgId);
  const refreshResult = await prefetchKnowledgePack({
    discoveryUrl: discoveryUrlFromMcpUrl(mcpUrl),
    destDir: workspace.knowledgeRoot,
  });
  if (refreshResult.ok) {
    const totalBytes = refreshResult.files.reduce((n, f) => n + f.bytes, 0);
    console.log(
      `[metric-agent] org=${input.orgId} slug=${input.slug} knowledge refreshed (${refreshResult.files.length} files, ${totalBytes}B)`,
    );
  } else {
    console.warn(
      `[metric-agent] org=${input.orgId} slug=${input.slug} knowledge refresh failed (${refreshResult.error}); proceeding with on-disk pack`,
    );
  }
  const knowledge = knowledgePackPaths(workspace.knowledgeRoot);

  const backend = await resolveAgentBackend(input.orgId);
  const debug = input.debug === true;

  const prompt = buildPrompt(input, knowledge, shellToolName(backend.id));

  console.log(
    `[metric-agent] org=${input.orgId} slug=${input.slug} backend=${backend.id}`,
  );

  const startedAt = Date.now();
  const result_ = await backend.run({
    prompt,
    orgId: input.orgId,
    tag: input.jobId,
    debug,
  });
  if (result_.status !== "completed") {
    const message = result_.error ?? `${backend.id} returned status=${result_.status}`;
    console.error(
      `[metric-agent] org=${input.orgId} slug=${input.slug} backend=${backend.id} run failed after ${(
        (Date.now() - startedAt) / 1000
      ).toFixed(0)}s: ${message}`,
    );
    throw new Error(message);
  }
  const stdout = result_.finalText;
  const elapsedSec = ((Date.now() - startedAt) / 1000).toFixed(0);

  const upstream = detectUpstreamError(stdout);
  if (upstream) {
    console.warn(
      `[metric-agent] org=${input.orgId} slug=${input.slug} upstream provider error: ${upstream.message}`,
    );
    throw upstream;
  }

  let parsed: Partial<MetricAgentResult>;
  try {
    parsed = parseJsonFromOutput(stdout) as Partial<MetricAgentResult>;
  } catch (e) {
    console.error(
      `[metric-agent] org=${input.orgId} slug=${input.slug} parse failed; full stdout follows (${stdout.length}B):`,
    );
    console.error(stdout);
    throw e;
  }

  const tw = (parsed.timeWindow ?? {}) as Partial<TimeWindow>;
  const result: MetricAgentResult = {
    reasoning: String(parsed.reasoning ?? ""),
    headlineMetric: String(parsed.headlineMetric ?? ""),
    headlineLabel: String(parsed.headlineLabel ?? ""),
    insightText: String(parsed.insightText ?? ""),
    detailText: String(parsed.detailText ?? ""),
    mood: parsed.mood as MetricAgentResult["mood"],
    chartType: parsed.chartType as MetricAgentResult["chartType"],
    chartData: Array.isArray(parsed.chartData)
      ? (parsed.chartData as MetricAgentResult["chartData"])
      : [],
    timeWindow: {
      grain: tw.grain as TimeWindowGrain,
      start: tw.start === null ? null : tw.start ? String(tw.start) : null,
      end: tw.end === null ? null : tw.end ? String(tw.end) : null,
      label: String(tw.label ?? ""),
    },
  };

  console.log(
    `[metric-agent] org=${input.orgId} slug=${input.slug} done in ${elapsedSec}s`,
  );

  return result;
}
