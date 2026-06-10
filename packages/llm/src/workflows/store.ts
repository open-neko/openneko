import {
  and,
  data_source,
  db,
  desc,
  eq,
  inArray,
  observation,
  source_change_log,
  sql,
  subscription,
  workflow_definition,
  workflow_output,
  workflow_output_source_observation,
  workflow_run,
} from "@neko/db";

export type WorkflowStep = {
  id: string;
  description: string;
};

export type WorkflowTriggers = {
  cron?: string;
  timezone?: string;
  enabled?: boolean;
  // Data-change trigger ("fire when a row matches"). saveWorkflow ignores it;
  // saveWorkflowWithTrigger persists it as a subscription row.
  when?: {
    table: string;
    where?: Record<string, unknown>;
    select?: string[];
    primary_key: string[];
    version_column?: string;
    enabled?: boolean;
    idempotency_key_template?: string;
    acknowledge_mutation_loop?: boolean;
  };
};

export type WorkflowRecord = {
  id: string;
  orgId: string;
  name: string;
  description: string;
  enabled: boolean;
  status: string;
  goal: string;
  systemPromptOverlay: string;
  steps: WorkflowStep[];
  cron: string | null;
  cronTimezone: string;
  cronEnabled: boolean;
  dailyRunBudget: number | null;
  outputContract: Record<string, unknown> | null;
  createdByThreadId: string | null;
  createdByRunId: string | null;
  /** CV1: '' = org layer; a member's personal workflow carries their id. */
  ownerUserId: string;
  /** Stable identity across copy/promote lineage (self for originals). */
  originId: string | null;
  /** The workflow this one was forked/promoted from. */
  parentId: string | null;
  createdAt: Date;
  updatedAt: Date;
};

export type SaveWorkflowInput = {
  orgId: string;
  name: string;
  description?: string;
  systemPromptOverlay?: string;
  steps: WorkflowStep[];
  goal?: string;
  triggers?: WorkflowTriggers;
  dailyRunBudget?: number | null;
  outputContract?: Record<string, unknown> | null;
  createdByThreadId?: string | null;
  createdByRunId?: string | null;
  /** CV1: omit/'' = org layer. */
  ownerUserId?: string;
};

export type SaveWorkflowResult = {
  action: "created" | "updated";
  workflow: WorkflowRecord;
};

function toRecord(
  row: typeof workflow_definition.$inferSelect,
): WorkflowRecord {
  return {
    id: row.id,
    orgId: row.org_id,
    name: row.name,
    description: row.description,
    enabled: row.enabled,
    status: row.status,
    goal: row.goal,
    systemPromptOverlay: row.system_prompt_overlay,
    steps: (row.steps as WorkflowStep[]) ?? [],
    cron: row.cron,
    cronTimezone: row.cron_timezone,
    cronEnabled: row.cron_enabled,
    dailyRunBudget: row.daily_run_budget,
    outputContract:
      (row.output_contract as Record<string, unknown> | null) ?? null,
    createdByThreadId: row.created_by_thread_id,
    createdByRunId: row.created_by_run_id,
    ownerUserId: row.owner_user_id,
    originId: row.origin_id,
    parentId: row.parent_id,
    createdAt: row.created_at,
    updatedAt: row.updated_at,
  };
}

export async function getWorkflowByOrgName(
  orgId: string,
  name: string,
  ownerUserId = "",
): Promise<WorkflowRecord | null> {
  const rows = await db()
    .select()
    .from(workflow_definition)
    .where(
      and(
        eq(workflow_definition.org_id, orgId),
        eq(workflow_definition.owner_user_id, ownerUserId),
        eq(workflow_definition.name, name),
      ),
    )
    .limit(1);
  return rows[0] ? toRecord(rows[0]) : null;
}

export async function getWorkflow(
  orgId: string,
  workflowId: string,
): Promise<WorkflowRecord | null> {
  const rows = await db()
    .select()
    .from(workflow_definition)
    .where(
      and(
        eq(workflow_definition.org_id, orgId),
        eq(workflow_definition.id, workflowId),
      ),
    )
    .limit(1);
  return rows[0] ? toRecord(rows[0]) : null;
}

