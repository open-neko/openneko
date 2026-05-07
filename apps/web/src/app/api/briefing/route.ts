import { NextRequest, NextResponse } from "next/server";
import {
  and,
  asc,
  db,
  desc,
  eq,
  metric,
  metric_snapshot,
  processing_job,
} from "@neko/db";
import { enqueue, QUEUE } from "@neko/db/jobs";
import { classifyQuestion } from "@neko/llm";
import type { A2UIMessage } from "@/a2ui/types";
import { CATALOG_ID } from "@/a2ui/catalog";
import { getOrgId } from "@/lib/db";
import { isDemoMode, mockChatResponse } from "@/lib/demo-mode";

/**
 * Briefing API.
 *
 * GET /api/briefing?role=CEO — returns the A2UI message stream for the
 * given role's briefing surface. Reads active metrics + their latest
 * metric_snapshot rows from the DB and converts them to BriefingCard
 * messages. Demo mode swaps the DB read for a mocked surface.
 *
 * POST /api/briefing — chat path. Classifies a question into card
 * metadata (slug/title/why/chartHint) via @neko/llm classifier and
 * enqueues a metric_refresh job; the worker's configured agent backend
 * (hermes | claude-agent) computes the answer.
 */

function genTimeSeriesData() {
  return Array.from({ length: 7 }, (_, i) => ({
    d: ["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"][i],
    v: Math.floor(Math.random() * 60 + 40),
    t: Math.floor(Math.random() * 40 + 50),
  }));
}

function genDonutData() {
  const labels = ["Enterprise", "Mid-market", "SMB", "Partner", "Other"];
  return labels.map((label) => ({
    d: label,
    v: Math.floor(Math.random() * 40 + 10),
    t: 0,
  }));
}

// KPI data: d = display value, v = current numeric, t = previous numeric
function genKpiData(metric: string, currentRaw: number, prevRaw: number) {
  return [{ d: metric, v: currentRaw, t: prevRaw }];
}

function moodGreeting(
  insights: Array<{ mood: string; state?: string; metric: string }>,
) {
  const counts = { good: 0, watch: 0, bad: 0, pending: 0, failed: 0 };
  for (const ins of insights) {
    if (ins.state === "failed") counts.failed++;
    else if (ins.state === "pending" || ins.metric === "Fetching…") counts.pending++;
    else if (ins.mood === "good") counts.good++;
    else if (ins.mood === "bad") counts.bad++;
    else counts.watch++;
  }
  if (counts.pending > insights.length / 2) {
    return {
      greeting: "Just a moment.",
      subtitle: "Reading the latest numbers for you.",
    };
  }
  if (counts.bad > 0 || counts.failed > 0) {
    const flags = counts.bad + counts.failed;
    return {
      greeting: flags === 1 ? "One thing to flag." : `${flags} things to flag.`,
      subtitle: "Worth your attention right now.",
    };
  }
  if (counts.watch > 0) {
    return {
      greeting: "Mostly on track.",
      subtitle: counts.watch === 1 ? "One thing worth watching." : `${counts.watch} things worth watching.`,
    };
  }
  return {
    greeting: "Everything looks healthy today.",
    subtitle: "Nothing demanding your attention.",
  };
}

function genChartData(chartType: string, metric: string) {
  switch (chartType) {
    case "kpi": {
      const num = parseFloat(metric.replace(/[^\d.]/g, "")) || 100;
      const prev = num * (0.85 + Math.random() * 0.2);
      return genKpiData(metric, num, prev);
    }
    case "donut":
      return genDonutData();
    default:
      return genTimeSeriesData();
  }
}

