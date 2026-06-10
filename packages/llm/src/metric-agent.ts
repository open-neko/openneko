import { data_source, db, eq } from "@neko/db";
import { shellToolName } from "./agent-backend";
import { resolveAgentBackend } from "./agent-backend-resolver";
import { parseJsonFromOutput } from "./agent-backends/hermes";
import { runValidatedAgentTurn } from "./agent-validate-loop";
import {
  buildDiscoveryPathwaysSection,
  getDiscoveryPathways,
} from "./discovery-pathways";
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

// Keep in sync with ROLE_FOCUS (bootstrap-metrics-writer.ts) and the
// onboarding ALL_SEATS list — every seat the product offers must be here.
export const EXEC_ROLES = [
  "CEO",
  "CFO",
  "COO",
  "CRO",
  "CMO",
  "CIO",
  "CPO",
] as const;
export type ExecRole = (typeof EXEC_ROLES)[number];

export type MetricAgentInput = {
  orgId: string;
  role: ExecRole;
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

  const basePrompt = buildMetricPrompt({
    input,
    knowledge,
    workspace,
    shellTool: shellToolName(backend.id),
    memoryContext,
    supportsMemorySearch,
  });
  // GJ3: role-shaped warm-start for discovery (cached per org/role/intent).
  const pathwaysSection = buildDiscoveryPathwaysSection(
    getDiscoveryPathways({
      orgId: input.orgId,
      role: input.role,
      intent: input.title,
      insightsJson: knowledge.insights,
    }),
  );
  const prompt = pathwaysSection ? `${basePrompt}\n\n${pathwaysSection}` : basePrompt;

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
  // GJ2: iterative validation loop — a malformed reply is fed back to the
  // agent for a corrective turn instead of failing the whole job.
  const { value: parsed } = await runValidatedAgentTurn<
    Partial<MetricAgentResult>
  >({
    backend,
    run: {
      prompt,
      orgId: input.orgId,
      tag: input.jobId,
      workspace,
      debug,
      mcpServers,
    },
    label: `metric-agent org=${input.orgId} slug=${input.slug}`,
    validate: (finalText) => {
      const out = parseJsonFromOutput(finalText) as Partial<MetricAgentResult>;
      if (!out.headlineMetric && !out.insightText) {
        throw new Error(
          "JSON parsed but headlineMetric and insightText are both empty",
        );
      }
      return out;
    },
  });
  const elapsedSec = ((Date.now() - startedAt) / 1000).toFixed(0);

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