export async function listWorkflows(orgId: string): Promise<WorkflowRecord[]> {
  const rows = await db()
    .select()
    .from(workflow_definition)
    .where(eq(workflow_definition.org_id, orgId))
    .orderBy(desc(workflow_definition.updated_at));
  return rows.map(toRecord);
}

/**
 * OL7 "pause for today": re-enable workflows whose pause timer has
 * passed. The cron sweep calls this every tick so a paused workflow
 * resumes within ~a minute of its paused_until.
 */
export async function reEnablePausedWorkflows(): Promise<number> {
  const rows = await db()
    .update(workflow_definition)
    .set({ enabled: true, paused_until: null, updated_at: new Date() })
    .where(
      and(
        eq(workflow_definition.enabled, false),
        sql`${workflow_definition.paused_until} is not null`,
        sql`${workflow_definition.paused_until} <= now()`,
      ),
    )
    .returning({ id: workflow_definition.id });
  return rows.length;
}

export async function listCronWorkflows(): Promise<WorkflowRecord[]> {
  const rows = await db()
    .select()
    .from(workflow_definition)
    .where(
      and(
        eq(workflow_definition.enabled, true),
        eq(workflow_definition.cron_enabled, true),
        sql`${workflow_definition.cron} is not null`,
      ),
    );
  return rows.map(toRecord);
}

export async function saveWorkflow(
  input: SaveWorkflowInput,
): Promise<SaveWorkflowResult> {
  const ownerUserId = input.ownerUserId ?? "";
  const existing = await getWorkflowByOrgName(input.orgId, input.name, ownerUserId);
  const cron = input.triggers?.cron ?? null;
  const cronTimezone = input.triggers?.timezone ?? "UTC";
  const cronEnabled = input.triggers?.enabled ?? true;

  if (existing) {
    const [row] = await db()
      .update(workflow_definition)
      .set({
        description: input.description ?? existing.description,
        system_prompt_overlay:
          input.systemPromptOverlay ?? existing.systemPromptOverlay,
        steps: input.steps,
        goal: input.goal ?? existing.goal,
        cron,
        cron_timezone: cronTimezone,
        cron_enabled: cronEnabled,
        daily_run_budget:
          input.dailyRunBudget === undefined
            ? existing.dailyRunBudget
            : input.dailyRunBudget,
        output_contract:
          input.outputContract === undefined
            ? existing.outputContract
            : input.outputContract,
        updated_at: new Date(),
      })
      .where(eq(workflow_definition.id, existing.id))
      .returning();
    const updated = toRecord(row);
    await versionWorkflowDefinition(updated, "Updated");
    return { action: "updated", workflow: updated };
  }

  const [row] = await db()
    .insert(workflow_definition)
    .values({
      org_id: input.orgId,
      owner_user_id: ownerUserId,
      name: input.name,
      description: input.description ?? "",
      system_prompt_overlay: input.systemPromptOverlay ?? "",
      steps: input.steps,
      goal: input.goal ?? "",
      cron,
      cron_timezone: cronTimezone,
      cron_enabled: cronEnabled,
      daily_run_budget: input.dailyRunBudget ?? null,
      output_contract: input.outputContract ?? null,
      created_by_thread_id: input.createdByThreadId ?? null,
      created_by_run_id: input.createdByRunId ?? null,
    })
    .returning();
  // origin_id = self for originals (lineage root).
  const [withOrigin] = await db()
    .update(workflow_definition)
    .set({ origin_id: row.id })
    .where(eq(workflow_definition.id, row.id))
    .returning();
  const created = toRecord(withOrigin);
  await versionWorkflowDefinition(created, "Added");
  return { action: "created", workflow: created };
}

/**
 * CV0: serialize the definitional half of a workflow to
 * workflows/<name>.md in the org config repo and commit. Best-effort —
 * never fails the save. Operational state (enabled, budgets, run history)
 * stays in the DB.
 */