const ROLE_DATA: Record<string, {
  greeting: string;
  subtitle: string;
  insights: Array<{
    id: string;
    mood: string;
    text: string;
    metric: string;
    label: string;
    detail: string;
    chartType: string;
  }>;
}> = {
  CEO: {
    greeting: "Everything looks healthy today.",
    subtitle: "One thing worth watching.",
    insights: [
      { id: "ceo-1", mood: "good", text: "Revenue is 3.2% above target this month", metric: "$4.7M", label: "Revenue MTD", detail: "$4.7M vs $4.55M target. Driven by strong renewals in enterprise.", chartType: "kpi" },
      { id: "ceo-2", mood: "good", text: "Team velocity is the highest it's been in 6 sprints", metric: "142 pts", label: "Sprint Velocity", detail: "142 story points shipped vs 128 average. No increase in bug rate.", chartType: "bar" },
      { id: "ceo-3", mood: "watch", text: "APAC churn ticked up to 4.1% — was 2.8% last month", metric: "4.1%", label: "APAC Churn", detail: "3 mid-market accounts in Singapore flagged. CS team is on it.", chartType: "line" },
      { id: "ceo-4", mood: "good", text: "NPS improved to 72 from 68 last quarter", metric: "72", label: "NPS Score", detail: "Key driver: onboarding redesign. Detractor comments down 40%.", chartType: "kpi" },
    ],
  },
  CFO: {
    greeting: "Finances are in good shape.",
    subtitle: "Runway is comfortable. OpEx trending slightly high.",
    insights: [
      { id: "cfo-1", mood: "good", text: "Runway is 18.4 months at current burn rate", metric: "18.4 mo", label: "Runway", detail: "$12.2M in bank. Monthly burn $660K. Improving from 16 months last quarter.", chartType: "kpi" },
      { id: "cfo-2", mood: "watch", text: "OpEx is 8% over budget — cloud costs spiked", metric: "+8%", label: "OpEx vs Budget", detail: "AWS bill up $42K due to GPU usage for model training. Consider reserved instances.", chartType: "bar" },
      { id: "cfo-3", mood: "good", text: "Collections are 94% on time this quarter", metric: "94%", label: "Collections", detail: "Only 2 invoices past 45 days. Both from government accounts (expected).", chartType: "kpi" },
      { id: "cfo-4", mood: "good", text: "Gross margin holding steady at 71%", metric: "71%", label: "Gross Margin", detail: "Up from 68% last year. SaaS revenue mix improving.", chartType: "area" },
    ],
  },
  CRO: {
    greeting: "Pipeline is strong this quarter.",
    subtitle: "You're 82% to target with 5 weeks left.",
    insights: [
      { id: "cro-1", mood: "good", text: "Pipeline value is $8.2M — 2.4x of remaining target", metric: "$8.2M", label: "Pipeline", detail: "28 qualified opportunities. Weighted forecast: $3.4M expected to close.", chartType: "area" },
      { id: "cro-2", mood: "good", text: "Demo-to-close rate improved to 34% from 28%", metric: "34%", label: "Conversion", detail: "New demo script performing well. Average deal cycle down to 22 days.", chartType: "kpi" },
      { id: "cro-3", mood: "watch", text: "Inbound leads down 12% this month vs last", metric: "-12%", label: "Inbound Leads", detail: "Blog traffic dropped after SEO algo change. Content team adjusting.", chartType: "line" },
      { id: "cro-4", mood: "good", text: "Average deal size up 18% to $68K", metric: "$68K", label: "Avg Deal", detail: "Enterprise tier adoption driving this. 4 deals above $100K in pipeline.", chartType: "kpi" },
    ],
  },
};

type SnapshotPayload = {
  headlineMetric?: string;
  headlineLabel?: string;
  insightText?: string;
  detailText?: string;
  chartType?: string;
  chartData?: Array<{ d: string; v: number; t?: number }>;
} | null;

export async function GET(request: NextRequest) {
  const role = request.nextUrl.searchParams.get("role") ?? "CEO";
  const surfaceId = `briefing-${role.toLowerCase()}`;
  const demo = isDemoMode();

  const cards = demo
    ? []
    : await db()
        .query.metric.findMany({
          where: and(
            eq(metric.org_id, (await getOrgId())),
            eq(metric.role, role),
            eq(metric.active, true),
          ),
          orderBy: asc(metric.slug),
          with: {
            snapshots: {
              orderBy: desc(metric_snapshot.captured_at),
              limit: 1,
            },
          },
        })
        .catch(() => [] as Array<never>);

  const dbInsights = cards.map((m) => {
    const snap = m.snapshots?.[0];
    const p = snap?.payload as SnapshotPayload;
    const hasData = !!p;
    // Card state — read by BriefingCard to render the right chrome:
    //   ok      → snapshot present, render data
    //   failed  → last metric_refresh failed; render error + Retry
    //   pending → no snapshot yet (or status unknown); render skeleton
    // Failure trumps a stale snapshot: a card that failed on its most
    // recent refresh should show the error even if an older snapshot
    // exists, because the user's last action expected fresh data.
    const state =
      m.last_refresh_status === "failed"
        ? "failed"
        : hasData
          ? "ok"
          : "pending";
    return {
      id: m.slug,
      metricId: m.id,
      source: m.source,
      state,
      error: state === "failed" ? (m.last_refresh_error ?? "Unknown error") : undefined,
      mood: state === "failed" ? "bad" : ((snap?.status ?? "watch") as string),
      text: m.title,
      metric:
        state === "failed"
          ? "Couldn't load"
          : hasData
            ? (p?.headlineMetric ?? "—")
            : "Fetching…",
      label: state === "ok" ? (p?.headlineLabel ?? "") : "",
      detail:
        state === "failed"
          ? (m.last_refresh_error ?? "Unknown error")
          : hasData
            ? [p?.insightText, p?.detailText].filter(Boolean).join(" ")
            : (m.why ?? ""),
      chartType: p?.chartType ?? m.chart_hint ?? "line",
      chartDataOverride: state === "ok" && hasData ? p?.chartData : [],
      fromDb: true,
    };
  });

  const hasRealCards = dbInsights.length > 0;
  const roleData = hasRealCards
    ? {
        ...moodGreeting(dbInsights),
        insights: dbInsights,
      }
    : (ROLE_DATA[role] ?? ROLE_DATA.CEO);

  const messages: A2UIMessage[] = [];

  messages.push({
    version: "v0.9",
    createSurface: {
      surfaceId,
      catalogId: CATALOG_ID,
    },
  });

  const dataModel: Record<string, unknown> = {
    role,
    greeting: roleData.greeting,
    subtitle: roleData.subtitle,
    insights: Object.fromEntries(
      roleData.insights.map((ins) => {
        const override = (ins as { chartDataOverride?: Array<{ d: string; v: number; t?: number }> }).chartDataOverride;
        const fromDb = (ins as { fromDb?: boolean }).fromDb === true;
        const chartData = fromDb
          ? (override ?? [])
          : (override && override.length > 0 ? override : genChartData(ins.chartType, ins.metric));
        return [
          ins.id,
          // state defaults to "ok" for the example/mock-data path so the
          // BriefingCard component doesn't see undefined when reading
          // /insights/<id>/state for hardcoded ROLE_DATA cards.
          { state: "ok", error: undefined, ...ins, chartData },
        ];
      })
    ),
  };

  messages.push({
    version: "v0.9",
    updateDataModel: {
      surfaceId,
      value: dataModel,
    },
  });

  const insightIds = roleData.insights.map((ins) => ins.id);

  const components = [
    {
      id: "root",
      component: "Briefing",
      greeting: { path: "/greeting" },
      subtitle: { path: "/subtitle" },
      role: { path: "/role" },
      isExample: !hasRealCards,
      children: insightIds,
    },
    ...roleData.insights.map((ins) => ({
      id: ins.id,
      component: "BriefingCard",
      metricId: { path: `/insights/${ins.id}/metricId` },
      source: { path: `/insights/${ins.id}/source` },
      state: { path: `/insights/${ins.id}/state` },
      error: { path: `/insights/${ins.id}/error` },
      mood: { path: `/insights/${ins.id}/mood` },
      text: { path: `/insights/${ins.id}/text` },
      metric: { path: `/insights/${ins.id}/metric` },
      label: { path: `/insights/${ins.id}/label` },
      detail: { path: `/insights/${ins.id}/detail` },
      chartType: { path: `/insights/${ins.id}/chartType` },
      chartData: { path: `/insights/${ins.id}/chartData` },
    })),
  ];

  messages.push({
    version: "v0.9",
    updateComponents: {
      surfaceId,
      components,
    },
  });

  return NextResponse.json(messages);
}

