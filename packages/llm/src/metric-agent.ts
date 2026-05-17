import { data_source, db, eq } from "@neko/db";
import { shellToolName } from "./agent-backend";
import { resolveAgentBackend } from "./agent-backend-resolver";
import { parseJsonFromOutput } from "./agent-backends/hermes";
import { detectUpstreamError } from "./agent-error";
import {
  discoveryUrlFromMcpUrl,
  knowledgePackPaths,
  prefetchKnowledgePack,
  readKnowledgePack,
} from "./knowledge-pack";
import { buildMetricPrompt } from "./metric-prompt";
import {
  ensureGraphjinGuard,
  resolveBinaryOnPath,
} from "./work/graphjin-guard";
import {
  formatGlobalMemoryPromptContext,
} from "./work/memory";
import { buildWorkMemoryServer } from "./work/tools";
import { ensureWorkWorkspace } from "./work/workspace";

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

  const workspace = await ensureWorkWorkspace(
    input.orgId,
    "metric-agent",
    input.jobId ?? input.slug,
  );
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
  const knowledge = await readKnowledgePack(knowledgePackPaths(workspace.knowledgeRoot));

  const backend = await resolveAgentBackend(input.orgId);
  const debug = input.debug === true;
  const graphjinBinary = await resolveBinaryOnPath("graphjin");
  if (!graphjinBinary) {
    throw new Error("graphjin CLI is not installed on PATH.");
  }
  await ensureGraphjinGuard(workspace.binRoot, graphjinBinary);

  // Preload the top-5 global memories so pinned operator rules show up
  // verbatim. Anything narrower (per-card semantic match) is reachable
  // via the search MCP tool below.
  const memoryContext = await formatGlobalMemoryPromptContext(input.orgId);

  const supportsMemorySearch = backend.capabilities.mcpTools;

  const prompt = buildMetricPrompt({
    input,
    knowledge,
    workspace,
    shellTool: shellToolName(backend.id),
    memoryContext,
    supportsMemorySearch,
  });

  console.log(
    `[metric-agent] org=${input.orgId} slug=${input.slug} backend=${backend.id}`,
  );

  // Search-only memory MCP: one-shot agent never persists memories itself
  // (the operator does that explicitly via `save:`), but it can look up
  // anything beyond the preloaded top-5.
  const mcpServers = supportsMemorySearch
    ? {
        neko_memory: buildWorkMemoryServer(
          { orgId: input.orgId },
          { exposeSave: false },
        ),
      }
    : undefined;

  const startedAt = Date.now();
  const result_ = await backend.run({
    prompt,
    orgId: input.orgId,
    tag: input.jobId,
    workspace,
    debug,
    mcpServers,
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