async function versionWorkflowDefinition(
  workflow: WorkflowRecord,
  verb: "Added" | "Updated",
): Promise<void> {
  try {
    const { getOrgAgentRoot } = await import("../work/workspace");
    const { recordConfigChange } = await import("../config-vcs");
    const { mkdir, writeFile } = await import("node:fs/promises");
    const { join } = await import("node:path");
    const root = getOrgAgentRoot(workflow.orgId);
    const slug = workflow.name
      .toLowerCase()
      .replace(/[^a-z0-9]+/g, "-")
      .replace(/^-|-$/g, "")
      .slice(0, 80);
    const body = [
      "---",
      `name: ${JSON.stringify(workflow.name)}`,
      `goal: ${JSON.stringify(workflow.goal)}`,
      `cron: ${workflow.cron ? JSON.stringify(workflow.cron) : "null"}`,
      `cron_timezone: ${JSON.stringify(workflow.cronTimezone)}`,
      "---",
      "",
      workflow.description,
      "",
      "## Steps",
      ...workflow.steps.map(
        (s, i) => `${i + 1}. ${typeof s === "object" && s && "description" in s ? String((s as { description: unknown }).description) : String(s)}`,
      ),
      ...(workflow.systemPromptOverlay
        ? ["", "## System prompt overlay", "", workflow.systemPromptOverlay]
        : []),
      "",
    ].join("\n");
    await mkdir(join(root, "workflows"), { recursive: true });
    await writeFile(join(root, "workflows", `${slug}.md`), body, "utf8");
    await recordConfigChange({
      workspaceRoot: root,
      orgId: workflow.orgId,
      paths: [`workflows/${slug}.md`],
      message: `${verb} workflow: ${workflow.name}`,
    });
  } catch (err) {
    console.warn(
      `[config-vcs] workflow versioning failed (save succeeded): ${err instanceof Error ? err.message : err}`,
    );
  }
}

export type WorkflowRunRecord = {
  id: string;
  orgId: string;
  workflowId: string;
  threadId: string;
  workRunId: string;
  triggerKind: "manual" | "cron" | "subscription";
  triggerPayload: Record<string, unknown>;
  triggeredBySubscriptionId: string | null;
  triggeredByOutputId: string | null;
  triggeredByObservationId: string | null;
  chainDepth: number;
  status: string;
  startedAt: Date | null;
  finishedAt: Date | null;
  summary: string | null;
  error: string | null;
  createdAt: Date;
  updatedAt: Date;
};

export type CreateWorkflowRunInput = {
  orgId: string;
  workflowId: string;
  threadId: string;
  workRunId: string;
  triggerKind: "manual" | "cron" | "subscription";
  triggerPayload?: Record<string, unknown>;
  chainDepth?: number;
  triggeredBySubscriptionId?: string | null;
  triggeredByOutputId?: string | null;
  triggeredByObservationId?: string | null;
};

function toRunRecord(
  row: typeof workflow_run.$inferSelect,
): WorkflowRunRecord {
  return {
    id: row.id,
    orgId: row.org_id,
    workflowId: row.workflow_id,
    threadId: row.thread_id,
    workRunId: row.work_run_id,
    triggerKind: row.trigger_kind as WorkflowRunRecord["triggerKind"],
    triggerPayload: (row.trigger_payload as Record<string, unknown>) ?? {},
    triggeredBySubscriptionId: row.triggered_by_subscription_id,
    triggeredByOutputId: row.triggered_by_output_id,
    triggeredByObservationId: row.triggered_by_observation_id,
    chainDepth: row.chain_depth,
    status: row.status,
    startedAt: row.started_at,
    finishedAt: row.finished_at,
    summary: row.summary,
    error: row.error,
    createdAt: row.created_at,
    updatedAt: row.updated_at,
  };
}

export async function createWorkflowRun(
  input: CreateWorkflowRunInput,
): Promise<WorkflowRunRecord> {
  const [row] = await db()
    .insert(workflow_run)
    .values({
      org_id: input.orgId,
      workflow_id: input.workflowId,
      thread_id: input.threadId,
      work_run_id: input.workRunId,
      trigger_kind: input.triggerKind,
      trigger_payload: input.triggerPayload ?? {},
      triggered_by_subscription_id: input.triggeredBySubscriptionId ?? null,
      triggered_by_output_id: input.triggeredByOutputId ?? null,
      triggered_by_observation_id: input.triggeredByObservationId ?? null,
      chain_depth: input.chainDepth ?? 0,
      status: "running",
      started_at: new Date(),
    })
    .returning();
  return toRunRecord(row);
}

