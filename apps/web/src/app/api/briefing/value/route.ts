import { NextResponse } from "next/server";
import {
  action_execution,
  action_request,
  and,
  db,
  eq,
  organization,
  sql,
  work_run,
} from "@neko/db";
import { getOrgId } from "@/lib/db";

export const runtime = "nodejs";
export const dynamic = "force-dynamic";

const WINDOW_HOURS = 24;

// Realized hours saved = executed actions + completed-run analysis. Returns
// the rolling-window total (for the dashboard line) and the cumulative total
// (the value-prop hero). Estimates are agent-produced and server-clamped at
// write time; see docs/HOURS_SAVED_PLAN.md.
export async function GET() {
  const orgId = await getOrgId();
  const since = new Date(Date.now() - WINDOW_HOURS * 3600 * 1000);

  // Per-action minutes — counted once an action has actually executed.
  const [actionAgg] = await db()
    .select({
      windowMin: sql<number>`coalesce(sum(${action_request.minutes_saved}) filter (where ${action_execution.finished_at} >= ${since}), 0)::int`,
      totalMin: sql<number>`coalesce(sum(${action_request.minutes_saved}), 0)::int`,
      windowTasks: sql<number>`count(*) filter (where ${action_execution.finished_at} >= ${since})::int`,
    })
    .from(action_request)
    .innerJoin(
      action_execution,
      eq(action_execution.action_request_id, action_request.id),
    )
    .where(
      and(
        eq(action_request.org_id, orgId),
        eq(action_request.status, "executed"),
      ),
    );

  // Per-run analysis minutes — counted for completed runs (Ask + workflow).
  const [analysisAgg] = await db()
    .select({
      windowMin: sql<number>`coalesce(sum(${work_run.analysis_minutes_saved}) filter (where ${work_run.finished_at} >= ${since}), 0)::int`,
      totalMin: sql<number>`coalesce(sum(${work_run.analysis_minutes_saved}), 0)::int`,
      windowTasks: sql<number>`count(*) filter (where ${work_run.finished_at} >= ${since} and ${work_run.analysis_minutes_saved} > 0)::int`,
    })
    .from(work_run)
    .where(and(eq(work_run.org_id, orgId), eq(work_run.status, "completed")));

  const [org] = await db()
    .select({ createdAt: organization.created_at })
    .from(organization)
    .where(eq(organization.id, orgId))
    .limit(1);

  const windowMinutes = (actionAgg?.windowMin ?? 0) + (analysisAgg?.windowMin ?? 0);
  const totalMinutes = (actionAgg?.totalMin ?? 0) + (analysisAgg?.totalMin ?? 0);
  const windowTasks =
    (actionAgg?.windowTasks ?? 0) + (analysisAgg?.windowTasks ?? 0);

  return NextResponse.json({
    windowHours: WINDOW_HOURS,
    windowMinutes,
    totalMinutes,
    windowTasks,
    sinceISO: org?.createdAt?.toISOString() ?? null,
  });
}
