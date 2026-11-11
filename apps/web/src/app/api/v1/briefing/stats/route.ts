import { NextResponse } from "next/server";
import {
  action_request,
  and,
  db,
  eq,
  gte,
  inArray,
  sql,
  workflow_definition,
  workflow_output,
  workflow_run,
} from "@neko/db";
import { getOrgId } from "@/lib/db";
import { actionRequestVisibility, watcherRunVisibility, workflowIdVisibility } from "@/lib/entitlements";

export const runtime = "nodejs";
export const dynamic = "force-dynamic";

function startOfTodayUtc(): Date {
  const d = new Date();
  d.setUTCHours(0, 0, 0, 0);
  return d;
}

// OL9 — the Briefing stat strip: runs today · findings · pending
// approvals · daily budget %. Budget % is the *hottest* workflow's
// utilisation (runs today / daily_run_budget), the one closest to its
// brake; the strip only shows it >= 40%.
export async function GET() {
  const orgId = await getOrgId();
  const dayStart = startOfTodayUtc();
  const [workflowVisible, actionVisible] = await Promise.all([
    workflowIdVisibility(),
    actionRequestVisibility(),
  ]);

  const runs = await db()
    .select({ id: workflow_run.id, workflowId: workflow_run.workflow_id })
    .from(workflow_run)
    .where(
      and(eq(workflow_run.org_id, orgId), gte(workflow_run.created_at, dayStart)),
    );

  const findings = await db()
    .select({ runId: workflow_output.workflow_run_id, workflowId: workflow_run.workflow_id })
    .from(workflow_output)
    .innerJoin(workflow_run, eq(workflow_run.id, workflow_output.workflow_run_id))
    .where(
      and(
        eq(workflow_output.org_id, orgId),
        gte(workflow_output.created_at, dayStart),
        inArray(workflow_output.mood, ["watch", "act"]),
      ),
    );

  const watcherVisible = await watcherRunVisibility([
    ...runs.map((r) => r.id),
    ...findings.map((f) => f.runId),
  ]);
  const visibleRuns = runs.filter((r) => workflowVisible(r.workflowId) && watcherVisible(r.id));

  const approvals = await db()
    .select({ kind: action_request.kind, scope: action_request.scope, workflowId: workflow_run.workflow_id })
    .from(action_request)
    .leftJoin(workflow_run, eq(workflow_run.id, action_request.workflow_run_id))
    .where(
      and(
        eq(action_request.org_id, orgId),
        eq(action_request.status, "pending_approval"),
      ),
    );

  const ranToday = new Map<string, number>();
  for (const run of runs) ranToday.set(run.workflowId, (ranToday.get(run.workflowId) ?? 0) + 1);

  const budgeted = await db()
    .select({ workflowId: workflow_definition.id, budget: workflow_definition.daily_run_budget })
    .from(workflow_definition)
    .where(
      and(
        eq(workflow_definition.org_id, orgId),
        sql`${workflow_definition.daily_run_budget} is not null`,
      ),
    );

  let budgetPct: number | null = null;
  for (const row of budgeted) {
    if (!row.budget || row.budget <= 0 || !workflowVisible(row.workflowId)) continue;
    const pct = Math.round(((ranToday.get(row.workflowId) ?? 0) / row.budget) * 100);
    if (budgetPct === null || pct > budgetPct) budgetPct = pct;
  }

  return NextResponse.json({
    runsToday: visibleRuns.length,
    findingsToday: findings.filter((f) => workflowVisible(f.workflowId) && watcherVisible(f.runId)).length,
    pendingApprovals: approvals.filter((a) => actionVisible(a)).length,
    budgetPct,
  });
}
