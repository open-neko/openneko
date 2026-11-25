export type MetricRefresh = "card" | "daily" | "weekly" | "off";

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
  taskLabel: string;
  provider: string | null;
  model: string | null;
  calls: number;
  tokens: number;
  spentUsd: number;
  unpricedCalls: number;
};

export type SpendAlert = {
  id: string;
  kind: "spend.budget_warning" | "spend.budget_blocked" | "spend.price_unknown";
  subject: string;
  message: string;
  createdAt: string;
};

export type SpendSettings = {
  limits: SpendLimitsUsd;
  metricRefresh: MetricRefresh;
  ceilings: Omit<SpendLimitsUsd, "warnPercent">;
  org: { hour: SpendWindowUsage; day: SpendWindowUsage };
  workflows: WorkflowSpendRow[];
  alerts: SpendAlert[];
  byModel: SpendByModelRow[];
};