export async function finishWorkflowRun(args: {
  workflowRunId: string;
  status: string;
  summary?: string | null;
  error?: string | null;
}): Promise<void> {
  await db()
    .update(workflow_run)
    .set({
      status: args.status,
      summary: args.summary ?? null,
      error: args.error ?? null,
      finished_at: new Date(),
      updated_at: new Date(),
    })
    .where(eq(workflow_run.id, args.workflowRunId));
}

export type WorkflowOutputInput = {
  orgId: string;
  workflowRunId: string;
  workRunId: string;
  kind: string;
  title?: string;
  body?: string;
  payload?: Record<string, unknown>;
  artifactPath?: string | null;
  scope?: string | null;
  topic?: string | null;
  mood?: "good" | "watch" | "act" | null;
  timeWindowStart?: Date | null;
  timeWindowEnd?: Date | null;
  freshnessTtlSeconds?: number | null;
};

export type WorkflowOutputRecord = {
  id: string;
  orgId: string;
  workflowRunId: string;
  workRunId: string;
  kind: string;
  title: string;
  body: string;
  payload: Record<string, unknown>;
  artifactPath: string | null;
  scope: string | null;
  topic: string | null;
  mood: string | null;
  timeWindowStart: Date | null;
  timeWindowEnd: Date | null;
  freshnessTtlSeconds: number | null;
  createdAt: Date;
};

function toOutputRecord(
  row: typeof workflow_output.$inferSelect,
): WorkflowOutputRecord {
  return {
    id: row.id,
    orgId: row.org_id,
    workflowRunId: row.workflow_run_id,
    workRunId: row.work_run_id,
    kind: row.kind,
    title: row.title,
    body: row.body,
    payload: (row.payload as Record<string, unknown>) ?? {},
    artifactPath: row.artifact_path,
    scope: row.scope,
    topic: row.topic,
    mood: row.mood,
    timeWindowStart: row.time_window_start,
    timeWindowEnd: row.time_window_end,
    freshnessTtlSeconds: row.freshness_ttl_seconds,
    createdAt: row.created_at,
  };
}

export async function emitWorkflowOutput(
  input: WorkflowOutputInput,
): Promise<WorkflowOutputRecord> {
  const [row] = await db()
    .insert(workflow_output)
    .values({
      org_id: input.orgId,
      workflow_run_id: input.workflowRunId,
      work_run_id: input.workRunId,
      kind: input.kind,
      title: input.title ?? "",
      body: input.body ?? "",
      payload: input.payload ?? {},
      artifact_path: input.artifactPath ?? null,
      scope: input.scope ?? null,
      topic: input.topic ?? null,
      mood: input.mood ?? null,
      time_window_start: input.timeWindowStart ?? null,
      time_window_end: input.timeWindowEnd ?? null,
      freshness_ttl_seconds: input.freshnessTtlSeconds ?? null,
    })
    .returning();
  return toOutputRecord(row);
}

export type SubscriptionSourceKind =
  | "workflow_output"
  | "source_change"
  | "external_event";

export type SubscriptionRecord = {
  id: string;
  orgId: string;
  workflowId: string;
  sourceKind: SubscriptionSourceKind;
  filter: Record<string, unknown>;
  enabled: boolean;
  debounceMs: number;
  maxConcurrentRuns: number;
  maxChainDepthOverride: number | null;
  idempotencyKeyTemplate: string | null;
  createdAt: Date;
  updatedAt: Date;
};

export type CreateSubscriptionInput = {
  orgId: string;
  workflowId: string;
  sourceKind: SubscriptionSourceKind;
  filter?: Record<string, unknown>;
  enabled?: boolean;
  debounceMs?: number;
  maxConcurrentRuns?: number;
  maxChainDepthOverride?: number | null;
  idempotencyKeyTemplate?: string | null;
};

