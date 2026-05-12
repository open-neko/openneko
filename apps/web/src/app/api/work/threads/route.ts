import { NextRequest, NextResponse } from "next/server";
import { and, db, desc, eq, metric, metric_snapshot } from "@neko/db";
import { getOrgId } from "@/lib/db";
import {
  createWorkMessage,
  createWorkThread,
  listWorkThreads,
} from "@/lib/work-store";

export async function GET() {
  const threads = await listWorkThreads(await getOrgId());
  return NextResponse.json({ threads });
}

export async function POST(request: NextRequest) {
  const body = await request.json().catch(() => ({}));
  const title = typeof body.title === "string" ? body.title.trim() : "";
  const seedMetricId =
    typeof body.seedMetricId === "string" && body.seedMetricId.length > 0
      ? body.seedMetricId
      : null;

  const orgId = await getOrgId();
  const thread = await createWorkThread(orgId, title);

  // When the dashboard's "Deep dive" action opens a new thread, the briefing
  // card travels into the thread as the opening user message — that way the
  // agent picks it up from the normal conversation history (getWorkThreadBundle)
  // with no extra plumbing, and reloads/new sessions still see it.
  if (seedMetricId) {
    const card = await loadBriefingCardForSeed(orgId, seedMetricId);
    if (card) {
      await createWorkMessage({
        orgId,
        threadId: thread.id,
        runId: null,
        role: "user",
        content: card,
      });
    }
  }

  return NextResponse.json({
    thread: {
      id: thread.id,
      title: thread.title || "Untitled thread",
      createdAt: thread.created_at.toISOString(),
      updatedAt: thread.updated_at.toISOString(),
      lastMessageAt: thread.last_message_at.toISOString(),
    },
  });
}

// Sentinel that flips the transcript renderer from "markdown bubble" to
// "BriefingCard component" for this one user message. The line below it
// is the full card payload as JSON — same shape as BriefingCardData — so
// the agent sees structured context AND the UI renders a real card.
export const BRIEFING_CARD_SENTINEL = "::neko-briefing-card::";

async function loadBriefingCardForSeed(
  orgId: string,
  metricId: string,
): Promise<string | null> {
  const metricRows = await db()
    .select({
      id: metric.id,
      title: metric.title,
      source: metric.source,
      chart_hint: metric.chart_hint,
    })
    .from(metric)
    .where(and(eq(metric.id, metricId), eq(metric.org_id, orgId)))
    .limit(1);
  const m = metricRows[0];
  if (!m) return null;

  const snapRows = await db()
    .select({ status: metric_snapshot.status, payload: metric_snapshot.payload })
    .from(metric_snapshot)
    .where(eq(metric_snapshot.metric_id, m.id))
    .orderBy(desc(metric_snapshot.captured_at))
    .limit(1);
  const snap = snapRows[0];
  const p = (snap?.payload as {
    mood?: string;
    headlineMetric?: string;
    headlineLabel?: string;
    insightText?: string;
    detailText?: string;
    chartType?: string;
    chartData?: Array<{ d: string; v: number; t?: number }>;
  } | null) ?? null;

  const card = {
    id: `seed-${m.id}`,
    metricId: m.id,
    source: m.source ?? "briefing",
    state: "ok" as const,
    mood: p?.mood ?? snap?.status ?? "good",
    text: m.title,
    metric: p?.headlineMetric ?? "",
    label: p?.headlineLabel ?? "",
    detail: [p?.insightText, p?.detailText].filter(Boolean).join(" "),
    chart: p?.chartType ?? m.chart_hint ?? "kpi",
    chartData: p?.chartData ?? [],
  };

  return `${BRIEFING_CARD_SENTINEL}${JSON.stringify(card)}`;
}
