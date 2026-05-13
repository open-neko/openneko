import {
  and,
  db,
  desc,
  eq,
  sql,
  workflow_definition,
  workflow_output,
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
  outputContract: Record<string, unknown> | null;
  createdByThreadId: string | null;
  createdByRunId: string | null;
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
  outputContract?: Record<string, unknown> | null;
  createdByThreadId?: string | null;
  createdByRunId?: string | null;
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
    outputContract:
      (row.output_contract as Record<string, unknown> | null) ?? null,
    createdByThreadId: row.created_by_thread_id,
    createdByRunId: row.created_by_run_id,
    createdAt: row.created_at,
    updatedAt: row.updated_at,
  };
}

export async function getWorkflowByOrgName(
  orgId: string,
  name: string,
): Promise<WorkflowRecord | null> {
  const rows = await db()
    .select()
    .from(workflow_definition)
    .where(
      and(
        eq(workflow_definition.org_id, orgId),
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
  const existing = await getWorkflowByOrgName(input.orgId, input.name);
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
        output_contract:
          input.outputContract === undefined
            ? existing.outputContract
            : input.outputContract,
        updated_at: new Date(),
      })
      .where(eq(workflow_definition.id, existing.id))
      .returning();
    return { action: "updated", workflow: toRecord(row) };
  }

  const [row] = await db()
    .insert(workflow_definition)
    .values({
      org_id: input.orgId,
      name: input.name,
      description: input.description ?? "",
      system_prompt_overlay: input.systemPromptOverlay ?? "",
      steps: input.steps,
      goal: input.goal ?? "",
      cron,
      cron_timezone: cronTimezone,
      cron_enabled: cronEnabled,
      output_contract: input.outputContract ?? null,
      created_by_thread_id: input.createdByThreadId ?? null,
      created_by_run_id: input.createdByRunId ?? null,
    })
    .returning();
  return { action: "created", workflow: toRecord(row) };
}

export type WorkflowRunRecord = {
  id: string;
  orgId: string;
  workflowId: string;
  threadId: string;
  workRunId: string;
  triggerKind: "manual" | "cron" | "subscription";
  triggerPayload: Record<string, unknown>;
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