function toSubscriptionRecord(
  row: typeof subscription.$inferSelect,
): SubscriptionRecord {
  return {
    id: row.id,
    orgId: row.org_id,
    workflowId: row.workflow_id,
    sourceKind: row.source_kind as SubscriptionSourceKind,
    filter: (row.filter as Record<string, unknown>) ?? {},
    enabled: row.enabled,
    debounceMs: row.debounce_ms,
    maxConcurrentRuns: row.max_concurrent_runs,
    maxChainDepthOverride: row.max_chain_depth_override,
    idempotencyKeyTemplate: row.idempotency_key_template,
    createdAt: row.created_at,
    updatedAt: row.updated_at,
  };
}

export async function createSubscription(
  input: CreateSubscriptionInput,
): Promise<SubscriptionRecord> {
  const [row] = await db()
    .insert(subscription)
    .values({
      org_id: input.orgId,
      workflow_id: input.workflowId,
      source_kind: input.sourceKind,
      filter: input.filter ?? {},
      enabled: input.enabled ?? true,
      debounce_ms: input.debounceMs ?? 0,
      max_concurrent_runs: input.maxConcurrentRuns ?? 5,
      max_chain_depth_override: input.maxChainDepthOverride ?? null,
      idempotency_key_template: input.idempotencyKeyTemplate ?? null,
    })
    .returning();
  return toSubscriptionRecord(row);
}

export async function listEnabledSubscriptions(args: {
  sourceKind?: SubscriptionSourceKind;
} = {}): Promise<SubscriptionRecord[]> {
  const rows = await db()
    .select()
    .from(subscription)
    .where(
      args.sourceKind
        ? and(
            eq(subscription.enabled, true),
            eq(subscription.source_kind, args.sourceKind),
          )
        : eq(subscription.enabled, true),
    );
  return rows.map(toSubscriptionRecord);
}

export async function listSubscriptionsByWorkflow(
  orgId: string,
  workflowId: string,
): Promise<SubscriptionRecord[]> {
  const rows = await db()
    .select()
    .from(subscription)
    .where(
      and(
        eq(subscription.org_id, orgId),
        eq(subscription.workflow_id, workflowId),
      ),
    )
    .orderBy(desc(subscription.created_at));
  return rows.map(toSubscriptionRecord);
}

export async function setSubscriptionEnabled(
  id: string,
  enabled: boolean,
): Promise<void> {
  await db()
    .update(subscription)
    .set({ enabled, updated_at: new Date() })
    .where(eq(subscription.id, id));
}

export async function deleteSubscription(id: string): Promise<void> {
  await db().delete(subscription).where(eq(subscription.id, id));
}

export type ObservationConsumerKind = "workflow" | "human";

export type ObservationRecord = {
  id: string;
  orgId: string;
  sourceOutputId: string | null;
  consumerKind: ObservationConsumerKind;
  consumerWorkflowId: string | null;
  consumerRunId: string | null;
  consumerUserId: string | null;
  subscriptionId: string | null;
  title: string | null;
  body: string | null;
  mood: string | null;
  status: string;
  firstSeenAt: Date;
  lastSeenAt: Date;
  createdAt: Date;
  updatedAt: Date;
};

export type CreateObservationInput = {
  orgId: string;
  sourceOutputId?: string | null;
  consumerKind: ObservationConsumerKind;
  consumerWorkflowId?: string | null;
  consumerRunId?: string | null;
  consumerUserId?: string | null;
  subscriptionId?: string | null;
  title?: string | null;
  body?: string | null;
  mood?: "good" | "watch" | "act" | null;
};

function toObservationRecord(
  row: typeof observation.$inferSelect,
): ObservationRecord {
  return {
    id: row.id,
    orgId: row.org_id,
    sourceOutputId: row.source_output_id,
    consumerKind: row.consumer_kind as ObservationConsumerKind,
    consumerWorkflowId: row.consumer_workflow_id,
    consumerRunId: row.consumer_run_id,
    consumerUserId: row.consumer_user_id,
    subscriptionId: row.subscription_id,
    title: row.title,
    body: row.body,
    mood: row.mood,
    status: row.status,
    firstSeenAt: row.first_seen_at,
    lastSeenAt: row.last_seen_at,
    createdAt: row.created_at,
    updatedAt: row.updated_at,
  };
}