// POST /api/briefing — classify question → enqueue metric_refresh → return skeleton
export async function POST(request: NextRequest) {
  const body = await request.json();
  const { surfaceId, message } = body as { surfaceId: string; message: string };

  if (!message?.trim()) {
    return NextResponse.json({ error: "empty message" }, { status: 400 });
  }

  if (isDemoMode()) {
    const answer = mockChatResponse(message);
    const chatId = `chat-demo-${Date.now().toString(36)}${Math.random().toString(36).slice(2, 6)}`;
    return NextResponse.json({ mock: true, chatId, answer });
  }

  const role = (surfaceId?.replace("briefing-", "") ?? "ceo").toUpperCase();
  const orgId = await getOrgId();

  console.log(
    `[chat] org=${orgId} role=${role} surface=${surfaceId} q=${JSON.stringify(message).slice(0, 500)}`,
  );

  // Pass 1: classify the question in-process (~2-5s LLM call). Was an HTTP
  // round-trip to the worker before @neko/llm extraction.
  let card;
  try {
    card = await classifyQuestion(message, role, orgId);
    console.log(
      `[chat] org=${orgId} classify -> slug=${card.slug} chartHint=${card.chartHint} title=${JSON.stringify(card.title).slice(0, 200)}`,
    );
  } catch (e) {
    const err = e instanceof Error ? e.message : "classify failed";
    console.error(`[chat] org=${orgId} classify failed: ${err}`);
    if (e instanceof Error && e.stack) console.error(e.stack);
    return NextResponse.json({ error: err }, { status: 500 });
  }

  // Pass 2: enqueue metric_refresh job with inline metadata
  const inserted = await db()
    .insert(processing_job)
    .values({
      org_id: orgId,
      kind: "metric_refresh",
      status: "queued",
      trigger: "chat",
      trigger_payload: {
        question: message,
        slug: card.slug,
        title: card.title,
        why: card.why,
        chartHint: card.chartHint,
        role,
      },
    })
    .returning({ id: processing_job.id });
  const jobId = inserted[0]?.id;
  if (!jobId) {
    console.error(`[chat] org=${orgId} insert processing_job returned no id`);
    return NextResponse.json({ error: "failed to enqueue job" }, { status: 500 });
  }

  await enqueue(QUEUE.METRIC_REFRESH, {
    processingJobId: jobId,
    orgId,
  });
  console.log(`[chat] org=${orgId} enqueued metric_refresh job=${jobId}`);

  return NextResponse.json({
    jobId,
    chatId: `chat-${jobId}`,
    skeleton: {
      text: card.title,
      metric: "Loading…",
      label: "",
      detail: card.why,
      chartType: card.chartHint,
      chartData: [],
      mood: "watch",
    },
  });
}
