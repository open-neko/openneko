import { pool } from "@neko/db";
import { spendBreakdownMicros, spendWindows } from "./admission";
import { listOpenSpendAlerts, type SpendAlert } from "./alerts";
import { loadSpendLimits, metricRefreshFromColumn, microsToUsd, spendCeilingsMicros, type MetricRefresh } from "./limits";

export type SpendLimitsUsd = {
  runCapUsd: number;
  orgHourlyUsd: number;
  orgDailyUsd: number;
  workflowHourlyUsd: number;
  workflowDailyUsd: number;
  warnPercent: number;
};

export type SpendWindowUsage = {
  spentUsd: number;
  heldUsd: number;
  limitUsd: number;
  resetsAt: string;
};

export type WorkflowSpendRow = {
  workflowId: string;
  name: string;
  enabled: boolean;
  hourlyOverrideUsd: number | null;
  dailyOverrideUsd: number | null;
  hourlyLimitUsd: number;
  dailyLimitUsd: number;
  spentTodayUsd: number;
};

export type SpendByModelRow = {
  task: string | null;
  provider: string | null;
  model: string | null;
  calls: number;
  tokens: number;
  spentUsd: number;
  unpricedCalls: number;
};

export const SPEND_METER_DAYS = 30;

export type SpendSettings = {
  limits: SpendLimitsUsd;
  metricRefresh: MetricRefresh;
  ceilings: Omit<SpendLimitsUsd, "warnPercent">;
  org: { hour: SpendWindowUsage; day: SpendWindowUsage };
  workflows: WorkflowSpendRow[];
  alerts: SpendAlert[];
  /** The last SPEND_METER_DAYS days by task and model, most spent first. */
  byModel: SpendByModelRow[];
};

export async function getSpendSettings(orgId: string, now = new Date()): Promise<SpendSettings> {
  const db = pool();
  const limits = await loadSpendLimits(db, orgId);
  const ceilings = spendCeilingsMicros();
  const windows = spendWindows(now);
  const [hour, day, alerts, refresh] = await Promise.all([
    spendBreakdownMicros(db, { orgId, since: windows.hourStart }),
    spendBreakdownMicros(db, { orgId, since: windows.dayStart }),
    listOpenSpendAlerts(orgId),
    db.query<{ metric_refresh: string | null }>(
      "select metric_refresh from spend_limit where org_id = $1 and workflow_id is null",
      [orgId],
    ),
  ]);
  const { rows } = await db.query<{
    id: string;
    name: string;
    enabled: boolean;
    workflow_hourly_micros: string | null;
    workflow_daily_micros: string | null;
    spent_today: string;
  }>(
    `select w.id, w.name, w.enabled, l.workflow_hourly_micros, l.workflow_daily_micros,
            coalesce((select sum(s.cost_micros) from spend_ledger s
                       where s.org_id = w.org_id and s.workflow_id = w.id and s.created_at >= $2), 0) as spent_today
       from workflow_definition w
       left join spend_limit l on l.org_id = w.org_id and l.workflow_id = w.id
      where w.org_id = $1
      order by spent_today desc, w.name`,
    [orgId, windows.dayStart],
  );
  const meter = await db.query<{
    task: string | null;
    provider: string | null;
    model: string | null;
    calls: string;
    tokens: string;
    spent: string;
    unpriced: string;
  }>(
    `select task, provider, model, count(*) as calls, coalesce(sum(tokens), 0) as tokens,
            coalesce(sum(cost_micros), 0) as spent, count(*) filter (where priced in ('unpriced', 'fallback')) as unpriced
       from spend_ledger
      where org_id = $1 and created_at >= $2
      group by task, provider, model
      order by spent desc, calls desc
      limit 100`,
    [orgId, new Date(now.getTime() - SPEND_METER_DAYS * 86_400_000)],
  );
  const override = (value: string | null) => (value === null ? null : microsToUsd(Number(value)));
  return {
    limits: {
      runCapUsd: microsToUsd(limits.runCapMicros),
      orgHourlyUsd: microsToUsd(limits.orgHourlyMicros),
      orgDailyUsd: microsToUsd(limits.orgDailyMicros),
      workflowHourlyUsd: microsToUsd(limits.workflowHourlyMicros),
      workflowDailyUsd: microsToUsd(limits.workflowDailyMicros),
      warnPercent: limits.warnPercent,
    },
    metricRefresh: metricRefreshFromColumn(refresh.rows[0]?.metric_refresh),
    ceilings: {
      runCapUsd: microsToUsd(ceilings.runCapMicros),
      orgHourlyUsd: microsToUsd(ceilings.orgHourlyMicros),
      orgDailyUsd: microsToUsd(ceilings.orgDailyMicros),
      workflowHourlyUsd: microsToUsd(ceilings.workflowHourlyMicros),
      workflowDailyUsd: microsToUsd(ceilings.workflowDailyMicros),
    },
    org: {
      hour: {
        spentUsd: microsToUsd(hour.spentMicros),
        heldUsd: microsToUsd(hour.heldMicros),
        limitUsd: microsToUsd(limits.orgHourlyMicros),
        resetsAt: windows.hourEnd.toISOString(),
      },
      day: {
        spentUsd: microsToUsd(day.spentMicros),
        heldUsd: microsToUsd(day.heldMicros),
        limitUsd: microsToUsd(limits.orgDailyMicros),
        resetsAt: windows.dayEnd.toISOString(),
      },
    },
    workflows: rows.map((row) => ({
      workflowId: row.id,
      name: row.name,
      enabled: row.enabled,
      hourlyOverrideUsd: override(row.workflow_hourly_micros),
      dailyOverrideUsd: override(row.workflow_daily_micros),
      hourlyLimitUsd: override(row.workflow_hourly_micros) ?? microsToUsd(limits.workflowHourlyMicros),
      dailyLimitUsd: override(row.workflow_daily_micros) ?? microsToUsd(limits.workflowDailyMicros),
      spentTodayUsd: microsToUsd(Number(row.spent_today)),
    })),
    alerts,
    byModel: meter.rows.map((row) => ({
      task: row.task,
      provider: row.provider,
      model: row.model,
      calls: Number(row.calls),
      tokens: Number(row.tokens),
      spentUsd: microsToUsd(Number(row.spent)),
      unpricedCalls: Number(row.unpriced),
    })),
  };
}