export async function createObservation(
  input: CreateObservationInput,
): Promise<ObservationRecord> {
  const [row] = await db()
    .insert(observation)
    .values({
      org_id: input.orgId,
      source_output_id: input.sourceOutputId ?? null,
      consumer_kind: input.consumerKind,
      consumer_workflow_id: input.consumerWorkflowId ?? null,
      consumer_run_id: input.consumerRunId ?? null,
      consumer_user_id: input.consumerUserId ?? null,
      subscription_id: input.subscriptionId ?? null,
      title: input.title ?? null,
      body: input.body ?? null,
      mood: input.mood ?? null,
    })
    .returning();
  return toObservationRecord(row);
}

export async function getObservation(
  orgId: string,
  id: string,
): Promise<ObservationRecord | null> {
  const rows = await db()
    .select()
    .from(observation)
    .where(and(eq(observation.org_id, orgId), eq(observation.id, id)))
    .limit(1);
  return rows[0] ? toObservationRecord(rows[0]) : null;
}

export async function listObservationsForOutput(
  orgId: string,
  sourceOutputId: string,
): Promise<ObservationRecord[]> {
  const rows = await db()
    .select()
    .from(observation)
    .where(
      and(
        eq(observation.org_id, orgId),
        eq(observation.source_output_id, sourceOutputId),
      ),
    )
    .orderBy(desc(observation.created_at));
  return rows.map(toObservationRecord);
}

export async function listObservationsByConsumerWorkflow(
  orgId: string,
  consumerWorkflowId: string,
  limit = 100,
): Promise<ObservationRecord[]> {
  const rows = await db()
    .select()
    .from(observation)
    .where(
      and(
        eq(observation.org_id, orgId),
        eq(observation.consumer_workflow_id, consumerWorkflowId),
      ),
    )
    .orderBy(desc(observation.created_at))
    .limit(limit);
  return rows.map(toObservationRecord);
}

/**
 * Link a produced workflow_output to the observations its producing run
 * consumed. Called by the runner once an output is emitted and the run's
 * consumed-observation set is known.
 */
export async function linkOutputSourceObservations(
  workflowOutputId: string,
  observationIds: string[],
): Promise<void> {
  if (observationIds.length === 0) return;
  await db()
    .insert(workflow_output_source_observation)
    .values(
      observationIds.map((observationId) => ({
        workflow_output_id: workflowOutputId,
        observation_id: observationId,
      })),
    )
    .onConflictDoNothing();
}

export async function countWorkflowRunsForSubscription(
  subscriptionId: string,
  status: "queued" | "running",
): Promise<number> {
  const rows = await db()
    .select({ id: workflow_run.id })
    .from(workflow_run)
    .where(
      and(
        eq(workflow_run.triggered_by_subscription_id, subscriptionId),
        eq(workflow_run.status, status),
      ),
    );
  return rows.length;
}

export async function countSubscriptionsMatchingOutput(
  outputId: string,
  sinceMs: number,
): Promise<number> {
  const since = new Date(Date.now() - sinceMs);
  const rows = await db()
    .select({ id: observation.id })
    .from(observation)
    .where(
      and(
        eq(observation.source_output_id, outputId),
        sql`${observation.created_at} >= ${since}`,
      ),
    );
  return rows.length;
}

export async function getWorkflowRunChainDepth(
  workflowRunId: string,
): Promise<number | null> {
  const rows = await db()
    .select({ chain_depth: workflow_run.chain_depth })
    .from(workflow_run)
    .where(eq(workflow_run.id, workflowRunId))
    .limit(1);
  return rows[0]?.chain_depth ?? null;
}

/**
 * Start of the current UTC day. Workflow daily budgets reset at this
 * boundary; we deliberately don't track per-org timezones for v1 — UTC
 * is the simplest stable boundary and operators can map mentally.
 */
export function startOfTodayUtc(now: Date = new Date()): Date {
  return new Date(
    Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate()),
  );
}

export async function countWorkflowRunsSince(
  orgId: string,
  workflowId: string,
  since: Date,
): Promise<number> {
  const rows = await db()
    .select({ id: workflow_run.id })
    .from(workflow_run)
    .where(
      and(
        eq(workflow_run.org_id, orgId),
        eq(workflow_run.workflow_id, workflowId),
        sql`${workflow_run.created_at} >= ${since}`,
      ),
    );
  return rows.length;
}

export type RecentOutputSummary = {
  id: string;
  kind: string;
  scope: string | null;
  topic: string | null;
  mood: string | null;
  createdAt: Date;
};

export async function listRecentOutputsByWorkflow(
  orgId: string,
  workflowId: string,
  limit = 100,
): Promise<RecentOutputSummary[]> {
  const rows = await db()
    .select({
      id: workflow_output.id,
      kind: workflow_output.kind,
      scope: workflow_output.scope,
      topic: workflow_output.topic,
      mood: workflow_output.mood,
      created_at: workflow_output.created_at,
      workflow_id: workflow_run.workflow_id,
    })
    .from(workflow_output)
    .innerJoin(
      workflow_run,
      eq(workflow_run.id, workflow_output.workflow_run_id),
    )
    .where(
      and(
        eq(workflow_output.org_id, orgId),
        eq(workflow_run.workflow_id, workflowId),
      ),
    )
    .orderBy(desc(workflow_output.created_at))
    .limit(limit);
  return rows.map((r) => ({
    id: r.id,
    kind: r.kind,
    scope: r.scope,
    topic: r.topic,
    mood: r.mood,
    createdAt: r.created_at,
  }));
}

// ─── data_source helpers ────────────────────────────────────────────────────

export type DataSourceContext = {
  id: string;
  graphqlUrl: string;
  subscriptionUrl: string | null;
  mcpUrl: string | null;
};

export async function getDataSourceForOrg(
  orgId: string,
): Promise<DataSourceContext | null> {
  const rows = await db()
    .select({
      id: data_source.id,
      graphql_url: data_source.graphql_url,
      subscription_url: data_source.subscription_url,
      mcp_url: data_source.mcp_url,
    })
    .from(data_source)
    .where(eq(data_source.org_id, orgId))
    .limit(1);
  const row = rows[0];
  if (!row) return null;
  return {
    id: row.id,
    graphqlUrl: row.graphql_url,
    subscriptionUrl: row.subscription_url,
    mcpUrl: row.mcp_url,
  };
}

// ─── source_change helpers ──────────────────────────────────────────────────

/**
 * Has any recent workflow_run of `workflowId` mutated (table, primaryKey)?
 * Backs the source_change cycle check — a responder workflow that writes
 * back to the table it's subscribed to would otherwise re-trigger itself.
 * Adapters populate `workflow_run.source_writes` (jsonb array of
 * `{table, primary_key}`); we check containment via `@>`.
 */
export async function hasRecentSourceWriteForWorkflow(args: {
  workflowId: string;
  table: string;
  primaryKey: Record<string, unknown>;
  sinceMs: number;
}): Promise<boolean> {
  const since = new Date(Date.now() - args.sinceMs);
  const needle = JSON.stringify([
    { table: args.table, primary_key: args.primaryKey },
  ]);
  const rows = await db()
    .select({ id: workflow_run.id })
    .from(workflow_run)
    .where(
      and(
        eq(workflow_run.workflow_id, args.workflowId),
        sql`${workflow_run.created_at} >= ${since}`,
        sql`${workflow_run.source_writes} @> ${needle}::jsonb`,
      ),
    )
    .limit(1);
  return rows.length > 0;
}

export async function writeSourceChangeLog(args: {
  orgId: string;
  sourceId: string;
  tableName: string;
  changeKind: string;
  payload: Record<string, unknown>;
}): Promise<void> {
  await db()
    .insert(source_change_log)
    .values({
      org_id: args.orgId,
      source_id: args.sourceId,
      table_name: args.tableName,
      change_kind: args.changeKind,
      payload: args.payload,
    });
}
